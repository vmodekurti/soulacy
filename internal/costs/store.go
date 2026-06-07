// Package costs tracks token usage per agent and session.
package costs

import (
	"context"
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

// UsageRecord records one LLM call's token consumption.
type UsageRecord struct {
	AgentID      string
	SessionID    string
	Provider     string
	Model        string
	PromptTokens int
	CompTokens   int
	TotalTokens  int
	CostUSD      float64 // estimated; 0 if pricing not configured
	CreatedAt    time.Time
}

// AgentCost is the aggregated cost summary for one agent.
type AgentCost struct {
	AgentID      string  `json:"agent_id"`
	TotalTokens  int     `json:"total_tokens"`
	PromptTokens int     `json:"prompt_tokens"`
	CompTokens   int     `json:"comp_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// SessionCost is the aggregated cost summary for one session.
type SessionCost struct {
	SessionID   string  `json:"session_id"`
	TotalTokens int     `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// Store persists token usage records to SQLite.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS token_usage (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id      TEXT NOT NULL,
    session_id    TEXT NOT NULL,
    provider      TEXT NOT NULL,
    model         TEXT NOT NULL,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    comp_tokens   INTEGER NOT NULL DEFAULT 0,
    total_tokens  INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_agent   ON token_usage(agent_id);
CREATE INDEX IF NOT EXISTS idx_usage_session ON token_usage(session_id);
CREATE INDEX IF NOT EXISTS idx_usage_created ON token_usage(created_at);
`

// NewStore opens (or creates) the costs SQLite database at path.
func NewStore(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// future changes go through sqlitex.MigrateSchema with v2+.
	if err := sqlitex.RecordSchemaVersion(db, "costs", 1); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Record appends a usage record.
func (s *Store) Record(ctx context.Context, r UsageRecord) error {
	createdAt := r.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO token_usage
		    (agent_id, session_id, provider, model, prompt_tokens, comp_tokens, total_tokens, cost_usd, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.AgentID, r.SessionID, r.Provider, r.Model,
		r.PromptTokens, r.CompTokens, r.TotalTokens, r.CostUSD,
		createdAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// SumByAgent returns total tokens and estimated cost grouped by agent_id.
// If since is non-zero, only rows after that time are included.
func (s *Store) SumByAgent(ctx context.Context, since time.Time) ([]AgentCost, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if since.IsZero() {
		rows, err = s.db.QueryContext(ctx, `
			SELECT agent_id,
			       SUM(total_tokens)  AS total_tokens,
			       SUM(prompt_tokens) AS prompt_tokens,
			       SUM(comp_tokens)   AS comp_tokens,
			       SUM(cost_usd)      AS cost_usd
			FROM token_usage
			GROUP BY agent_id
			ORDER BY agent_id`)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT agent_id,
			       SUM(total_tokens)  AS total_tokens,
			       SUM(prompt_tokens) AS prompt_tokens,
			       SUM(comp_tokens)   AS comp_tokens,
			       SUM(cost_usd)      AS cost_usd
			FROM token_usage
			WHERE created_at > ?
			GROUP BY agent_id
			ORDER BY agent_id`,
			since.UTC().Format("2006-01-02 15:04:05"))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AgentCost
	for rows.Next() {
		var ac AgentCost
		if err := rows.Scan(&ac.AgentID, &ac.TotalTokens, &ac.PromptTokens, &ac.CompTokens, &ac.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, ac)
	}
	return out, rows.Err()
}

// SumBySession returns total tokens for one agent's sessions.
// If since is non-zero, only rows after that time are included.
func (s *Store) SumBySession(ctx context.Context, agentID string, since time.Time) ([]SessionCost, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if since.IsZero() {
		rows, err = s.db.QueryContext(ctx, `
			SELECT session_id,
			       SUM(total_tokens) AS total_tokens,
			       SUM(cost_usd)     AS cost_usd
			FROM token_usage
			WHERE agent_id = ?
			GROUP BY session_id
			ORDER BY session_id`,
			agentID)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT session_id,
			       SUM(total_tokens) AS total_tokens,
			       SUM(cost_usd)     AS cost_usd
			FROM token_usage
			WHERE agent_id = ?
			  AND created_at > ?
			GROUP BY session_id
			ORDER BY session_id`,
			agentID, since.UTC().Format("2006-01-02 15:04:05"))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionCost
	for rows.Next() {
		var sc SessionCost
		if err := rows.Scan(&sc.SessionID, &sc.TotalTokens, &sc.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// Close closes the DB.
func (s *Store) Close() error {
	return s.db.Close()
}
