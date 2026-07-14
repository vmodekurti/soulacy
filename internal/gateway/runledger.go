package gateway

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/pkg/message"
)

type runLedgerRow struct {
	RunID           string    `json:"runId"`
	ID              string    `json:"id"`
	AgentID         string    `json:"agentId,omitempty"`
	AgentName       string    `json:"agentName,omitempty"`
	SessionID       string    `json:"sessionId,omitempty"`
	Trigger         string    `json:"trigger,omitempty"`
	Channel         string    `json:"channel,omitempty"`
	Source          string    `json:"source,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
	Steps           int       `json:"steps,omitempty"`
	Ok              bool      `json:"ok"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
	Output          string    `json:"output,omitempty"`
	DeliveryChannel string    `json:"deliveryChannel,omitempty"`
	DeliveryTo      string    `json:"deliveryTo,omitempty"`
	DeliveryStatus  string    `json:"deliveryStatus,omitempty"`
	DeliveryError   string    `json:"deliveryError,omitempty"`
	EventCount      int       `json:"eventCount,omitempty"`
	DurationMS      int64     `json:"durationMs,omitempty"`
}

// handleRunLedger exposes one durable run ledger for Schedule, Activity, and
// support screens. It groups the action log into executions across all trigger
// paths: manual/http runs, chat/channel messages, cron runs, and delivery-only
// schedule.output records. The goal is that a run appears in one place even
// when the UI that initiated it is not the UI that later inspects it.
func (s *Server) handleRunLedger(c *fiber.Ctx) error {
	if s.actions == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "run ledger not available (action log disabled)",
		})
	}
	q, ok := s.actions.(eventQuerier)
	if !ok {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "run ledger requires a durable action log backend",
		})
	}
	limit := c.QueryInt("limit", 100)
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	eventLimit := c.QueryInt("event_limit", 10000)
	if eventLimit <= 0 {
		eventLimit = 10000
	}
	if eventLimit > 50000 {
		eventLimit = 50000
	}

	agentID := strings.TrimSpace(c.Query("agent_id"))
	events, err := q.QueryEvents(agentID, strings.TrimSpace(c.Query("session_id")), eventLimit, runLedgerEventTypes())
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	rows := s.buildRunLedger(events, limit)
	return c.JSON(fiber.Map{
		"agent_id": agentID,
		"runs":     rows,
		"count":    len(rows),
		"durable":  true,
		"source":   "action-log",
	})
}

func runLedgerEventTypes() map[string]bool {
	return map[string]bool{
		"message.in":       true,
		"message.out":      true,
		"error":            true,
		"tool.call":        true,
		"tool.result":      true,
		"reasoning.step":   true,
		"reasoning.result": true,
		"schedule.output":  true,
	}
}

func (s *Server) buildRunLedger(events []message.Event, limit int) []runLedgerRow {
	if len(events) == 0 {
		return nil
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })

	agentNames := s.runLedgerAgentNames()
	byRun := map[string][]message.Event{}
	runAgent := map[string]string{}
	runSession := map[string]string{}
	currentByAgentSession := map[string]string{}
	startsByAgentSession := map[string]int{}
	order := []string{}

	for i, ev := range events {
		if ev.Type == "" {
			continue
		}
		agentID := strings.TrimSpace(ev.AgentID)
		if agentID == "" {
			agentID = "unknown"
		}
		sessionID := strings.TrimSpace(ev.SessionID)
		if sessionID == "" {
			sessionID = fmt.Sprintf("event-%d", i)
		}
		key := agentID + "\x00" + sessionID
		runID := currentByAgentSession[key]
		if ev.Type == "message.in" || runID == "" {
			if startsByAgentSession[key] == 0 {
				runID = sessionID
			} else {
				runID = durableHistoryRunID(sessionID, ev.Timestamp, i)
			}
			startsByAgentSession[key]++
			currentByAgentSession[key] = runID
			order = append(order, runID)
		}
		if _, ok := byRun[runID]; !ok {
			runAgent[runID] = agentID
			runSession[runID] = sessionID
		}
		byRun[runID] = append(byRun[runID], ev)
	}

	rows := make([]runLedgerRow, 0, len(byRun))
	seen := map[string]bool{}
	for _, runID := range order {
		if seen[runID] {
			continue
		}
		seen[runID] = true
		base, ok := summarizeActionEvents(runID, runSession[runID], byRun[runID])
		if !ok {
			continue
		}
		agentID := runAgent[runID]
		row := runLedgerRow{
			RunID:           base.RunID,
			ID:              base.RunID,
			AgentID:         agentID,
			AgentName:       studioFirstNonEmpty(agentNames[agentID], agentID),
			SessionID:       base.SessionID,
			Trigger:         base.Trigger,
			Channel:         base.Trigger,
			Source:          "action-log",
			StartedAt:       base.StartedAt,
			UpdatedAt:       base.UpdatedAt,
			Steps:           base.Steps,
			Ok:              base.Ok,
			Status:          base.Status,
			Error:           base.Error,
			Output:          base.Output,
			DeliveryChannel: base.DeliveryChannel,
			DeliveryTo:      base.DeliveryTo,
			DeliveryStatus:  base.DeliveryStatus,
			DeliveryError:   base.DeliveryError,
			EventCount:      len(byRun[runID]),
		}
		if row.UpdatedAt.IsZero() {
			row.UpdatedAt = row.StartedAt
		}
		if !row.StartedAt.IsZero() && !row.UpdatedAt.IsZero() {
			row.DurationMS = row.UpdatedAt.Sub(row.StartedAt).Milliseconds()
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func (s *Server) runLedgerAgentNames() map[string]string {
	out := map[string]string{}
	if s == nil || s.loader == nil {
		return out
	}
	for _, def := range s.loader.All() {
		if def != nil {
			out[def.ID] = def.Name
		}
	}
	return out
}
