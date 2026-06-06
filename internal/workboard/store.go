// Package workboard persists Kanban-style work items for agents.
// Tasks move through a fixed lifecycle:
// todo → running → needs_review → done (or failed).
package workboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

// Task statuses (Kanban columns).
const (
	StatusTodo        = "todo"
	StatusRunning     = "running"
	StatusNeedsReview = "needs_review"
	StatusDone        = "done"
	StatusFailed      = "failed"
)

// Statuses lists all valid statuses in board order.
var Statuses = []string{StatusTodo, StatusRunning, StatusNeedsReview, StatusDone, StatusFailed}

// ValidStatus reports whether s is a recognised task status.
func ValidStatus(s string) bool {
	switch s {
	case StatusTodo, StatusRunning, StatusNeedsReview, StatusDone, StatusFailed:
		return true
	}
	return false
}

// Sentinel errors. API layers map ErrInvalid → 400 and ErrNotFound → 404.
var (
	ErrNotFound = errors.New("workboard: task not found")
	ErrInvalid  = errors.New("workboard: invalid task")
)

// Task is one work item on the board.
type Task struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	AgentID     string    `json:"agent_id"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Filter narrows List results. Zero values match everything.
type Filter struct {
	Status  string
	AgentID string
}

// Update describes a partial update; nil fields are left unchanged.
type Update struct {
	Title       *string
	Description *string
	AgentID     *string
	Status      *string
}

// Store persists tasks to SQLite.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS workboard_tasks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    agent_id    TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'todo',
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wb_status  ON workboard_tasks(status);
CREATE INDEX IF NOT EXISTS idx_wb_agent   ON workboard_tasks(agent_id);
CREATE INDEX IF NOT EXISTS idx_wb_updated ON workboard_tasks(updated_at);
`

// NewStore opens (or creates) the workboard SQLite database at path.
func NewStore(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(runsSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

const timeLayout = "2006-01-02 15:04:05"

// Create inserts a new task. Status defaults to todo; title must be non-blank.
func (s *Store) Create(ctx context.Context, t Task) (Task, error) {
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		return Task{}, fmt.Errorf("%w: title is required", ErrInvalid)
	}
	if t.Status == "" {
		t.Status = StatusTodo
	}
	if !ValidStatus(t.Status) {
		return Task{}, fmt.Errorf("%w: unknown status %q", ErrInvalid, t.Status)
	}
	// Truncate to the stored DATETIME resolution so the returned struct
	// matches what a later Get/Update reads back from SQLite.
	now := time.Now().UTC().Truncate(time.Second)
	t.CreatedAt = now
	t.UpdatedAt = now
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO workboard_tasks (title, description, agent_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.Title, t.Description, t.AgentID, t.Status,
		now.Format(timeLayout), now.Format(timeLayout),
	)
	if err != nil {
		return Task{}, err
	}
	t.ID, err = res.LastInsertId()
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

// Get returns one task by ID, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id int64) (Task, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, title, description, agent_id, status, created_at, updated_at
		 FROM workboard_tasks WHERE id = ?`, id)
	return scanTask(row)
}

// List returns tasks matching f, newest first.
func (s *Store) List(ctx context.Context, f Filter) ([]Task, error) {
	if f.Status != "" && !ValidStatus(f.Status) {
		return nil, fmt.Errorf("%w: unknown status %q", ErrInvalid, f.Status)
	}
	q := `SELECT id, title, description, agent_id, status, created_at, updated_at
	      FROM workboard_tasks WHERE 1=1`
	args := []any{}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.AgentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, f.AgentID)
	}
	q += ` ORDER BY created_at DESC, id DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Update applies a partial update and returns the resulting task.
func (s *Store) Update(ctx context.Context, id int64, u Update) (Task, error) {
	if u.Status != nil && !ValidStatus(*u.Status) {
		return Task{}, fmt.Errorf("%w: unknown status %q", ErrInvalid, *u.Status)
	}
	if u.Title != nil && strings.TrimSpace(*u.Title) == "" {
		return Task{}, fmt.Errorf("%w: title cannot be blank", ErrInvalid)
	}

	sets := []string{"updated_at = ?"}
	args := []any{time.Now().UTC().Format(timeLayout)}
	if u.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, strings.TrimSpace(*u.Title))
	}
	if u.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *u.Description)
	}
	if u.AgentID != nil {
		sets = append(sets, "agent_id = ?")
		args = append(args, *u.AgentID)
	}
	if u.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, *u.Status)
	}
	args = append(args, id)

	res, err := s.db.ExecContext(ctx,
		`UPDATE workboard_tasks SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return Task{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if n == 0 {
		return Task{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Delete removes a task and its run history, or returns ErrNotFound.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workboard_tasks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM workboard_runs WHERE task_id = ?`, id)
	return err
}

// Close closes the DB.
func (s *Store) Close() error {
	return s.db.Close()
}

// scanner covers both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.AgentID, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return t, nil
}
