// studio.go — HTTP handlers for the Studio visual builder (Story S1.1).
//
// As of ARCH-6 the Studio UI is a first-class page of the core dashboard
// (gui/src/pages/Studio.svelte), not a sandboxed plugin. These endpoints are
// registered under /api/v1/studio/* with the standard user RBAC (agent
// read/write) and are NOT in the plugin route allowlist, so scoped plugin
// tokens are rejected with 403 — the dashboard calls them with the user's
// own session.
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
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/studio/consent"
	"github.com/soulacy/soulacy/pkg/agent"
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
	// Studio compile is reasoning-heavy. Honour the optional llm.studio override
	// so operators can run it on a stronger model than the global default. A
	// configured-but-unregistered provider falls back to the default rather than
	// failing every compile.
	provider := strings.TrimSpace(s.cfg.LLM.Studio.Provider)
	if provider == "" {
		provider = s.cfg.LLM.DefaultProvider
	} else if _, ok := s.cfg.LLM.Providers[provider]; !ok {
		provider = s.cfg.LLM.DefaultProvider
	}
	model := strings.TrimSpace(s.cfg.LLM.Studio.Model)
	if model == "" {
		if pc, ok := s.cfg.LLM.Providers[provider]; ok {
			model = pc.Model
		}
	}
	return routerLLM{router: s.llmRouter, provider: provider, model: model}
}

// handleStudioCompile implements POST /api/v1/studio/compile.
func (s *Server) handleStudioCompile(c *fiber.Ctx) error {
	var req studio.Request
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if req.Intent == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "intent is required")
	}

	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}

	// Ground the compiler in the REAL installed skills (authoritative, from the
	// live loader) so it maps loose references ("yahoo finance") to the actual
	// skill name ("yfinance") instead of inventing one. Server-side population
	// means this works regardless of what the GUI sent in the catalog.
	if s.skillLoader != nil {
		req.Catalog.Skills = req.Catalog.Skills[:0]
		for _, sk := range s.skillLoader.All() {
			if sk == nil || strings.TrimSpace(sk.Name) == "" {
				continue
			}
			req.Catalog.Skills = append(req.Catalog.Skills, studio.CatalogSkill{
				Name: sk.Name, Description: sk.Description,
			})
		}
	}

	// Ground the compiler in the live, CONNECTED MCP servers and their tools
	// (authoritative, server-side) so it can wire an MCP tool when the intent
	// names a server or a capability one provides. Grouped by server, order
	// preserved as snapshotMCPTools returns them.
	mcpBySrv := map[string]int{}
	req.Catalog.MCP = nil
	for _, mt := range s.snapshotMCPTools() {
		idx, ok := mcpBySrv[mt.Server]
		if !ok {
			idx = len(req.Catalog.MCP)
			mcpBySrv[mt.Server] = idx
			req.Catalog.MCP = append(req.Catalog.MCP, studio.CatalogMCPServer{Server: mt.Server})
		}
		// Use the FULL callable name (mcp__<server>__<tool>): that is what a tool
		// node's "tool" must be so the engine can resolve it AND so flowTools
		// classifies it as an MCP tool (mcp__ prefix) rather than a builtin.
		// Emitting the bare name is exactly why generated flows referenced
		// non-existent tools like "notebook_create".
		req.Catalog.MCP[idx].Tools = append(req.Catalog.MCP[idx].Tools,
			studio.CatalogMCPTool{Name: mt.FullName, Description: mt.Description})
	}

	res, err := studio.Compile(c.Context(), model, req.Intent, req.Catalog, req.Answers)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
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
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	opts := &studio.TestOptions{
		Mocks:      req.Mocks,
		Assertions: req.Assertions,
		Mode:       req.Mode,
	}
	res, err := studio.TestRun(c.Context(), req.Workflow, req.Input, opts)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
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
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
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
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	res, err := studio.Plan(req.Workflow)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(res)
}

// studioSaveRequest is the POST /api/v1/studio/save body. AcceptPrivilegedExposure
// is the operator's consent to expose a Privileged-tier workflow on a bound
// channel; it is required (true) when studio.Plan reports requiresConsent.
type studioSaveRequest struct {
	Workflow                 studio.Draft `json:"workflow"`
	AcceptPrivilegedExposure bool         `json:"acceptPrivilegedExposure"`
	// Grants carries the per-node code consent collected by the Studio consent
	// dialog (§13). One entry per beyond-guardrail Custom Python node.
	Grants []studioGrant `json:"grants,omitempty"`
}

// studioGrant is one per-node code-consent grant from the save request.
type studioGrant struct {
	NodeID       string   `json:"nodeId"`
	Hash         string   `json:"hash"`
	Capabilities []string `json:"capabilities"`
	Scope        string   `json:"scope"`
}

// handleStudioSave implements POST /api/v1/studio/save. It converts the
// draft into a DISABLED agent.Definition and persists it via the same
// loader.Upsert path the create-agent handler uses, then returns the new
// agent id. The agent is saved with Enabled=false so the operator reviews
// and enables it explicitly.
func (s *Server) handleStudioSave(c *fiber.Ctx) error {
	var req studioSaveRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	// Consent gate: classify the draft and refuse to persist a Privileged
	// channel exposure unless the operator accepted it. The decision logic
	// lives in studio.Plan so it is identical to what /studio/plan reported.
	plan, err := studio.Plan(req.Workflow)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
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
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if isProtectedSystemAgent(def.ID) {
		return protectedSystemAgentResponse(c)
	}

	// Default LLM to the configured provider, mirroring handleCreateAgent.
	if def.LLM.Provider == "" {
		def.LLM.Provider = s.cfg.LLM.DefaultProvider
	}
	// Stamp per-node code consent (§13) onto the workflow nodes. ApplyGrants
	// refuses if any beyond-guardrail Custom Python node lacks a matching grant,
	// so a saved (and later enabled) agent can never carry unconsented host or
	// network code — the runtime fail-closed check in internal/runtime/flow.go
	// then honours these stamps. GrantedBy records who approved.
	if def.Workflow != nil {
		grantedBy := ""
		if cl := auth.ClaimsFromCtx(c); cl != nil {
			grantedBy = cl.Subject
		}
		grants := make([]consent.Grant, 0, len(req.Grants))
		for _, g := range req.Grants {
			grants = append(grants, consent.Grant{
				NodeID:       g.NodeID,
				Hash:         g.Hash,
				Capabilities: g.Capabilities,
				Scope:        g.Scope,
				GrantedBy:    grantedBy,
			})
		}
		if gerr := consent.ApplyGrants(def.Workflow.Nodes, grants); gerr != nil {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error":           gerr.Error(),
				"requiresConsent": true,
				"consentItems":    plan.ConsentItems,
			})
		}
	}

	// Persist as a DISABLED agent — Studio saves are staged, not live.
	def.Enabled = false

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if err := s.loader.Upsert(dir, &def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	// Auto-stub any missing peer agents referenced in the workflow.
	// We use the Draft's NewAgents array to populate full profiles (SystemPrompt, Description).
	// We save these auto-generated agents as Enabled: true with safe defaults so they do not
	// fail the main workflow execution when it delegates to them.
	if def.Workflow != nil {
		newAgentsMap := make(map[string]studio.NewAgent)
		for _, na := range req.Workflow.NewAgents {
			newAgentsMap[na.ID] = na
		}
		for _, node := range def.Workflow.Nodes {
			if node.Kind == "agent" && node.Agent != "" {
				if existing := s.loader.Get(node.Agent); existing == nil {
					stub := agent.Definition{
						ID:          node.Agent,
						Name:        node.Agent,
						Description: "Auto-generated from workflow",
						Enabled:     true,
						MaxTurns:    15,
						Memory:      agent.MemoryPolicy{MaxTokens: 8000},
						LLM: agent.LLMConfig{
							Provider:    s.cfg.LLM.DefaultProvider,
							Temperature: 0.7,
						},
					}
					if na, ok := newAgentsMap[node.Agent]; ok {
						if na.Name != "" {
							stub.Name = na.Name
						}
						if na.Description != "" {
							stub.Description = na.Description
						}
						stub.SystemPrompt = na.SystemPrompt
					}
					// Ignore errors here; best-effort stubbing.
					_ = s.loader.Upsert(dir, &stub)
				}
			}
		}
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"agentId": def.ID,
		"enabled": false,
	})
}

// handleStudioListAgents implements GET /api/v1/studio/agents. It returns the
// agents that carry a workflow graph (Studio-editable) as lightweight summaries
// for the "My Workflows" list. Agents without a workflow are skipped.
func (s *Server) handleStudioListAgents(c *fiber.Ctx) error {
	type agentSummary struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Enabled     bool   `json:"enabled"`
		Trigger     string `json:"trigger"`
		Nodes       int    `json:"nodes"`
	}
	out := []agentSummary{}
	for _, d := range s.loader.All() {
		if d == nil || !studio.HasWorkflow(*d) {
			continue
		}
		out = append(out, agentSummary{
			ID:          d.ID,
			Name:        d.Name,
			Description: d.Description,
			Enabled:     d.Enabled,
			Trigger:     string(d.Trigger),
			Nodes:       len(d.Workflow.Nodes),
		})
	}
	return c.JSON(fiber.Map{"agents": out})
}

// handleStudioLoadAgent implements GET /api/v1/studio/agents/:id. It returns the
// agent's workflow as a Studio Draft so it can be re-opened on the canvas for
// editing; re-saving (POST /studio/save) upserts the same id.
// handleStudioScaffolds implements GET /api/v1/studio/scaffolds. It returns the
// built-in framework Python scaffolds (deterministic, shipped code — no LLM) the
// Custom Python editor offers as "Insert scaffold".
func (s *Server) handleStudioScaffolds(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"scaffolds": studio.Scaffolds()})
}

// handleStudioCodegen implements POST /api/v1/studio/codegen. It asks the
// framework's configured model (llm.studio → studioLLM) to write a complete
// Custom Python node body for ONE node from its description + workflow context.
// In-framework only — the same llm.Router the rest of the gateway uses.
func (s *Server) handleStudioCodegen(c *fiber.Ctx) error {
	var req studio.CodegenRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	llm := s.studioLLM()
	if llm == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "no LLM provider configured for code generation")
	}
	code, err := studio.GenerateNodeCode(c.Context(), llm, req)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadGateway, err)
	}
	return c.JSON(fiber.Map{"code": code})
}

func (s *Server) handleStudioLoadAgent(c *fiber.Ctx) error {
	def := s.loader.Get(c.Params("id"))
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	if !studio.HasWorkflow(*def) {
		return s.errMsg(c, fiber.StatusBadRequest, "agent has no editable workflow")
	}
	return c.JSON(fiber.Map{"workflow": studio.FromAgentDefinition(*def)})
}

// --- Studio templates (Story S6.1) ---

// handleStudioTemplates implements GET /api/v1/studio/templates. It returns the
// built-in Studio starter Drafts the canvas offers as one-click starting
// points. Read-only: nothing is persisted and no LLM is involved. Every
// template.workflow is guaranteed (by studio.Templates + its tests) to pass
// reasoning.CompileFlow, so a user who picks one lands on a valid graph.
func (s *Server) handleStudioTemplates(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"templates": studio.Templates()})
}

// --- Studio draft library (Story S6.2) ---

// studioDraftsDir derives the Studio drafts directory from the resolved
// workspace: <workspace>/studio/drafts. The store (internal/studio) creates the
// directory on first save, so this only needs to return the path. It is kept on
// Server so every draft handler reaches the same location.
func (s *Server) studioDraftsDir() (string, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return "", err
	}
	return filepath.Join(ws.Root, "studio", "drafts"), nil
}

// studioSaveDraftRequest is the POST /api/v1/studio/drafts body.
type studioSaveDraftRequest struct {
	Name     string       `json:"name"`
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioSaveDraft implements POST /api/v1/studio/drafts. It persists the
// draft as a JSON file under the Studio drafts dir and returns the new id. The
// store derives a slug+short-hash id and overwrites on an identical re-save.
func (s *Server) handleStudioSaveDraft(c *fiber.Ctx) error {
	var req studioSaveDraftRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if req.Name == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "name is required")
	}
	dir, err := s.studioDraftsDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	id, err := studio.SaveDraft(dir, req.Name, req.Workflow)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": id})
}

// handleStudioListDrafts implements GET /api/v1/studio/drafts. It returns the
// metadata (id, name, updated) of every saved draft, most recent first.
func (s *Server) handleStudioListDrafts(c *fiber.Ctx) error {
	dir, err := s.studioDraftsDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	drafts, err := studio.ListDrafts(dir)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"drafts": drafts})
}

// handleStudioLoadDraft implements GET /api/v1/studio/drafts/:id. It returns
// the full stored draft (id, name, workflow). The :id is validated against
// path traversal inside studio.LoadDraft.
func (s *Server) handleStudioLoadDraft(c *fiber.Ctx) error {
	dir, err := s.studioDraftsDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	sd, err := studio.LoadDraft(dir, c.Params("id"))
	if err != nil {
		return s.errJSON(c, fiber.StatusNotFound, err)
	}
	return c.JSON(fiber.Map{"id": sd.ID, "name": sd.Name, "workflow": sd.Workflow})
}

// handleStudioDeleteDraft implements DELETE /api/v1/studio/drafts/:id. The :id
// is validated against path traversal inside studio.DeleteDraft.
func (s *Server) handleStudioDeleteDraft(c *fiber.Ctx) error {
	dir, err := s.studioDraftsDir()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if err := studio.DeleteDraft(dir, c.Params("id")); err != nil {
		return s.errJSON(c, fiber.StatusNotFound, err)
	}
	return c.JSON(fiber.Map{"ok": true})
}

// --- Studio per-node re-describe (Story S6.3) ---

// studioRefineRequest is the POST /api/v1/studio/refine body: the current
// workflow, the target node id, and a plain-language instruction.
type studioRefineRequest struct {
	Workflow    studio.Draft `json:"workflow"`
	NodeID      string       `json:"nodeId"`
	Instruction string       `json:"instruction"`
}

// handleStudioRefine implements POST /api/v1/studio/refine. It applies a
// plain-language change to one node via studio.Refine (reusing the gateway's
// LLM router) and returns the full updated workflow. studio.Refine validates
// the result via reasoning.CompileFlow and never returns a broken draft, so an
// invalid model output surfaces as a clear error rather than a bad workflow.
func (s *Server) handleStudioRefine(c *fiber.Ctx) error {
	var req studioRefineRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if req.NodeID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "nodeId is required")
	}
	if req.Instruction == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "instruction is required")
	}

	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}

	updated, err := studio.Refine(c.Context(), model, req.Workflow, req.NodeID, req.Instruction)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(fiber.Map{"workflow": updated})
}
