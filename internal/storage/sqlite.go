package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rodrwan/slack-codex/internal/model"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	// Use one connection to avoid writer lock contention across pooled conns.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return nil, fmt.Errorf("set journal_mode: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000;`); err != nil {
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON;`); err != nil {
		return nil, fmt.Errorf("set foreign_keys: %w", err)
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  base_branch TEXT NOT NULL,
  prompt TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  slack_channel_id TEXT NOT NULL,
  slack_thread_ts TEXT NOT NULL,
  slack_user_id TEXT NOT NULL,
  workspace_path TEXT NOT NULL DEFAULT '',
  job_branch TEXT NOT NULL DEFAULT '',
  last_input TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  last_diff_stat TEXT NOT NULL DEFAULT '',
  pr_url TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS job_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL,
  type TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY(job_id) REFERENCES jobs(id)
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_job_events_job_id ON job_events(job_id);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	return nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func parseTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Now().UTC()
	}
	return t
}

func (s *SQLiteStore) CreateJob(j model.Job) error {
	err := s.execWithRetry(func() error {
		_, e := s.db.Exec(`
	INSERT INTO jobs (id, repo, base_branch, prompt, status, created_at, updated_at, slack_channel_id, slack_thread_ts, slack_user_id, workspace_path, job_branch, last_input, last_error, last_diff_stat, pr_url)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.ID, j.Repo, j.BaseBranch, j.Prompt, string(j.Status), now(), now(), j.SlackChannelID, j.SlackThreadTS, j.SlackUserID,
			j.WorkspacePath, j.JobBranch, j.LastInput, j.LastError, j.LastDiffStat, j.PRURL,
		)
		return e
	})
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetJob(jobID string) (model.Job, error) {
	row := s.db.QueryRow(`
SELECT id, repo, base_branch, prompt, status, created_at, updated_at, slack_channel_id, slack_thread_ts, slack_user_id,
       workspace_path, job_branch, last_input, last_error, last_diff_stat, pr_url
FROM jobs WHERE id = ?`, jobID)
	var (
		j         model.Job
		status    string
		createdAt string
		updatedAt string
	)
	if err := row.Scan(
		&j.ID, &j.Repo, &j.BaseBranch, &j.Prompt, &status, &createdAt, &updatedAt, &j.SlackChannelID, &j.SlackThreadTS, &j.SlackUserID,
		&j.WorkspacePath, &j.JobBranch, &j.LastInput, &j.LastError, &j.LastDiffStat, &j.PRURL,
	); err != nil {
		return model.Job{}, err
	}
	j.Status = model.JobStatus(status)
	j.CreatedAt = parseTime(createdAt)
	j.UpdatedAt = parseTime(updatedAt)
	return j, nil
}

func (s *SQLiteStore) UpdateJob(j model.Job) error {
	err := s.execWithRetry(func() error {
		_, e := s.db.Exec(`
	UPDATE jobs
	SET status=?, updated_at=?, workspace_path=?, job_branch=?, last_input=?, last_error=?, last_diff_stat=?, pr_url=?, slack_thread_ts=?
	WHERE id=?`,
			string(j.Status), now(), j.WorkspacePath, j.JobBranch, j.LastInput, j.LastError, j.LastDiffStat, j.PRURL, j.SlackThreadTS, j.ID,
		)
		return e
	})
	if err != nil {
		return fmt.Errorf("update job: %w", err)
	}
	return nil
}

func (s *SQLiteStore) AddEvent(jobID, eventType, payload string) error {
	err := s.execWithRetry(func() error {
		_, e := s.db.Exec(`
	INSERT INTO job_events(job_id, type, payload, created_at)
	VALUES (?, ?, ?, ?)`, jobID, eventType, payload, now())
		return e
	})
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

func (s *SQLiteStore) execWithRetry(fn func() error) error {
	const maxAttempts = 5
	backoff := 40 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			if !isSQLiteBusyErr(err) || attempt == maxAttempts {
				return err
			}
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		return nil
	}
	return lastErr
}

func isSQLiteBusyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "DATABASE IS LOCKED")
}

func (s *SQLiteStore) ListRecoverableJobs() ([]model.Job, error) {
	rows, err := s.db.Query(`
SELECT id, repo, base_branch, prompt, status, created_at, updated_at, slack_channel_id, slack_thread_ts, slack_user_id,
       workspace_path, job_branch, last_input, last_error, last_diff_stat, pr_url
FROM jobs WHERE status IN ('queued', 'running', 'needs_input', 'needs_approval', 'needs_review')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]model.Job, 0)
	for rows.Next() {
		var (
			j         model.Job
			status    string
			createdAt string
			updatedAt string
		)
		if err := rows.Scan(
			&j.ID, &j.Repo, &j.BaseBranch, &j.Prompt, &status, &createdAt, &updatedAt, &j.SlackChannelID, &j.SlackThreadTS, &j.SlackUserID,
			&j.WorkspacePath, &j.JobBranch, &j.LastInput, &j.LastError, &j.LastDiffStat, &j.PRURL,
		); err != nil {
			return nil, err
		}
		j.Status = model.JobStatus(status)
		j.CreatedAt = parseTime(createdAt)
		j.UpdatedAt = parseTime(updatedAt)
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}
