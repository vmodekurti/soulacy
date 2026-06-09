// studio.go — HTTP handler for the Studio plugin backend (Story S1.1).
//
// Route (under /api/v1, user-authenticated, same RBAC as agent writes):
//
//	POST /api/v1/studio/compile — turn a plain-language intent into a draft
//	                              workflow plus clarifying questions.
//
// The handler is thin: it parses the body, adapts the gateway's llm.Router
// to the narrow studio.LLM interface (reaching the model exactly like the
// rest of the gateway does — through s.llmRouter, with provider/model
// resolved from config.LLM), calls studio.Compile, and returns the Result
// as JSON.
package gateway

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/studio"
)

// routerLLM adapts the gateway's *llm.Router to studio.LLM. It routes to
// the configured default provider and resolves that provider's model from
// config, mirroring how the gateway otherwise reaches the LLM layer.
type routerLLM struct {
	router   *llm.Router
	provider string
	model    string
}

func (a routerLLM) Complete(ctx context.Context, prompt string) (string, error) {
	resp, err := a.router.Complete(ctx, a.provider, llm.CompletionRequest{
		Model: a.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		ResponseFormat: "json",
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// studioLLM builds the studio.LLM the compiler will use, wiring the default
// provider + model out of config. Returns nil when no router is available.
func (s *Server) studioLLM() studio.LLM {
	if s.llmRouter == nil {
		return nil
	}
	provider := s.cfg.LLM.DefaultProvider
	model := ""
	if pc, ok := s.cfg.LLM.Providers[provider]; ok {
		model = pc.Model
	}
	return routerLLM{router: s.llmRouter, provider: provider, model: model}
}

// handleStudioCompile implements POST /api/v1/studio/compile.
func (s *Server) handleStudioCompile(c *fiber.Ctx) error {
	var req studio.Request
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body: " + err.Error()})
	}
	if req.Intent == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "intent is required"})
	}

	model := s.studioLLM()
	if model == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "LLM router unavailable"})
	}

	res, err := studio.Compile(c.Context(), model, req.Intent, req.Catalog, req.Answers)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(res)
}

// studioTestRequest is the POST /api/v1/studio/test body.
type studioTestRequest struct {
	Workflow studio.Draft `json:"workflow"`
	Input    string       `json:"input"`
}

// handleStudioTest implements POST /api/v1/studio/test. It dry-runs the
// draft workflow through studio.TestRun (a mock node runner — no real
// tools/agents/LLM) and returns the per-node trace plus the final result.
func (s *Server) handleStudioTest(c *fiber.Ctx) error {
	var req studioTestRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body: " + err.Error()})
	}

	res, err := studio.TestRun(c.Context(), req.Workflow, req.Input)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(res)
}

// studioPlanRequest is the POST /api/v1/studio/plan body.
type studioPlanRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioPlan implements POST /api/v1/studio/plan. It classifies the
// agent the draft would become (capability tier) and reports whether saving
// would create a Privileged channel exposure that needs the operator's
// consent. Pure decision: nothing is persisted. The handler is thin — all
// logic lives in studio.Plan so it stays unit-testable.
func (s *Server) handleStudioPlan(c *fiber.Ctx) error {
	var req studioPlanRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body: " + err.Error()})
	}

	res, err := studio.Plan(req.Workflow)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(res)
}

// studioSaveRequest is the POST /api/v1/studio/save body. AcceptPrivilegedExposure
// is the operator's consent to expose a Privileged-tier workflow on a bound
// channel; it is required (true) when studio.Plan reports requiresConsent.
type studioSaveRequest struct {
	Workflow                 studio.Draft `json:"workflow"`
	AcceptPrivilegedExposure bool         `json:"acceptPrivilegedExposure"`
}

// handleStudioSave implements POST /api/v1/studio/save. It converts the
// draft into a DISABLED agent.Definition and persists it via the same
// loader.Upsert path the create-agent handler uses, then returns the new
// agent id. The agent is saved with Enabled=false so the operator reviews
// and enables it explicitly.
func (s *Server) handleStudioSave(c *fiber.Ctx) error {
	var req studioSaveRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body: " + err.Error()})
	}

	// Consent gate: classify the draft and refuse to persist a Privileged
	// channel exposure unless the operator accepted it. The decision logic
	// lives in studio.Plan so it is identical to what /studio/plan reported.
	plan, err := studio.Plan(req.Workflow)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if plan.RequiresConsent && !req.AcceptPrivilegedExposure {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":           "saving this workflow exposes a privileged-tier agent to a channel; explicit consent required",
			"requiresConsent": true,
			"consentItems":    plan.ConsentItems,
		})
	}

	def, err := studio.ToAgentDefinition(req.Workflow, req.AcceptPrivilegedExposure)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if isProtectedSystemAgent(def.ID) {
		return protectedSystemAgentResponse(c)
	}

	// Default LLM to the configured provider, mirroring handleCreateAgent.
	if def.LLM.Provider == "" {
		def.LLM.Provider = s.cfg.LLM.DefaultProvider
	}
	// Persist as a DISABLED agent — Studio saves are staged, not live.
	def.Enabled = false

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if err := s.loader.Upsert(dir, &def); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"agentId": def.ID,
		"enabled": false,
	})
}
