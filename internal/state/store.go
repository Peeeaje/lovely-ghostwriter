package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Status string

const (
	StatusDetected  Status = "detected"
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusReviewed  Status = "reviewed"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

type PullRequest struct {
	Repository string
	Number     int
	HeadSHA    string
	Title      string
	URL        string
	Author     string
	BaseBranch string
	BaseSHA    string
	Manual     bool
	Status     Status
	DetectedAt time.Time
	UpdatedAt  time.Time
}

type Store struct {
	db *sql.DB
}

type Run struct {
	ID           int64
	Repository   string
	Number       int
	HeadSHA      string
	Attempt      int
	Status       Status
	StartedAt    time.Time
	EndedAt      time.Time
	LogPath      string
	ArtifactPath string
	WorktreePath string
	Error        string
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS pull_requests (
  repository TEXT NOT NULL,
  number INTEGER NOT NULL,
  head_sha TEXT NOT NULL,
  title TEXT NOT NULL,
  url TEXT NOT NULL,
  author TEXT NOT NULL,
  base_branch TEXT NOT NULL,
  base_sha TEXT NOT NULL DEFAULT '',
  manual INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  detected_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (repository, number, head_sha)
);

CREATE INDEX IF NOT EXISTS pull_requests_status_idx
  ON pull_requests(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repository TEXT NOT NULL,
  number INTEGER NOT NULL,
  head_sha TEXT NOT NULL,
  attempt INTEGER NOT NULL,
  status TEXT NOT NULL,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  log_path TEXT NOT NULL DEFAULT '',
  artifact_path TEXT NOT NULL DEFAULT '',
  worktree_path TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  UNIQUE (repository, number, head_sha, attempt)
);

CREATE INDEX IF NOT EXISTS runs_status_idx ON runs(status, started_at DESC);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate state database: %w", err)
	}
	if err := s.ensureColumn(ctx, "pull_requests", "base_sha", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "pull_requests", "manual", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan %s columns: %w", table, err)
		}
		found = found || name == column
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func (s *Store) ClaimNext(ctx context.Context) (PullRequest, Run, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PullRequest{}, Run{}, false, fmt.Errorf("begin queue claim: %w", err)
	}
	defer tx.Rollback()

	var pr PullRequest
	var detectedAt, updatedAt string
	err = tx.QueryRowContext(ctx, `
SELECT repository, number, head_sha, title, url, author, base_branch, base_sha, manual, status, detected_at, updated_at
FROM pull_requests
WHERE status = 'queued'
ORDER BY detected_at, repository, number
LIMIT 1
`).Scan(&pr.Repository, &pr.Number, &pr.HeadSHA, &pr.Title, &pr.URL, &pr.Author, &pr.BaseBranch, &pr.BaseSHA, &pr.Manual, &pr.Status, &detectedAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequest{}, Run{}, false, nil
	}
	if err != nil {
		return PullRequest{}, Run{}, false, fmt.Errorf("select queued pull request: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	result, err := tx.ExecContext(ctx, `
UPDATE pull_requests SET status = 'running', updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ? AND status = 'queued'
`, now.Format(time.RFC3339), pr.Repository, pr.Number, pr.HeadSHA)
	if err != nil {
		return PullRequest{}, Run{}, false, fmt.Errorf("claim pull request: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return PullRequest{}, Run{}, false, err
	}

	var run Run
	err = tx.QueryRowContext(ctx, `
INSERT INTO runs (repository, number, head_sha, attempt, status, started_at)
VALUES (?, ?, ?, (
  SELECT COALESCE(MAX(attempt), 0) + 1 FROM runs
  WHERE repository = ? AND number = ? AND head_sha = ?
), 'running', ?)
RETURNING id, attempt
`, pr.Repository, pr.Number, pr.HeadSHA, pr.Repository, pr.Number, pr.HeadSHA, now.Format(time.RFC3339)).Scan(&run.ID, &run.Attempt)
	if err != nil {
		return PullRequest{}, Run{}, false, fmt.Errorf("create run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PullRequest{}, Run{}, false, fmt.Errorf("commit queue claim: %w", err)
	}

	pr.Status = StatusRunning
	pr.DetectedAt, _ = time.Parse(time.RFC3339, detectedAt)
	pr.UpdatedAt = now
	run.Repository = pr.Repository
	run.Number = pr.Number
	run.HeadSHA = pr.HeadSHA
	run.Status = StatusRunning
	run.StartedAt = now
	return pr, run, true, nil
}

func (s *Store) SetRunPaths(ctx context.Context, runID int64, logPath, artifactPath, worktreePath string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE runs SET log_path = ?, artifact_path = ?, worktree_path = ? WHERE id = ?
`, logPath, artifactPath, worktreePath, runID)
	if err != nil {
		return fmt.Errorf("set run paths: %w", err)
	}
	return nil
}

func (s *Store) FinishRun(ctx context.Context, run Run, status Status, runError error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin run finish: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Truncate(time.Second)
	errorText := ""
	if runError != nil {
		errorText = runError.Error()
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runs SET status = ?, ended_at = ?, error = ? WHERE id = ?
`, status, now.Format(time.RFC3339), errorText, run.ID); err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE pull_requests SET status = ?, updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ?
`, status, now.Format(time.RFC3339), run.Repository, run.Number, run.HeadSHA); err != nil {
		return fmt.Errorf("finish pull request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run finish: %w", err)
	}
	return nil
}

func (s *Store) Enqueue(ctx context.Context, pr PullRequest, force bool) (bool, error) {
	created, err := s.UpsertPullRequest(ctx, pr)
	if err != nil || created {
		return created, err
	}
	predicate := "status = 'detected'"
	if force {
		predicate = "status != 'running'"
	}
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `
UPDATE pull_requests SET status = 'queued', updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ? AND `+predicate,
		now, pr.Repository, pr.Number, pr.HeadSHA)
	if err != nil {
		return false, fmt.Errorf("requeue pull request: %w", err)
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) UpsertPullRequest(ctx context.Context, pr PullRequest) (bool, error) {
	now := time.Now().UTC().Truncate(time.Second)
	result, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO pull_requests (
  repository, number, head_sha, title, url, author, base_branch, base_sha, manual, status, detected_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, pr.Repository, pr.Number, pr.HeadSHA, pr.Title, pr.URL, pr.Author, pr.BaseBranch, pr.BaseSHA, pr.Manual, pr.Status, now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return false, fmt.Errorf("insert pull request %s#%d: %w", pr.Repository, pr.Number, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read insert result: %w", err)
	}
	if rows == 0 {
		if _, err := s.db.ExecContext(ctx, `
UPDATE pull_requests
SET title = ?, url = ?, author = ?, base_branch = ?, base_sha = ?,
    manual = CASE WHEN ? THEN 1 ELSE manual END, updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ?
`, pr.Title, pr.URL, pr.Author, pr.BaseBranch, pr.BaseSHA, pr.Manual, now.Format(time.RFC3339), pr.Repository, pr.Number, pr.HeadSHA); err != nil {
			return false, fmt.Errorf("update pull request %s#%d: %w", pr.Repository, pr.Number, err)
		}
	}
	return rows == 1, nil
}

func (s *Store) Counts(ctx context.Context) (map[Status]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM pull_requests GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count pull requests: %w", err)
	}
	defer rows.Close()

	counts := make(map[Status]int)
	for rows.Next() {
		var status Status
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan status count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Store) Active(ctx context.Context) ([]PullRequest, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT repository, number, head_sha, title, url, author, base_branch, base_sha, manual, status, detected_at, updated_at
FROM pull_requests
WHERE status IN ('detected', 'queued', 'running', 'failed')
ORDER BY updated_at DESC
`)
	if err != nil {
		return nil, fmt.Errorf("list active pull requests: %w", err)
	}
	defer rows.Close()

	var prs []PullRequest
	for rows.Next() {
		var pr PullRequest
		var detectedAt, updatedAt string
		if err := rows.Scan(&pr.Repository, &pr.Number, &pr.HeadSHA, &pr.Title, &pr.URL, &pr.Author, &pr.BaseBranch, &pr.BaseSHA, &pr.Manual, &pr.Status, &detectedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan pull request: %w", err)
		}
		pr.DetectedAt, _ = time.Parse(time.RFC3339, detectedAt)
		pr.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		prs = append(prs, pr)
	}
	return prs, rows.Err()
}

func (s *Store) ActiveRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repository, number, head_sha, attempt, status, started_at,
       COALESCE(ended_at, ''), log_path, artifact_path, worktree_path, error
FROM runs
WHERE status IN ('running', 'failed')
ORDER BY started_at DESC
`)
	if err != nil {
		return nil, fmt.Errorf("list active runs: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var run Run
		var startedAt, endedAt string
		if err := rows.Scan(&run.ID, &run.Repository, &run.Number, &run.HeadSHA, &run.Attempt, &run.Status, &startedAt, &endedAt, &run.LogPath, &run.ArtifactPath, &run.WorktreePath, &run.Error); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		run.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
		run.EndedAt, _ = time.Parse(time.RFC3339, endedAt)
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
