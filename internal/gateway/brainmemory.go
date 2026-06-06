// brainmemory.go — REST API for the three-layer agent brain memory system.
//
// Endpoints (all under /api/v1/brain-memory):
//
//	GET    /brain-memory                          list all agents with memory stats
//	GET    /brain-memory/:agentID/stats           stats for one agent
//	GET    /brain-memory/:agentID/episodic        list episodic records (newest first)
//	DELETE /brain-memory/:agentID/episodic        clear all episodic records for agent
//	POST   /brain-memory/:agentID/episodic        write a manual episodic record
//	GET    /brain-memory/:agentID/procedural      get procedural rules markdown
//	PUT    /brain-memory/:agentID/procedural      overwrite procedural rules
//	DELETE /brain-memory/:agentID/procedural      clear procedural rules
//	POST   /brain-memory/:agentID/context-preview preview context block for a task query
package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/agentmemory"
)

// handleBrainMemoryStats returns per-agent brain memory statistics for all
// loaded agents. Used by the GUI's Brain Memory overview page.
func (s *Server) handleBrainMemoryStats(c *fiber.Ctx) error {
	store := s.engine.BrainStore()
	if store == nil {
		return c.JSON(fiber.Map{"enabled": false, "agents": []any{}})
	}

	agents := s.loader.All()
	var out []fiber.Map
	for _, def := range agents {
		records, _ := store.EpisodicRecords(def.ID, 0) // 0 = all
		procedural := store.ProceduralRules(def.ID)
		var lastActivity *time.Time
		if len(records) > 0 {
			t := records[0].Timestamp
			lastActivity = &t
		}
		out = append(out, fiber.Map{
			"agent_id":        def.ID,
			"agent_name":      def.Name,
			"episodic_count":  len(records),
			"has_procedural":  procedural != "",
			"last_activity":   lastActivity,
		})
	}
	return c.JSON(fiber.Map{"enabled": true, "agents": out})
}

// handleGetEpisodic returns episodic records for one agent (newest first).
// Query params: limit (default 100)
func (s *Server) handleGetEpisodic(c *fiber.Ctx) error {
	store := s.engine.BrainStore()
	if store == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "brain memory not configured (set SOULACY_MEMORY_DIR)",
		})
	}
	agentID := c.Params("agentID")
	limit := c.QueryInt("limit", 100)

	records, err := store.EpisodicRecords(agentID, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if records == nil {
		records = []agentmemory.Record{}
	}
	return c.JSON(fiber.Map{"records": records, "count": len(records)})
}

// handleClearEpisodic deletes all episodic records for an agent.
func (s *Server) handleClearEpisodic(c *fiber.Ctx) error {
	store := s.engine.BrainStore()
	if store == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "brain memory not configured",
		})
	}
	agentID := c.Params("agentID")
	if err := store.ClearEpisodic(agentID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusNoContent).Send(nil)
}

// handleWriteEpisodic writes a manual episodic record.
func (s *Server) handleWriteEpisodic(c *fiber.Ctx) error {
	store := s.engine.BrainStore()
	if store == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "brain memory not configured",
		})
	}
	agentID := c.Params("agentID")

	var body struct {
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if body.Content == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "content is required"})
	}

	rec := agentmemory.Record{
		AgentID:   agentID,
		Type:      agentmemory.MemoryTypeEpisodic,
		Content:   body.Content,
		Tags:      body.Tags,
		Timestamp: time.Now().UTC(),
	}
	if err := store.Write(rec); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true})
}

// handleGetProcedural returns the procedural rules markdown for an agent.
func (s *Server) handleGetProcedural(c *fiber.Ctx) error {
	store := s.engine.BrainStore()
	if store == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "brain memory not configured",
		})
	}
	agentID := c.Params("agentID")
	rules := store.ProceduralRules(agentID)
	return c.JSON(fiber.Map{"agent_id": agentID, "rules": rules})
}

// handleUpdateProcedural overwrites the procedural rules for an agent.
func (s *Server) handleUpdateProcedural(c *fiber.Ctx) error {
	store := s.engine.BrainStore()
	if store == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "brain memory not configured",
		})
	}
	agentID := c.Params("agentID")

	var body struct {
		Rules string `json:"rules"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if err := store.UpdateProcedural(agentID, body.Rules); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// handleClearProcedural deletes the procedural rules file for an agent.
func (s *Server) handleClearProcedural(c *fiber.Ctx) error {
	store := s.engine.BrainStore()
	if store == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "brain memory not configured",
		})
	}
	agentID := c.Params("agentID")
	if err := store.UpdateProcedural(agentID, ""); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusNoContent).Send(nil)
}

// handleContextPreview returns what BuildContextBlock would inject for a given
// task query. Used by the GUI's "Context Preview" tab to show operators exactly
// what the agent will see before executing a task.
func (s *Server) handleContextPreview(c *fiber.Ctx) error {
	store := s.engine.BrainStore()
	if store == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "brain memory not configured",
		})
	}
	agentID := c.Params("agentID")

	var body struct {
		TaskInput   string `json:"task_input"`
		MaxEpisodic int    `json:"max_episodic"`
		MaxSemantic int    `json:"max_semantic"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if body.MaxEpisodic <= 0 {
		body.MaxEpisodic = 5
	}
	if body.MaxSemantic <= 0 {
		body.MaxSemantic = 8
	}

	result, err := store.Retrieve(agentmemory.RetrieveQuery{
		AgentID:     agentID,
		TaskInput:   body.TaskInput,
		MaxEpisodic: body.MaxEpisodic,
		MaxSemantic: body.MaxSemantic,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	block := agentmemory.BuildContextBlock(result)
	return c.JSON(fiber.Map{
		"context_block":    block,
		"episodic_count":   len(result.EpisodicSummary),
		"semantic_count":   len(result.SemanticChunks),
		"has_procedural":   result.ProceduralRules != "",
		"token_estimate":   len(block) / 4,
	})
}

// ─── Public accessors on CompositeStore needed by handlers ───────────────────
// These are added here to keep agentmemory package self-contained.
// We add them as methods on CompositeStore via the brainmemory_ext.go companion.
// For now they are implemented inline using the existing store API.

// brainMemoryDir returns the base directory for agent memory files.
// Used to build file path displays in the GUI stats.
func brainMemoryDir() string {
	dir := os.Getenv("SOULACY_MEMORY_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".soulacy", "memory")
	}
	return dir
}

// formatMemoryPath returns the on-disk path display for an agent's episodic store.
func formatMemoryPath(agentID string) string {
	return fmt.Sprintf("%s/%s/episodic.jsonl", brainMemoryDir(), agentID)
}
