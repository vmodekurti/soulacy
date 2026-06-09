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
	"encoding/json"

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

// studioTestRequest is the POST /api/v1/studio/test body. Mocks, Assertions,
// and Mode are optional (M5, Stories S5.2/S5.3): existing {workflow,input}
// callers keep working unchanged.
//
//	{ workflow, input,
//	  mocks?:      {<nodeId>: <output>},
//	  assertions?: [{target, op, value}],
//	  mode?:       "dry" }
type studioTestRequest struct {
	Workflow   studio.Draft               `json:"workflow"`
	Input      string                     `json:"input"`
	Mocks      map[string]json.RawMessage `json:"mocks,omitempty"`
	Assertions []studio.Assertion         `json:"assertions,omitempty"`
	Mode       string                     `json:"mode,omitempty"`
}

// handleStudioTest implements POST /api/v1/studio/test. It dry-runs the
// draft workflow through studio.TestRun (a mock node runner — no real
// tools/agents/LLM) and returns the per-node trace, the final result, the
// evaluated assertions, an aggregate passed flag, the echoed mode, and any
// warnings. Per-node mock overrides and assertions are optional.
func (s *Server) handleStudioTest(c *fiber.Ctx) error {
	var req studioTestRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body: " + err.Error()})
	}

	opts := &studio.TestOptions{
		Mocks:      req.Mocks,
		Assertions: req.Assertions,
		Mode:       req.Mode,
	}
	res, err := studio.TestRun(c.Context(), req.Workflow, req.Input, opts)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(res)
}

// studioValidateRequest is the POST /api/v1/studio/validate body. The canvas
// posts the in-progress draft and gets back structured errors + warnings.
type studioValidateRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioValidate implements POST /api/v1/studio/validate. It validates
// the draft's flow graph (Story M3) and returns structured errors + soft
// warnings the canvas surfaces while the user edits. The handler is thin —
// all logic lives in studio.Validate, which NEVER fails on a bad graph (a bad
// graph is reported as data), so this endpoint never 500s on workflow content.
func (s *Server) handleStudioValidate(c *fiber.Ctx) error {
	var req studioValidateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body: " + err.Error()})
	}
	return c.JSON(studio.Validate(req.Workflow))
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
