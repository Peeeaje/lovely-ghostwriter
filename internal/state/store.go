package state

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Status string

const (
	StatusDetected Status = "detected"
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusReviewed Status = "reviewed"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

type PullRequest struct {
	Repository string
	Number     int
	HeadSHA    string
	Title      string
	URL        string
	Author     string
	BaseBranch string
	Status     Status
	DetectedAt time.Time
	UpdatedAt  time.Time
}

type Store struct {
	db *sql.DB
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
  status TEXT NOT NULL,
  detected_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (repository, number, head_sha)
);

CREATE INDEX IF NOT EXISTS pull_requests_status_idx
  ON pull_requests(status, updated_at DESC);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate state database: %w", err)
	}
	return nil
}

func (s *Store) UpsertPullRequest(ctx context.Context, pr PullRequest) (bool, error) {
	now := time.Now().UTC().Truncate(time.Second)
	result, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO pull_requests (
  repository, number, head_sha, title, url, author, base_branch, status, detected_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, pr.Repository, pr.Number, pr.HeadSHA, pr.Title, pr.URL, pr.Author, pr.BaseBranch, pr.Status, now.Format(time.RFC3339), now.Format(time.RFC3339))
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
SET title = ?, url = ?, author = ?, base_branch = ?, updated_at = ?
WHERE repository = ? AND number = ? AND head_sha = ?
`, pr.Title, pr.URL, pr.Author, pr.BaseBranch, now.Format(time.RFC3339), pr.Repository, pr.Number, pr.HeadSHA); err != nil {
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
SELECT repository, number, head_sha, title, url, author, base_branch, status, detected_at, updated_at
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
		if err := rows.Scan(&pr.Repository, &pr.Number, &pr.HeadSHA, &pr.Title, &pr.URL, &pr.Author, &pr.BaseBranch, &pr.Status, &detectedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan pull request: %w", err)
		}
		pr.DetectedAt, _ = time.Parse(time.RFC3339, detectedAt)
		pr.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		prs = append(prs, pr)
	}
	return prs, rows.Err()
}
