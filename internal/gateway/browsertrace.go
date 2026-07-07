package gateway

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/browsertrace"
)

// handleBrowserTrace returns the reconstructed browser-automation trace for a
// session: an ordered list of navigate/click/type/extract/screenshot steps plus
// the last URL and screenshot reference. Read-only aggregation over the action
// log; the domain-approval policy that governs navigation lives in the tool
// policy engine.
func (s *Server) handleBrowserTrace(c *fiber.Ctx) error {
	if s.actions == nil {
		return c.JSON(fiber.Map{"enabled": false, "trace": browsertrace.Trace{Steps: []browsertrace.Step{}}})
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if agentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id is required")
	}
	limit, _ := strconv.Atoi(c.Query("limit", "2000"))
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	events, err := s.actions.Tail(agentID, limit)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	trace := browsertrace.Build(agentID, sessionID, events)
	return c.JSON(fiber.Map{"enabled": true, "trace": trace})
}
