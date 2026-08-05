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
	StatusStale     Status = "stale"
	StatusRejected  Status = "rejected"
)

type PullRequest struct {
	Repository      string
	Number          int
	HeadSHA         string
	Title           string
	URL             string
	Author          string
	BaseBranch      string
	BaseSHA         string
	Manual          bool
	RecoveryRunID   int64
	CancelRequested bool
	Status          Status
	DetectedAt      time.Time
	UpdatedAt       time.Time
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

type ArtifactHandoff struct {
	HeadSHA string
	Path    string
}

type StopRequestedError struct {
	Status Status
}

func (e StopRequestedError) Error() string {
	return fmt.Sprintf("pull request stop requested with status %s", e.Status)
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
  recovery_run_id INTEGER NOT NULL DEFAULT 0,
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  cancel_status TEXT NOT NULL DEFAULT 'rejected',
  status TEXT NOT NULL,
  detected_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (repository, number, head_sha)
);

CREATE INDEX IF NOT EXISTS pull_requests_status_idx
  ON pull_requests(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS current_pull_requests (
  repository TEXT NOT NULL,
  number INTEGER NOT NULL,
  head_sha TEXT NOT NULL,
  PRIMARY KEY (repository, number)
);

CREATE TABLE IF NOT EXISTS pull_request_targets (
  repository TEXT NOT NULL,
  number INTEGER NOT NULL,
  head_sha TEXT NOT NULL,
  base_branch TEXT NOT NULL,
  detected_at TEXT NOT NULL,
  PRIMARY KEY (repository, number, head_sha, base_branch)
);

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
	if err := s.ensureColumn(ctx, "pull_requests", "recovery_run_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "pull_requests", "cancel_requested", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "pull_requests", "cancel_status", "TEXT NOT NULL DEFAULT 'rejected'"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO pull_request_targets (repository, number, head_sha, base_branch, detected_at)
SELECT repository, number, head_sha, base_branch, detected_at FROM pull_requests
`); err != nil {
		return fmt.Errorf("backfill pull request targets: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO current_pull_requests (repository, number, head_sha)
SELECT repository, number, head_sha FROM pull_requests current
WHERE current.rowid = (
  SELECT latest.rowid FROM pull_requests latest
  WHERE latest.repository = current.repository AND latest.number = current.number
  ORDER BY latest.updated_at DESC, latest.rowid DESC LIMIT 1
)
`); err != nil {
		return fmt.Errorf("backfill current pull request heads: %w", err)
	}
	return nil
}

func (s *Store) SetCurrentHead(ctx context.Context, repository string, number int, headSHA string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO current_pull_requests (repository, number, head_sha) VALUES (?, ?, ?)
ON CONFLICT(repository, number) DO UPDATE SET head_sha = excluded.head_sha
`, repository, number, headSHA)
	if err != nil {
		return fmt.Errorf("set current pull request head: %w", err)
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
SELECT repository, number, head_sha, title, url, author, base_branch, base_sha, manual, recovery_run_id, status, detected_at, updated_at
FROM pull_requests queued
WHERE status = 'queued'
  AND NOT EXISTS (
    SELECT 1 FROM pull_requests running
    WHERE running.repository = queued.repository
      AND running.number = queued.number
      AND running.status = 'running'
  )
ORDER BY detected_at, repository, number
LIMIT 1
`).Scan(&pr.Repository, &pr.Number, &pr.HeadSHA, &pr.Title, &pr.URL, &pr.Author, &pr.BaseBranch, &pr.BaseSHA, &pr.Manual, &pr.RecoveryRunID, &pr.Status, &detectedAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequest{}, Run{}, false, nil
	}
	if err != nil {
		return PullRequest{}, Run{}, false, fmt.Errorf("select queued pull request: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	result, err := tx.ExecContext(ctx, `
UPDATE pull_requests SET status = 'running', recovery_run_id = 0, cancel_requested = 0, cancel_status = 'rejected', updated_at = ?
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

func (s *Store) SetRunningTarget(ctx context.Context, pr PullRequest) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE pull_requests SET base_branch = ?, base_sha = ?, updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ? AND status = 'running'
`, pr.BaseBranch, pr.BaseSHA, time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), pr.Repository, pr.Number, pr.HeadSHA)
	if err != nil {
		return fmt.Errorf("update running pull request target: %w", err)
	}
	return nil
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

func (s *Store) RetargetRun(ctx context.Context, run *Run, previous, current PullRequest) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin run retarget: %w", err)
	}
	defer tx.Rollback()
	var canceled bool
	var cancelStatus Status
	if err := tx.QueryRowContext(ctx, `
SELECT cancel_requested, cancel_status FROM pull_requests
WHERE repository = ? AND number = ? AND head_sha = ?
	`, previous.Repository, previous.Number, previous.HeadSHA).Scan(&canceled, &cancelStatus); err != nil {
		return false, fmt.Errorf("read previous head before retarget: %w", err)
	}
	if canceled {
		return false, StopRequestedError{Status: cancelStatus}
	}
	var currentStatus Status
	var currentManual bool
	err = tx.QueryRowContext(ctx, `
SELECT status, manual FROM pull_requests
WHERE repository = ? AND number = ? AND head_sha = ?
`, current.Repository, current.Number, current.HeadSHA).Scan(&currentStatus, &currentManual)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read current head before retarget: %w", err)
	}
	if currentStatus == StatusRejected || currentStatus == StatusCanceled {
		return false, StopRequestedError{Status: currentStatus}
	}
	effectiveManual := previous.Manual || current.Manual || currentManual
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
UPDATE pull_requests SET status = 'stale', updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ? AND status = 'running'
	`, now, previous.Repository, previous.Number, previous.HeadSHA); err != nil {
		return false, fmt.Errorf("mark previous head stale: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pull_request_targets (repository, number, head_sha, base_branch, detected_at)
SELECT ?, ?, ?, ?, ?
WHERE NOT EXISTS (
  SELECT 1 FROM pull_request_targets
  WHERE repository = ? AND number = ? AND head_sha = ? AND base_branch = ?
)
`, current.Repository, current.Number, current.HeadSHA, current.BaseBranch, now, current.Repository, current.Number, current.HeadSHA, current.BaseBranch); err != nil {
		return false, fmt.Errorf("record current target: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pull_requests (
  repository, number, head_sha, title, url, author, base_branch, base_sha,
  manual, recovery_run_id, cancel_requested, status, detected_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 'running', ?, ?)
ON CONFLICT(repository, number, head_sha) DO UPDATE SET
  title = excluded.title,
  url = excluded.url,
  author = excluded.author,
  base_branch = excluded.base_branch,
  base_sha = excluded.base_sha,
  manual = excluded.manual,
  recovery_run_id = 0,
  cancel_requested = 0,
  status = 'running',
  updated_at = excluded.updated_at
`, current.Repository, current.Number, current.HeadSHA, current.Title, current.URL, current.Author,
		current.BaseBranch, current.BaseSHA, effectiveManual, now, now); err != nil {
		return false, fmt.Errorf("activate current head: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO current_pull_requests (repository, number, head_sha) VALUES (?, ?, ?)
ON CONFLICT(repository, number) DO UPDATE SET head_sha = excluded.head_sha
`, current.Repository, current.Number, current.HeadSHA); err != nil {
		return false, fmt.Errorf("set current head after retarget: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runs SET
  head_sha = ?,
  attempt = (
    SELECT COALESCE(MAX(other.attempt), 0) + 1 FROM runs other
    WHERE other.repository = runs.repository
      AND other.number = runs.number
      AND other.head_sha = ?
      AND other.id <> runs.id
  )
WHERE id = ? AND status = 'running'
	`, current.HeadSHA, current.HeadSHA, run.ID); err != nil {
		return false, fmt.Errorf("retarget run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit run retarget: %w", err)
	}
	run.HeadSHA = current.HeadSHA
	return effectiveManual, nil
}

func (s *Store) RequestCancel(ctx context.Context, repository string, number int) (int64, error) {
	return s.requestStop(ctx, repository, number, StatusRejected)
}

func (s *Store) RequestStale(ctx context.Context, repository string, number int) (int64, error) {
	return s.requestStop(ctx, repository, number, StatusStale)
}

func (s *Store) requestStop(ctx context.Context, repository string, number int, status Status) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE pull_requests
SET cancel_requested = CASE WHEN status = 'running' THEN 1 ELSE cancel_requested END,
    cancel_status = CASE WHEN status = 'running' THEN ? ELSE cancel_status END,
    status = CASE WHEN status = 'running' THEN status ELSE ? END,
    updated_at = ?
WHERE repository = ? AND number = ? AND status IN ('detected', 'queued', 'running', 'failed')
`, status, status, time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), repository, number)
	if err != nil {
		return 0, fmt.Errorf("cancel pull request: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) CancellationRequested(ctx context.Context, repository string, number int, headSHA string) (Status, bool, error) {
	var requested bool
	var status Status
	err := s.db.QueryRowContext(ctx, `
SELECT cancel_requested, cancel_status FROM pull_requests
WHERE repository = ? AND number = ? AND head_sha = ?
`, repository, number, headSHA).Scan(&requested, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read cancellation request: %w", err)
	}
	return status, requested, nil
}

func (s *Store) FinishRun(ctx context.Context, run Run, status Status, runError error) error {
	_, err := s.FinishRunStatus(ctx, run, status, runError)
	return err
}

func (s *Store) FinishRunStatus(ctx context.Context, run Run, status Status, runError error) (Status, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return status, fmt.Errorf("begin run finish: %w", err)
	}
	defer tx.Rollback()
	var cancelRequested bool
	var cancelStatus Status
	if err := tx.QueryRowContext(ctx, `
SELECT cancel_requested, cancel_status FROM pull_requests
WHERE repository = ? AND number = ? AND head_sha = ?
`, run.Repository, run.Number, run.HeadSHA).Scan(&cancelRequested, &cancelStatus); err != nil {
		return status, fmt.Errorf("read stop request before finish: %w", err)
	}
	if cancelRequested {
		status = cancelStatus
	}

	now := time.Now().UTC().Truncate(time.Second)
	errorText := ""
	if runError != nil {
		errorText = runError.Error()
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runs SET status = ?, ended_at = ?, error = ? WHERE id = ?
	`, status, now.Format(time.RFC3339), errorText, run.ID); err != nil {
		return status, fmt.Errorf("finish run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE pull_requests SET status = ?, manual = 0, updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ?
	`, status, now.Format(time.RFC3339), run.Repository, run.Number, run.HeadSHA); err != nil {
		return status, fmt.Errorf("finish pull request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return status, fmt.Errorf("commit run finish: %w", err)
	}
	return status, nil
}

func (s *Store) RecoverInterrupted(ctx context.Context) ([]Run, error) {
	runs, err := s.runningRuns(ctx)
	if err != nil || len(runs) == 0 {
		return runs, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin interrupted run recovery: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	for _, run := range runs {
		if _, err := tx.ExecContext(ctx, `
UPDATE runs SET status = 'failed', ended_at = ?, error = 'queue consumer stopped before completion'
WHERE id = ? AND status = 'running'
`, now, run.ID); err != nil {
			return nil, fmt.Errorf("recover run %d: %w", run.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE pull_requests SET
  status = CASE WHEN cancel_requested = 1 THEN cancel_status ELSE 'queued' END,
  recovery_run_id = CASE WHEN cancel_requested = 1 THEN 0 ELSE ? END,
  updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ? AND status = 'running'
`, run.ID, now, run.Repository, run.Number, run.HeadSHA); err != nil {
			return nil, fmt.Errorf("requeue run %d: %w", run.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit interrupted run recovery: %w", err)
	}
	return runs, nil
}

func (s *Store) runningRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repository, number, head_sha, attempt, status, started_at,
       COALESCE(ended_at, ''), log_path, artifact_path, worktree_path, error
FROM runs WHERE status = 'running' ORDER BY started_at
`)
	if err != nil {
		return nil, fmt.Errorf("list running runs: %w", err)
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		var run Run
		var startedAt, endedAt string
		if err := rows.Scan(&run.ID, &run.Repository, &run.Number, &run.HeadSHA, &run.Attempt, &run.Status, &startedAt, &endedAt, &run.LogPath, &run.ArtifactPath, &run.WorktreePath, &run.Error); err != nil {
			return nil, fmt.Errorf("scan running run: %w", err)
		}
		run.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
		runs = append(runs, run)
	}
	return runs, rows.Err()
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
UPDATE pull_requests SET status = 'queued', manual = 1, updated_at = ?
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
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO pull_request_targets (repository, number, head_sha, base_branch, detected_at)
SELECT ?, ?, ?, ?, ?
WHERE NOT EXISTS (
  SELECT 1 FROM pull_request_targets
  WHERE repository = ? AND number = ? AND head_sha = ? AND base_branch = ?
)
`, pr.Repository, pr.Number, pr.HeadSHA, pr.BaseBranch, now.Format(time.RFC3339), pr.Repository, pr.Number, pr.HeadSHA, pr.BaseBranch); err != nil {
		return false, fmt.Errorf("record pull request target %s#%d: %w", pr.Repository, pr.Number, err)
	}
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
SET title = ?, url = ?, author = ?,
    status = CASE
      WHEN manual = 0 AND ? = 0 AND status IN ('detected', 'canceled') AND ? = 'queued' THEN ?
      WHEN manual = 0 AND base_branch <> ? AND status IN ('detected', 'canceled', 'failed', 'completed', 'reviewed') THEN ?
      ELSE status
    END,
    base_branch = CASE WHEN status = 'running' THEN base_branch ELSE ? END,
    base_sha = CASE WHEN status = 'running' THEN base_sha ELSE ? END,
    updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ?
`, pr.Title, pr.URL, pr.Author, pr.Manual, pr.Status, pr.Status, pr.BaseBranch, pr.Status, pr.BaseBranch, pr.BaseSHA, now.Format(time.RFC3339), pr.Repository, pr.Number, pr.HeadSHA); err != nil {
			return false, fmt.Errorf("update pull request %s#%d: %w", pr.Repository, pr.Number, err)
		}
	}
	if err := s.SetCurrentHead(ctx, pr.Repository, pr.Number, pr.HeadSHA); err != nil {
		return false, err
	}
	return rows == 1, nil
}

func (s *Store) LatestFailedRunID(ctx context.Context, repository string, number int, headSHA string) (int64, bool, error) {
	var runID int64
	err := s.db.QueryRowContext(ctx, `
SELECT id FROM runs
WHERE repository = ? AND number = ? AND head_sha = ? AND status = 'failed'
ORDER BY id DESC LIMIT 1
`, repository, number, headSHA).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find latest failed run: %w", err)
	}
	return runID, true, nil
}

func (s *Store) HasPreviousTarget(ctx context.Context, repository string, number int, headSHA, baseBranch string) (bool, error) {
	var previous bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pull_request_targets
  WHERE repository = ? AND number = ? AND (head_sha <> ? OR base_branch <> ?)
)
`, repository, number, headSHA, baseBranch).Scan(&previous)
	if err != nil {
		return false, fmt.Errorf("check previous pull request target: %w", err)
	}
	return previous, nil
}

func (s *Store) TargetExists(ctx context.Context, repository string, number int, headSHA, baseBranch string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pull_request_targets
  WHERE repository = ? AND number = ? AND head_sha = ? AND base_branch = ?
)
`, repository, number, headSHA, baseBranch).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check pull request target: %w", err)
	}
	return exists, nil
}

func (s *Store) MarkReviewed(ctx context.Context, repository string, number int, headSHA string, runID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin review reconciliation: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	result, err := tx.ExecContext(ctx, `
UPDATE runs SET status = 'reviewed', ended_at = ?, error = ''
WHERE id = ? AND repository = ? AND number = ? AND head_sha = ? AND status = 'failed'
`, now, runID, repository, number, headSHA)
	if err != nil {
		return fmt.Errorf("reconcile failed run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("failed run %d is no longer eligible for reconciliation", runID)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE pull_requests SET status = 'reviewed', updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ? AND status = 'failed'
`, now, repository, number, headSHA); err != nil {
		return fmt.Errorf("reconcile reviewed pull request: %w", err)
	}
	return tx.Commit()
}

func (s *Store) Counts(ctx context.Context) (map[Status]int, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT status, COUNT(*) FROM pull_requests
JOIN current_pull_requests USING (repository, number, head_sha)
GROUP BY status`)
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
	return s.PullRequests(ctx, false)
}

func (s *Store) PullRequests(ctx context.Context, all bool) ([]PullRequest, error) {
	predicate := `JOIN current_pull_requests USING (repository, number, head_sha)
WHERE status IN ('detected', 'queued', 'running', 'failed')`
	if all {
		predicate = ""
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT repository, number, head_sha, title, url, author, base_branch, base_sha, manual, status, detected_at, updated_at
FROM pull_requests `+predicate+`
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
  AND id = (
    SELECT latest.id FROM runs latest
    WHERE latest.repository = runs.repository
      AND latest.number = runs.number
      AND latest.head_sha = runs.head_sha
    ORDER BY latest.id DESC LIMIT 1
  )
  AND EXISTS (SELECT 1 FROM current_pull_requests current
    WHERE current.repository = runs.repository AND current.number = runs.number AND current.head_sha = runs.head_sha)
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

func (s *Store) Runs(ctx context.Context, all bool) ([]Run, error) {
	predicate := `WHERE status IN ('running', 'failed')
AND id = (
  SELECT latest.id FROM runs latest
  WHERE latest.repository = runs.repository
    AND latest.number = runs.number
    AND latest.head_sha = runs.head_sha
  ORDER BY latest.id DESC LIMIT 1
)
AND EXISTS (SELECT 1 FROM current_pull_requests current
  WHERE current.repository = runs.repository AND current.number = runs.number AND current.head_sha = runs.head_sha)`
	if all {
		predicate = ""
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repository, number, head_sha, attempt, status, started_at,
       COALESCE(ended_at, ''), log_path, artifact_path, worktree_path, error
FROM runs `+predicate+` ORDER BY started_at DESC
`)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
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

func (s *Store) PreviousArtifact(ctx context.Context, repository string, number int, headSHA string) (ArtifactHandoff, error) {
	var handoff ArtifactHandoff
	err := s.db.QueryRowContext(ctx, `
SELECT head_sha, artifact_path FROM runs
WHERE repository = ? AND number = ? AND head_sha <> ? AND artifact_path <> ''
ORDER BY id DESC LIMIT 1
`, repository, number, headSHA).Scan(&handoff.HeadSHA, &handoff.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactHandoff{}, nil
	}
	if err != nil {
		return ArtifactHandoff{}, fmt.Errorf("find previous artifact: %w", err)
	}
	return handoff, nil
}
