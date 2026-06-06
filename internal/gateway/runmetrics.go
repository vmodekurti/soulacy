package gateway

import (
	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/costs"
)

// Run-level observability (Story 7, milestone M1). Combines the costs store
// (tokens/cost/model per session) with the actionlog event trail (tool calls,
// duration, failure) into one compact metrics object — no new storage.

// sessionStatser is satisfied by storage backends whose action log can
// aggregate per-session stats (storagesqlite.ActionLog via the embedded
// actionlog.Logger). Checked at request time so other backends degrade
// gracefully to costs-only metrics.
type sessionStatser interface {
	SessionStats(agentID, sessionID string) (actionlog.SessionStats, error)
}

// handleRunMetrics handles GET /api/v1/runs/:session_id/metrics?agent_id=
func (s *Server) handleRunMetrics(c *fiber.Ctx) error {
	if s.costStore == nil && s.actions == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "run metrics not available (no cost store or action log)",
		})
	}
	sessionID := c.Params("session_id")
	agentID := c.Query("agent_id")

	var (
		cm        costs.SessionMetrics
		haveCosts bool
		st        actionlog.SessionStats
		haveTrail bool
	)
	if s.costStore != nil {
		m, found, err := s.costStore.SessionMetrics(c.Context(), sessionID)
		if err != nil {
			s.log.Warn("run metrics: costs query failed")
		} else if found {
			cm = m
			haveCosts = true
		}
	}
	if s.actions != nil {
		if sp, ok := s.actions.(sessionStatser); ok {
			got, err := sp.SessionStats(agentID, sessionID)
			if err != nil {
				s.log.Warn("run metrics: session stats query failed")
			} else if got.Events > 0 {
				st = got
				haveTrail = true
			}
		}
	}
	if !haveCosts && !haveTrail {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "no metrics recorded for this session",
		})
	}

	out := fiber.Map{
		"session_id":    sessionID,
		"provider":      cm.Provider,
		"model":         cm.Model,
		"llm_calls":     cm.LLMCalls,
		"prompt_tokens": cm.PromptTokens,
		"comp_tokens":   cm.CompTokens,
		"total_tokens":  cm.TotalTokens,
		"cost_usd":      cm.CostUSD,
		"tool_calls":    st.ToolCalls,
		"events":        st.Events,
	}
	if st.LastError != "" {
		out["failure"] = st.LastError
	}

	// Duration: prefer the event trail (covers the whole run including tool
	// time); fall back to the LLM call span when no events were recorded.
	switch {
	case haveTrail:
		out["started_at"] = st.FirstEvent
		out["ended_at"] = st.LastEvent
		out["duration_ms"] = st.LastEvent.Sub(st.FirstEvent).Milliseconds()
	case haveCosts:
		out["started_at"] = cm.FirstCallAt
		out["ended_at"] = cm.LastCallAt
		out["duration_ms"] = cm.LastCallAt.Sub(cm.FirstCallAt).Milliseconds()
	}
	return c.JSON(out)
}
