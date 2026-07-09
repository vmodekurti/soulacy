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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/pkg/message"

	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
	reasoningpkg "github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/secrets"
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

// CompleteSchema constrains the builder model's output to the supplied JSON
// Schema (studio.SchemaLLM). The schema is a strong GUIDE, not a strict
// contract — it intentionally allows freeform sub-objects — so JSONSchemaLenient
// keeps OpenAI in non-strict mode instead of rejecting it. Providers without
// schema support (Ollama) transparently fall back to JSON mode + post-validation.
func (a routerLLM) CompleteSchema(ctx context.Context, prompt string, schema map[string]any) (string, error) {
	resp, err := a.router.Complete(ctx, a.provider, llm.CompletionRequest{
		Model: a.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		ResponseFormat:    "json_schema",
		JSONSchema:        schema,
		JSONSchemaLenient: true,
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
	provider, model := s.studioProviderModel()
	return routerLLM{router: s.llmRouter, provider: provider, model: model}
}

// studioProviderModel resolves the builder (llm.studio) provider + model the
// same way studioLLM wires it: honour the llm.studio override, fall back to the
// default provider when it's unset or unregistered, and fill the model from the
// provider config when unset. Shared by studioLLM and the model-advice endpoint.
func (s *Server) studioProviderModel() (provider, model string) {
	provider = strings.TrimSpace(s.cfg.LLM.Studio.Provider)
	if provider == "" {
		provider = s.cfg.LLM.DefaultProvider
	} else if _, ok := s.cfg.LLM.Providers[provider]; !ok {
		provider = s.cfg.LLM.DefaultProvider
	}
	model = strings.TrimSpace(s.cfg.LLM.Studio.Model)
	if model == "" {
		if pc, ok := s.cfg.LLM.Providers[provider]; ok {
			model = pc.Model
		}
	}
	return provider, model
}

// defaultAgentLLM resolves the RUNTIME default provider + model that a generated
// agent should run on. This is distinct from studioProviderModel (the builder
// model used to GENERATE the agent): the agent runs on the gateway's default
// provider, not necessarily the (possibly cloud) builder model.
func (s *Server) defaultAgentLLM() (provider, model string) {
	provider = strings.TrimSpace(s.cfg.LLM.DefaultProvider)
	if pc, ok := s.cfg.LLM.Providers[provider]; ok {
		model = strings.TrimSpace(pc.Model)
	}
	return provider, model
}

// stampDefaultLLM fills a generated draft's empty llm.provider/model with the
// runtime default so the choice is explicit and visible in the saved SOUL.yaml
// (rather than blank fields that silently inherit the default at run time).
func (s *Server) stampDefaultLLM(d *studio.Draft) {
	if d == nil {
		return
	}
	p, m := s.defaultAgentLLM()
	if strings.TrimSpace(d.LLM.Provider) == "" {
		d.LLM.Provider = p
	}
	if strings.TrimSpace(d.LLM.Model) == "" {
		d.LLM.Model = m
	}
}

// handleStudioModelAdvice implements GET /api/v1/studio/model-advice. Local-first
// pivot: it reports the builder model, whether it runs locally, a supportive
// (non-shaming) complexity note for small local models, whether using it would
// send the prompt off-box (cloud-escalation), and whether a stronger frontier
// model is configured and can be OFFERED as optional assistance (hybrid use).
func (s *Server) handleStudioModelAdvice(c *fiber.Ctx) error {
	provider, model := s.studioProviderModel()
	registered := false
	if s.llmRouter != nil {
		for _, id := range s.llmRouter.ProviderIDs() {
			if id == provider {
				registered = true
				break
			}
		}
	}
	if !registered {
		// Provider not actually usable → advise as unconfigured (block).
		return c.JSON(studio.AssessModel("", "", ""))
	}
	baseURL := ""
	if pc, ok := s.cfg.LLM.Providers[provider]; ok {
		baseURL = pc.BaseURL
	}
	adv := studio.AssessModel(provider, model, baseURL)

	// Hybrid (the user's question): if the builder is local, surface whether a
	// stronger CLOUD provider is also configured + registered, so the UI can
	// offer it as optional assistance for complex builds — opt-in, never forced.
	if adv.Local {
		if fp := s.firstConfiguredCloudProvider(); fp != "" {
			adv.FrontierAvailable = true
			adv.FrontierProvider = fp
		}
	}
	return c.JSON(adv)
}

// firstConfiguredCloudProvider returns the name of a registered, configured
// cloud LLM provider (if any), so Studio can offer it as optional frontier
// assistance alongside a local builder. Deterministic order: registered IDs.
func (s *Server) firstConfiguredCloudProvider() string {
	if s.llmRouter == nil {
		return ""
	}
	for _, id := range s.llmRouter.ProviderIDs() {
		pc, ok := s.cfg.LLM.Providers[id]
		if !ok {
			continue
		}
		if !studio.IsLocalProvider(id, pc.BaseURL) {
			return id
		}
	}
	return ""
}

// groundCatalog overwrites the caller-supplied catalog's Skills and MCP fields
// with the REAL, live, authoritative server-side inventory (installed skills +
// connected MCP servers and their tools). Both the compile and the pre-compile
// refine pass call this so they see the same world the engine will, and so loose
// references map to actual capabilities instead of being invented. It mutates
// the passed catalog in place.
func (s *Server) groundCatalog(cat *studio.Catalog) {
	// Inject the authoring rulebook so the builder follows the same rules the
	// validator and AI fixer enforce.
	cat.Rules = s.soulRules()
	// Inject lessons learned from accepted live-run repairs so generation avoids
	// repeating real shape mistakes (gated by llm.studio.learning).
	s.groundLessons(cat)
	// Installed skills (so "yahoo finance" maps to the real "yfinance").
	if s.skillLoader != nil {
		cat.Skills = cat.Skills[:0]
		for _, sk := range s.skillLoader.All() {
			if sk == nil || strings.TrimSpace(sk.Name) == "" {
				continue
			}
			cat.Skills = append(cat.Skills, studio.CatalogSkill{
				Name: sk.Name, Description: sk.Description,
			})
		}
	}

	// Connected MCP servers and their tools, grouped by server, order preserved
	// as snapshotMCPTools returns them. Use the FULL callable name
	// (mcp__<server>__<tool>) so a tool node's "tool" resolves and classifies as
	// MCP rather than a builtin.
	mcpBySrv := map[string]int{}
	cat.MCP = nil
	for _, mt := range s.snapshotMCPTools() {
		idx, ok := mcpBySrv[mt.Server]
		if !ok {
			idx = len(cat.MCP)
			mcpBySrv[mt.Server] = idx
			cat.MCP = append(cat.MCP, studio.CatalogMCPServer{Server: mt.Server})
		}
		cat.MCP[idx].Tools = append(cat.MCP[idx].Tools,
			studio.CatalogMCPTool{Name: mt.FullName, Description: mt.Description, Params: mt.Params})
	}

	// Configured output channels (Story #1): a channel is groundable when it is
	// always-on (http) or has been configured + enabled. Studio wires delivery
	// to one of these instead of inventing a channel name.
	cat.Channels = s.groundedChannels()

	// Knowledge bases (Story #7): expose the KBs the agent could draw on so the
	// compiler can attach a relevant one. Best-effort: empty when the knowledge
	// store is disabled.
	cat.KnowledgeBases = nil
	if s.engine != nil {
		if ksvc := s.engine.Knowledge(); ksvc != nil && ksvc.Store != nil {
			if kbs, err := ksvc.Store.ListKBs(); err == nil {
				for _, kb := range kbs {
					cat.KnowledgeBases = append(cat.KnowledgeBases, studio.CatalogKB{
						Name: kb.Name, Description: kb.Description,
					})
				}
			}
		}
	}
}

// groundedChannels returns the output channels available to a workflow: the
// always-on ones plus any configured-and-enabled channel. Mirrors the
// enabled/configured logic in handleListChannels but distilled to the names
// Studio can wire delivery to.
func (s *Server) groundedChannels() []string {
	statuses := s.channels.Statuses()
	var out []string
	for _, spec := range channelSpecs {
		cfg := s.cfg.Channels[spec.ID]
		enabled := spec.Always
		if v, ok := cfg["enabled"].(bool); ok {
			enabled = v
		}
		if !enabled {
			continue
		}
		// Require some configuration for non-always channels (a token/bot), or
		// a registered live adapter, so we don't advertise an unusable channel.
		if !spec.Always {
			configured := false
			for _, f := range spec.Fields {
				if valuePresent(cfg[f.Key]) {
					configured = true
					break
				}
			}
			if _, registered := statuses[spec.ID]; !configured && !registered {
				continue
			}
		}
		out = append(out, spec.ID)
	}
	return out
}

// studioRefinePromptRequest is the POST /api/v1/studio/refine-prompt body.
type studioRefinePromptRequest struct {
	Intent  string         `json:"intent"`
	Catalog studio.Catalog `json:"catalog,omitempty"`
	// Light requests a touch-up pass instead of a full rewrite. The UI sets it
	// when re-generating from an already-refined, user-edited prompt so the LLM
	// only cleans up the edits rather than re-refining the whole specification.
	Light bool `json:"light,omitempty"`
}

// handleStudioRefinePrompt implements POST /api/v1/studio/refine-prompt. It is
// the mandatory pre-generation step: it turns the user's rough intent into a
// clear, complete specification plus the assumptions it made and any clarifying
// questions, which the UI shows for confirmation BEFORE a workflow is compiled.
func (s *Server) handleStudioRefinePrompt(c *fiber.Ctx) error {
	var req studioRefinePromptRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.Intent) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "intent is required")
	}
	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	s.groundCatalog(&req.Catalog)

	refine := studio.RefinePrompt
	if req.Light {
		refine = studio.LightRefinePrompt
	}
	res, err := refine(c.Context(), model, req.Intent, req.Catalog)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(res)
}

// studioPreflightRequest is the POST /api/v1/studio/preflight body.
type studioPreflightRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioPreflight implements POST /api/v1/studio/preflight. It runs the
// consolidated pre-save validation (Stories #11/#12): missing tools/agents,
// disconnected MCP servers, empty required tool arguments, invalid schedules,
// and unconfigured channels — assembled against authoritative server-side state
// and returned as a single blockers/warnings report.
func (s *Server) handleStudioPreflight(c *fiber.Ctx) error {
	var req studioPreflightRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	// Ground a fresh catalog so tool/MCP/channel references are checked against
	// the real, live inventory rather than whatever the GUI happened to send.
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)

	res := studio.Preflight(req.Workflow, s.preflightInput(c, cat))
	return c.JSON(res)
}

// preflightInput assembles the live-state PreflightInput from a grounded
// catalog: connected MCP servers, configured channels, and stored secrets.
// Shared by the preflight endpoint and the compile handler (which surfaces the
// result as the explanation's NeedsConfig).
func (s *Server) preflightInput(c *fiber.Ctx, cat studio.Catalog) studio.PreflightInput {
	in := studio.PreflightInput{
		Catalog:            cat,
		ConnectedMCP:       s.connectedMCPSet(),
		ChannelsConfigured: s.configuredChannelSet(),
	}
	if mgr := secrets.New(s.CredentialVault()); mgr.Enabled() {
		set := map[string]bool{}
		for _, d := range mgr.Catalog(c.Context(), s.cfg) {
			set[d.Name] = d.Set
		}
		in.SecretsSet = set
	}
	return in
}

// studioCatalogSnapshot builds the agents/tools/providers portion of the
// catalog from authoritative live state (the agent loader, the unified tool
// catalog, and the LLM router). groundCatalog then fills Skills/MCP/Channels/
// KBs. Used by preflight (no GUI-supplied catalog) and reusable elsewhere.
func (s *Server) studioCatalogSnapshot() studio.Catalog {
	var cat studio.Catalog
	if s.loader != nil {
		for _, d := range s.loader.All() {
			if d == nil || strings.TrimSpace(d.ID) == "" {
				continue
			}
			cat.Agents = append(cat.Agents, d.ID)
		}
	}
	tc := s.toolCatalog()
	for _, b := range tc.Builtins {
		if strings.TrimSpace(b.Name) != "" {
			cat.Tools = append(cat.Tools, b.Name)
		}
	}
	for _, p := range tc.PythonTools {
		if strings.TrimSpace(p.Name) != "" {
			cat.Tools = append(cat.Tools, p.Name)
		}
	}
	if s.llmRouter != nil {
		cat.Providers = append(cat.Providers, s.llmRouter.ProviderIDs()...)
	}
	return cat
}

// connectedMCPSet returns the set of currently connected MCP server names.
func (s *Server) connectedMCPSet() map[string]bool {
	set := map[string]bool{}
	for _, mt := range s.snapshotMCPTools() {
		set[mt.Server] = true
	}
	return set
}

// configuredChannelSet returns channel id → configured+enabled, lowercased.
func (s *Server) configuredChannelSet() map[string]bool {
	set := map[string]bool{}
	for _, id := range s.groundedChannels() {
		set[strings.ToLower(id)] = true
	}
	return set
}

// studioAutowireRequest is the POST /api/v1/studio/autowire body.
type studioAutowireRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioAutowire implements POST /api/v1/studio/autowire. It runs the
// deterministic data-flow repair over a draft (fill empty required tool args +
// reconcile dangling {{ .var }} references to the right upstream output) and
// returns the repaired workflow plus the number of fixes. This lets the GUI
// offer "Fix automatically" on a draft that was loaded or edited outside a
// fresh compile (where the repair already runs). No LLM call.
func (s *Server) handleStudioAutowire(c *fiber.Ctx) error {
	var req studioAutowireRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)
	model := s.studioLLM()

	// 1) Deterministic repair (auto-wire empty args, reconcile dangling vars,
	//    collapse template typos).
	fixed := studio.RepairWiring(&req.Workflow, cat)

	// 2) Iterative LLM repair over the FULL preflight report: validate, hand the
	//    model EVERY remaining blocker (template/wiring/MCP/python/etc.), apply
	//    its corrected draft, re-run deterministic passes, and re-validate. Up to
	//    a few rounds so it converges instead of fixing one thing at a time.
	if model != nil {
		for round := 0; round < 3; round++ {
			pf := studio.Preflight(req.Workflow, in)
			if pf.OK || len(pf.Blockers) == 0 {
				break
			}
			problems := make([]string, 0, len(pf.Blockers))
			for _, b := range pf.Blockers {
				problems = append(problems, studioProblemLine(b))
			}
			repaired, changed := studio.RepairWithProblems(c.Context(), model, req.Workflow, problems, cat)
			if !changed {
				break // model couldn't improve it; stop rather than loop pointlessly
			}
			studio.RepairWiring(&repaired, cat)
			req.Workflow = repaired
			fixed++
		}
	}

	final := studio.Preflight(req.Workflow, in)
	return c.JSON(fiber.Map{"workflow": req.Workflow, "fixed": fixed, "preflight": final})
}

// studioProblemLine renders a preflight issue as a single repair instruction
// (message + the node it applies to + the suggested fix).
func studioProblemLine(i studio.PreflightIssue) string {
	out := i.Message
	if i.NodeID != "" {
		out = "node \"" + i.NodeID + "\": " + out
	}
	if i.Fix != "" {
		out += " (" + i.Fix + ")"
	}
	return out
}

// studioTroubleshootRequest is the POST /api/v1/studio/troubleshoot body: a
// draft plus a runtime error message to fix.
type studioTroubleshootRequest struct {
	Workflow studio.Draft `json:"workflow"`
	Error    string       `json:"error"`
	Input    string       `json:"input,omitempty"`
	Evidence string       `json:"evidence,omitempty"`
}

// handleStudioTroubleshoot implements POST /api/v1/studio/troubleshoot. Given a
// draft and a RUNTIME error (e.g. from a failed scheduled run), it asks the
// model to fix the draft so that error won't recur, runs the deterministic
// passes, and returns the corrected draft + a fresh preflight. This is the
// "Fix with AI" loop for run-time failures, not just pre-save validation.
func (s *Server) handleStudioTroubleshoot(c *fiber.Ctx) error {
	var req studioTroubleshootRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.Error) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "error message is required")
	}
	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	problem := "At RUN TIME the agent failed with this error — change the workflow so it cannot happen again: " + strings.TrimSpace(req.Error)
	if strings.TrimSpace(req.Input) != "" {
		problem += "\nSample input that triggered it: " + strings.TrimSpace(req.Input)
	}
	if strings.TrimSpace(req.Evidence) != "" {
		problem += "\nObserved run evidence:\n" + strings.TrimSpace(req.Evidence)
	}
	problems := []string{problem}
	repaired, changed := studio.RepairWithProblems(c.Context(), model, req.Workflow, problems, cat)
	studio.RepairWiring(&repaired, cat)
	pf := studio.Preflight(repaired, s.preflightInput(c, cat))
	return c.JSON(fiber.Map{"workflow": repaired, "changed": changed, "preflight": pf})
}

// studioTraceStore lazily initialises the bounded build-trace store. Disk
// persistence is enabled when SOULACY_STUDIO_TRACE_DIR names a writable
// directory; otherwise the store is in-memory only (still fully readable via the
// build-trace endpoints, just not durable across restarts).
func (s *Server) studioTraceStore() *studio.BuildTraceStore {
	s.buildTracesOnce.Do(func() {
		s.buildTraces = studio.NewBuildTraceStore(50, os.Getenv("SOULACY_STUDIO_TRACE_DIR"))
	})
	return s.buildTraces
}

// handleStudioBuildTrace implements GET /api/v1/studio/build-trace. With ?id it
// returns that build's full structured trace; without, the most recent build.
// The trace is the durable record of every phase the autonomous loop ran —
// snapshots, preflight, each repair, each verify — with timings and detail, so a
// build that failed (or a 6am scheduled one) is debuggable without server logs.
func (s *Server) handleStudioBuildTrace(c *fiber.Ctx) error {
	st := s.studioTraceStore()
	id := strings.TrimSpace(c.Query("id"))
	var (
		tr *studio.BuildTrace
		ok bool
	)
	if id != "" {
		tr, ok = st.Get(id)
	} else {
		tr, ok = st.Latest()
	}
	if !ok {
		return c.JSON(studio.TraceDump{ID: id, Events: []studio.TraceEvent{}})
	}
	return c.JSON(tr.Dump())
}

// handleStudioBuildTraces implements GET /api/v1/studio/build-traces — compact
// summaries of retained builds (newest first) for a "recent builds" picker.
func (s *Server) handleStudioBuildTraces(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"traces": s.studioTraceStore().List(), "dir": s.studioTraceStore().Dir()})
}

// studioBuildRequest is the POST /api/v1/studio/build body: the current draft to
// make work, plus the originating intent (used to synthesize self-tests).
type studioBuildRequest struct {
	Workflow studio.Draft `json:"workflow"`
	Intent   string       `json:"intent,omitempty"`
	// Verify, when false, runs the loop in validation-only mode (no real
	// execution). Defaults to true: the Architect actually runs the agent.
	Verify *bool `json:"verify,omitempty"`
}

// handleStudioBuild implements POST /api/v1/studio/build — the Architect's
// autonomous build-verify-repair loop. It (1) fills capability holes with
// generated glue code, (2) synthesizes self-tests from the intent, (3) drives
// studio.BuildUntilWorks with a REAL-execution verifier backed by the engine
// (tool + Python steps run for real), repairing every blocker and every runtime
// error it hits until the agent works or it exhausts its budget, and (4) returns
// the final draft plus a full, transparent attempt transcript.
func (s *Server) handleStudioBuild(c *fiber.Ctx) error {
	var req studioBuildRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)

	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		intent = req.Workflow.Intent
	}

	// Open a durable build trace covering the WHOLE flow — glue, self-tests, and
	// the loop — so every build is debuggable end to end.
	tr := s.studioTraceStore().New(intent)
	defer func() { _ = tr.Close() }()

	// (1) Fill capability holes with generated glue code before the loop.
	var glueNotes []string
	doneGlue := tr.Step("phase", "glue", 0, "filling capability gaps with generated glue")
	if _, notes := studio.EnsureCapabilities(c.Context(), model, &req.Workflow, cat); len(notes) > 0 {
		glueNotes = notes
	}
	doneGlue(nil, map[string]any{"notes": glueNotes})

	// (2) Synthesize self-tests so "it works" is checked, not assumed.
	doneTests := tr.Step("phase", "tests", 0, "synthesizing self-tests from the intent")
	tests := studio.SynthesizeTests(c.Context(), model, intent, req.Workflow, cat)
	doneTests(nil, map[string]any{"count": len(tests)})

	// (3) Choose the verifier. Default: REAL execution via the engine.
	verify := true
	if req.Verify != nil {
		verify = *req.Verify
	}
	opts := studio.BuildOptions{In: in, Tests: tests, Trace: tr, ExtraProblems: s.pythonBuildProblems}
	if verify {
		opts.Verifier = studio.RealRunVerifier{Runner: s.studioRealRunner()}
	}

	rep := studio.BuildUntilWorks(c.Context(), model, req.Workflow, cat, opts)

	final := studio.Preflight(rep.Workflow, in)
	return c.JSON(fiber.Map{
		"report":    rep,
		"preflight": final,
		"glue":      glueNotes,
		"traceId":   tr.ID,
	})
}

// handleStudioBuildStream is the streaming variant of /studio/build. It runs the
// same autonomous loop but emits a text/event-stream so the GUI shows live
// progress (each attempt starting, what's being repaired, when it's running,
// the outcome) instead of a frozen "Building…". The stream ends with an
// `event: done` frame carrying the full {report, preflight, glue} payload.
func (s *Server) handleStudioBuildStream(c *fiber.Ctx) error {
	var req studioBuildRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)
	// Detach from the request context so the loop isn't cancelled when the
	// handler returns to take over the connection as a stream writer.
	ctx := context.WithoutCancel(c.Context())

	// Events are produced by the loop (in a goroutine) and drained by the SSE
	// writer. Buffered so the loop never blocks on a slow client.
	type sse struct{ event, data string }
	events := make(chan sse, 64)

	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		intent = req.Workflow.Intent
	}
	// Durable trace for the whole streamed build (glue → tests → loop).
	tr := s.studioTraceStore().New(intent)

	// Heartbeat: during a long verify step the build can legitimately produce no
	// events for minutes (it's running the agent against a real model + tools).
	// A periodic keepalive frame keeps the connection visibly alive end-to-end so
	// no intermediary (or browser) treats the idle stream as dead. The frame is a
	// harmless `{"kind":"ping"}` with no message — the client's parser ignores it
	// (non-'done', no .message). hbStop + WaitGroup guarantee the heartbeat has
	// fully stopped BEFORE the producer closes `events`, so there is no send on a
	// closed channel.
	hbStop := make(chan struct{})
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbStop:
				return
			case <-t.C:
				select {
				case events <- sse{event: "event", data: `{"kind":"ping"}`}:
				case <-hbStop:
					return
				default: // buffer full → a real event is already in flight; skip
				}
			}
		}
	}()

	go func() {
		// Order matters: close(events) is registered first so it runs LAST —
		// after the heartbeat goroutine has been stopped and joined.
		defer close(events)
		defer func() { close(hbStop); hbWG.Wait() }()
		defer func() { _ = tr.Close() }()
		emit := func(kind, data string) {
			select {
			case events <- sse{event: kind, data: data}:
			default: // drop if the client is gone / buffer full
			}
		}
		// (1) Glue, then (2) self-tests — reported as their own steps.
		doneGlue := tr.Step("phase", "glue", 0, "filling capability gaps with generated glue")
		var glueNotes []string
		if _, notes := studio.EnsureCapabilities(ctx, model, &req.Workflow, cat); len(notes) > 0 {
			glueNotes = notes
			for _, nt := range notes {
				emit("event", jsonMsg("glue", "🧩 "+nt))
			}
		}
		doneGlue(nil, map[string]any{"notes": glueNotes})

		emit("event", jsonMsg("tests", "Writing self-tests…"))
		doneTests := tr.Step("phase", "tests", 0, "synthesizing self-tests from the intent")
		tests := studio.SynthesizeTests(ctx, model, intent, req.Workflow, cat)
		doneTests(nil, map[string]any{"count": len(tests)})

		verify := true
		if req.Verify != nil {
			verify = *req.Verify
		}
		opts := studio.BuildOptions{In: in, Tests: tests, Trace: tr, ExtraProblems: s.pythonBuildProblems}
		if verify {
			opts.Verifier = studio.RealRunVerifier{Runner: s.studioRealRunner()}
		}
		opts.OnEvent = func(ev studio.BuildEvent) {
			b, _ := json.Marshal(ev)
			emit("event", string(b))
		}

		rep := studio.BuildUntilWorks(ctx, model, req.Workflow, cat, opts)
		final := studio.Preflight(rep.Workflow, in)
		done, _ := json.Marshal(fiber.Map{"report": rep, "preflight": final, "traceId": tr.ID})
		emit("done", string(done))
	}()

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	c.Context().SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		for ev := range events {
			if ev.event != "" {
				fmt.Fprintf(w, "event: %s\n", ev.event) //nolint:errcheck
			}
			fmt.Fprintf(w, "data: %s\n\n", ev.data) //nolint:errcheck
			w.Flush()                               //nolint:errcheck
		}
	}))
	return nil
}

// jsonMsg renders a {kind,message} progress frame for the build stream.
func jsonMsg(kind, msg string) string {
	b, _ := json.Marshal(studio.BuildEvent{Kind: kind, Message: msg})
	return string(b)
}

// studioRealRunner wires the studio RealRunVerifier to the engine's execution
// primitives so a build-time verification run invokes real tools and real Python
// — the only way to catch failures that only appear when the agent actually runs.
func (s *Server) studioRealRunner() studio.RealRunner {
	if s.engine == nil {
		return studio.RealRunner{}
	}
	return studio.RealRunner{
		Tool: func(ctx context.Context, name, argsJSON string) (json.RawMessage, error) {
			return s.engine.RunTool(ctx, name, argsJSON)
		},
		Python: func(ctx context.Context, code string, argsJSON []byte) (json.RawMessage, error) {
			return s.engine.RunInlinePython(ctx, code, argsJSON)
		},
	}
}

// truncate shortens s to at most n runes, appending an ellipsis when cut, so a
// try-run trace stays compact without dumping large tool payloads.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// studioTryAgentRequest runs an UNSAVED reasoning agent against one question.
type studioTryAgentRequest struct {
	Workflow studio.Draft `json:"workflow"`
	Question string       `json:"question"`
}

// handleStudioTryAgent implements POST /api/v1/studio/try-agent. It runs an
// UNSAVED ReAct/Plan-Execute agent against a single sample question so the author
// can see real behaviour before saving. The agent is registered in memory ONLY
// (never written to disk), runs unattended so a guardrail confirmation can't hang
// the try, and is removed immediately after. Real tools/skills DO fire (that's
// the point of a real try), but the reply is returned to the caller and is NOT
// delivered to any channel — calling Handle directly never routes the reply out.
func (s *Server) handleStudioTryAgent(c *fiber.Ctx) error {
	var req studioTryAgentRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if s.engine == nil || s.loader == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "runtime engine unavailable")
	}
	// Runnable when it's a reasoning agent OR a workflow with actual steps. A bare
	// empty draft has nothing to run.
	if !req.Workflow.IsAgent() && len(req.Workflow.Flow.Nodes) == 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "nothing to run — add steps, or use a ReAct/Plan-Execute agent")
	}
	q := strings.TrimSpace(req.Question)
	if q == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "question is required")
	}

	defVal, err := studio.ToAgentDefinition(req.Workflow, true)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	def := &defVal
	def.ID = "studio-try-" + uuid.NewString()
	def.Enabled = true
	def.Unattended = true // auto-approve confirmations so the throwaway run can't hang
	def.SourcePath = ""   // never persisted

	s.loader.Register(def)
	defer s.loader.Unregister(def.ID)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), 120*time.Second)
	defer cancel()

	// Capture the exact sequence of skills/tools the agent invokes, so the author
	// can see whether it routed to the right one. read_skill calls record which
	// skill was loaded.
	var (
		traceMu sync.Mutex
		trace   = []fiber.Map{}
	)
	ctx = runtime.WithToolObserver(ctx, func(call message.ToolCall, result string, isErr bool) {
		detail := ""
		if call.Name == "read_skill" {
			if sk, ok := call.Arguments["skill_name"].(string); ok {
				detail = sk
			}
		}
		args := ""
		if len(call.Arguments) > 0 {
			if b, err := json.Marshal(call.Arguments); err == nil {
				args = truncate(string(b), 240)
			}
		}
		traceMu.Lock()
		trace = append(trace, fiber.Map{
			"name":   call.Name,
			"detail": detail,
			"args":   args,
			"result": truncate(strings.TrimSpace(result), 400),
			"error":  isErr,
		})
		traceMu.Unlock()
	})

	// Capture EVERY workflow node (python, branch-adjacent tool, agent, llm) with
	// its input/output/error — not just tool calls — so the author can see what
	// each node returned and why a branch went the way it did (e.g. a Python node
	// refused by the consent gate, or one that produced no data).
	var nodeTrace []fiber.Map
	ctx = runtime.WithFlowNodeObserver(ctx, func(rec reasoningpkg.FlowNodeRun) {
		out := strings.TrimSpace(string(rec.Output))
		traceMu.Lock()
		nodeTrace = append(nodeTrace, fiber.Map{
			"node_id":     rec.NodeID,
			"kind":        rec.Kind,
			"input":       truncate(rec.Input, 300),
			"output":      truncate(out, 600),
			"error":       rec.Error,
			"skipped":     rec.Error != "" && strings.Contains(strings.ToLower(rec.Error), "consent"),
			"duration_ms": rec.DurationMS,
		})
		traceMu.Unlock()
	})

	msg := message.Message{
		ID:        uuid.NewString(),
		SessionID: "studio-try-" + def.ID,
		AgentID:   def.ID,
		Channel:   "studio-try",
		ThreadID:  "studio",
		UserID:    "studio",
		Username:  "studio",
		Role:      message.RoleUser,
		Parts:     message.Text(q),
		CreatedAt: time.Now().UTC(),
	}

	reply, runErr := s.engine.Handle(ctx, msg)
	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	traceMu.Lock()
	usedTrace := trace
	usedNodeTrace := nodeTrace
	traceMu.Unlock()
	if usedNodeTrace == nil {
		usedNodeTrace = []fiber.Map{}
	}
	resp := fiber.Map{"reply": replyText, "parts": reply.Parts, "trace": usedTrace, "node_trace": usedNodeTrace}
	if runErr != nil {
		resp["error"] = runErr.Error()
	}
	return c.JSON(resp)
}

// handleStudioFailedRuns implements GET /api/v1/studio/failed-runs. It surfaces
// the runs that FAILED at run time (including unattended scheduled runs), drawn
// from the dead-letter queue the engine writes on every failed Handle(). Each
// entry names the agent, the real error, and when it failed — so the user can
// self-heal a 6am scheduled failure without pasting anything anywhere. This is
// the "Soulacy is the only savior" feed: every failure is actionable in-product.
func (s *Server) handleStudioFailedRuns(c *fiber.Ctx) error {
	if s.dlqStore == nil {
		return c.JSON(fiber.Map{"runs": []any{}})
	}
	items, err := s.dlqStore.List(c.Context(), "")
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	type failedRun struct {
		ID        string `json:"id"`
		AgentID   string `json:"agentId"`
		AgentName string `json:"agentName"`
		Error     string `json:"error"`
		Attempts  int    `json:"attempts"`
		FailedAt  string `json:"failedAt"`
		Healable  bool   `json:"healable"`
	}
	runs := make([]failedRun, 0, len(items))
	for _, it := range items {
		fr := failedRun{
			ID: it.ID, AgentID: it.Queue, Error: it.ErrorMsg,
			Attempts: it.Attempts, FailedAt: it.LastAttemptAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if s.loader != nil {
			if def := s.loader.Get(it.Queue); def != nil {
				fr.AgentName = def.Name
				fr.Healable = true // the saved agent still exists, so we can repair it
			}
		}
		runs = append(runs, fr)
	}
	return c.JSON(fiber.Map{"runs": runs})
}

// handleStudioRunTrace implements GET /api/v1/studio/run-trace. It returns the
// per-block run trace (Story S0.3 Phase 1) of a flow run — each executed block's
// input, output, duration, error, and whether its input came from typed port
// wires — so the GUI can show a non-technical user WHERE a run went wrong.
//
// Query: runId selects a specific run; otherwise agentId returns that agent's
// most recent run. The trace is best-effort and in-memory, so a run the gateway
// no longer retains returns an empty (not error) trace.
func (s *Server) handleStudioRunTrace(c *fiber.Ctx) error {
	if s.engine == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "engine unavailable")
	}
	agentID := strings.TrimSpace(c.Query("agentId"))
	runID := strings.TrimSpace(c.Query("runId"))
	if agentID == "" && runID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agentId or runId is required")
	}
	// Types inferred so studio.go needn't import the runtime package.
	tr, ok := s.engine.LatestFlowTrace(agentID)
	if runID != "" {
		// Disk-aware: an old run that aged out of memory is still served from the
		// durable run history.
		tr, ok = s.engine.FlowTraceFor(agentID, runID)
	}
	if !ok {
		return c.JSON(fiber.Map{"agentId": agentID, "runId": runID, "entries": []any{}})
	}
	return c.JSON(tr)
}

// handleStudioRunDiagnosis implements GET /api/v1/studio/run-diagnosis. It
// returns a deterministic diagnosis for a retained run trace: the failing node,
// likely root cause, evidence, and next action. This gives Studio and Activity a
// shared troubleshooting vocabulary without requiring an LLM just to classify
// common platform failures.
func (s *Server) handleStudioRunDiagnosis(c *fiber.Ctx) error {
	if s.engine == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "engine unavailable")
	}
	agentID := strings.TrimSpace(c.Query("agentId"))
	runID := strings.TrimSpace(c.Query("runId"))
	if agentID == "" && runID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agentId or runId is required")
	}
	d, ok := s.engine.FlowRunDiagnosis(agentID, runID)
	if !ok {
		return c.JSON(fiber.Map{
			"agentId": agentID,
			"runId":   runID,
			"status":  "empty",
			"summary": "No retained run trace was found.",
			"suggestions": []string{
				"Run the agent once and refresh this panel.",
				"Check Activity if the failure happened before the workflow emitted a trace.",
			},
			"retryable": true,
		})
	}
	return c.JSON(d)
}

// handleStudioRunHistory implements GET /api/v1/studio/run-history. It returns a
// summary of EVERY retained run for an agent — scheduled and on-demand alike,
// newest first, each with its trigger source and verdict — so the GUI can show a
// complete run history instead of just the latest run. In-memory and best-effort
// (a run the gateway no longer retains is dropped), so it returns an empty list,
// not an error, when nothing is recorded.
func (s *Server) handleStudioRunHistory(c *fiber.Ctx) error {
	if s.engine == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "engine unavailable")
	}
	agentID := strings.TrimSpace(c.Query("agentId"))
	if agentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agentId is required")
	}
	return c.JSON(fiber.Map{"agentId": agentID, "runs": s.engine.FlowRunHistory(agentID)})
}

// studioDiagnoseRunRequest is the POST /api/v1/studio/diagnose-run body: a
// dead-letter entry id to diagnose and self-heal.
type studioDiagnoseRunRequest struct {
	ID string `json:"id"`
}

type studioDiagnoseSessionRequest struct {
	AgentID   string `json:"agentId"`
	SessionID string `json:"sessionId"`
}

// handleStudioDiagnoseRun implements POST /api/v1/studio/diagnose-run. Given a
// failed run, it loads the SAVED agent, reconstructs its draft, repairs it
// against the REAL runtime error, then runs the full build-verify loop so the
// fix is validated (and, for workflows, actually re-executed). It returns the
// healed draft + a transcript so the user can review and apply it — turning an
// opaque scheduled-run failure into a one-click fix.
func (s *Server) handleStudioDiagnoseRun(c *fiber.Ctx) error {
	var req studioDiagnoseRunRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.ID) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "failed-run id is required")
	}
	if s.dlqStore == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "no failed-run history available")
	}
	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	entry, err := s.dlqStore.Get(c.Context(), req.ID)
	if err != nil {
		return s.errMsg(c, fiber.StatusNotFound, "failed run not found")
	}
	def := s.loader.Get(entry.Queue)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "the agent for this run no longer exists")
	}
	draft := studio.FromAgentDefinition(*def)

	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)

	// (1) Repair directly against the real runtime error.
	problem := "At RUN TIME this agent failed with: " + strings.TrimSpace(entry.ErrorMsg) +
		" — change the agent so this cannot happen again."
	healed, changed := studio.RepairWithProblems(c.Context(), model, draft, []string{problem}, cat)
	studio.RepairWiring(&healed, cat)

	// (2) Validate (and, for workflows, re-run) to confirm the fix holds.
	rep := studio.BuildUntilWorks(c.Context(), model, healed, cat, studio.BuildOptions{
		In:            in,
		Verifier:      studio.RealRunVerifier{Runner: s.studioRealRunner()},
		ExtraProblems: s.pythonBuildProblems,
	})

	return c.JSON(fiber.Map{
		"agentId":   entry.Queue,
		"agentName": def.Name,
		"error":     entry.ErrorMsg,
		"changed":   changed,
		"workflow":  rep.Workflow,
		"report":    rep,
		"preflight": studio.Preflight(rep.Workflow, in),
	})
}

// handleStudioDiagnoseSession repairs a saved agent from a concrete Activity
// session. Unlike handleStudioDiagnoseRun, this does not require a dead-letter
// queue entry; it reconstructs evidence from the action log, so any visible
// Activity error can become a Studio debugging session.
func (s *Server) handleStudioDiagnoseSession(c *fiber.Ctx) error {
	var req studioDiagnoseSessionRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.AgentID == "" || req.SessionID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agentId and sessionId are required")
	}
	if s.actions == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "action logging disabled")
	}
	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	def := s.loader.Get(req.AgentID)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "the agent for this run no longer exists")
	}
	events, err := s.actions.Tail(req.AgentID, 5000)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	evidence, errText, found := studioSessionEvidence(events, req.AgentID, req.SessionID)
	if !found {
		return s.errMsg(c, fiber.StatusNotFound, "no action-log events found for that agent/session")
	}
	if strings.TrimSpace(errText) == "" {
		errText = "The run did not emit an explicit error, but the operator requested debugging from Activity."
	}

	draft := studio.FromAgentDefinition(*def)
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	in := s.preflightInput(c, cat)

	problem := "A REAL execution of this agent failed or needs debugging.\n" +
		"Agent: " + req.AgentID + "\nSession: " + req.SessionID + "\n" +
		"Error: " + strings.TrimSpace(errText) + "\n" +
		"Recent action-log evidence:\n" + evidence + "\n" +
		"Change the agent workflow/tools/prompts so this failure is prevented. Preserve the user's intent and only make targeted fixes."
	healed, changed := studio.RepairWithProblems(c.Context(), model, draft, []string{problem}, cat)
	studio.RepairWiring(&healed, cat)

	rep := studio.BuildUntilWorks(c.Context(), model, healed, cat, studio.BuildOptions{
		In:            in,
		Verifier:      studio.RealRunVerifier{Runner: s.studioRealRunner()},
		ExtraProblems: s.pythonBuildProblems,
	})

	return c.JSON(fiber.Map{
		"agentId":   req.AgentID,
		"agentName": def.Name,
		"sessionId": req.SessionID,
		"error":     errText,
		"evidence":  evidence,
		"changed":   changed,
		"workflow":  rep.Workflow,
		"report":    rep,
		"preflight": studio.Preflight(rep.Workflow, in),
	})
}

func studioSessionEvidence(events []message.Event, agentID, sessionID string) (evidence, errText string, found bool) {
	var lines []string
	for _, ev := range events {
		if ev.AgentID != agentID || ev.SessionID != sessionID {
			continue
		}
		found = true
		line := studioEventEvidenceLine(ev)
		if line != "" {
			lines = append(lines, line)
		}
		if candidate := studioEventErrorText(ev); candidate != "" {
			errText = candidate
		}
	}
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n"), errText, found
}

func studioEventEvidenceLine(ev message.Event) string {
	payload := studioCompactPayload(ev.Payload, 420)
	ts := ""
	if !ev.Timestamp.IsZero() {
		ts = ev.Timestamp.UTC().Format("15:04:05") + " "
	}
	return fmt.Sprintf("- %s%s: %s", ts, ev.Type, payload)
}

func studioEventErrorText(ev message.Event) string {
	p, ok := ev.Payload.(map[string]any)
	if !ok {
		b, err := json.Marshal(ev.Payload)
		if err != nil {
			if ev.Type == "error" {
				return fmt.Sprint(ev.Payload)
			}
			return ""
		}
		_ = json.Unmarshal(b, &p)
	}
	if ev.Type == "error" {
		for _, k := range []string{"error", "message", "content"} {
			if v, ok := p[k].(string); ok && strings.TrimSpace(v) != "" {
				if stage, _ := p["stage"].(string); strings.TrimSpace(stage) != "" {
					return stage + ": " + v
				}
				return v
			}
		}
	}
	for _, k := range []string{"error", "message"} {
		if v, ok := p[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func studioCompactPayload(v any, max int) string {
	if max <= 0 {
		max = 400
	}
	var s string
	switch t := v.(type) {
	case string:
		s = t
	default:
		b, err := json.Marshal(v)
		if err != nil {
			s = fmt.Sprint(v)
		} else {
			s = string(b)
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// studioCompileAgentRequest is the POST /api/v1/studio/compile-agent body.
type studioCompileAgentRequest struct {
	Intent   string            `json:"intent"`
	Strategy string            `json:"strategy"` // react | plan_execute
	Catalog  studio.Catalog    `json:"catalog,omitempty"`
	Answers  map[string]string `json:"answers,omitempty"`
}

// handleStudioCompileAgent implements POST /api/v1/studio/compile-agent. It
// generates a ReAct/Plan-Execute AGENT (system prompt + tool allowlist + peers/
// skills/KBs, NO workflow) for intents that need a reasoning loop rather than a
// fixed graph (local-first pivot).
func (s *Server) handleStudioCompileAgent(c *fiber.Ctx) error {
	var req studioCompileAgentRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.Intent) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "intent is required")
	}
	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	s.groundCatalog(&req.Catalog)
	res, err := studio.CompileAgent(c.Context(), model, req.Intent, req.Catalog, req.Strategy, req.Answers)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.stampDefaultLLM(&res.Workflow)         // make the runtime provider/model explicit in the YAML
	studio.ApplyTemplateFixes(&res.Workflow) // deterministic self-heal (no-op when there's no flow graph)
	if res.Explanation != nil {
		pf := studio.Preflight(res.Workflow, s.preflightInput(c, req.Catalog))
		var needs []string
		for _, b := range pf.Blockers {
			needs = append(needs, preflightLine(b))
		}
		for _, w := range pf.Warnings {
			needs = append(needs, preflightLine(w))
		}
		res.Explanation.NeedsConfig = needs
	}
	return c.JSON(res)
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

	// Ground the compiler in the REAL installed skills + connected MCP tools
	// (authoritative, server-side) so it maps loose references to actual
	// capabilities and wires real MCP tools instead of inventing names.
	s.groundCatalog(&req.Catalog)

	// SINGLE authoritative architecture decision, evaluated over the raw + refined
	// text (the SAME input refine used) so it can't diverge by entry path. A
	// reasoning task (dynamic skill routing, async polling, per-item loops, or an
	// explicit multi-phase plan) is built as an AGENT here — never a fixed
	// workflow. This is the server-side guarantee behind "if it can't be a
	// workflow, don't build one": even if the client calls /compile, the server
	// returns the agent, so the user never gets a workflow carrying a "use ReAct"
	// sticker (or a brittle multi-agent flow with invented peers).
	if mode := studio.RecommendAgentMode(req.Intent + " " + req.RawIntent); mode != "" {
		ares, aerr := studio.CompileAgent(c.Context(), model, req.Intent, req.Catalog, mode, req.Answers)
		if aerr != nil {
			// Do NOT silently fall through to the workflow compiler — that would
			// produce exactly the brittle workflow this guard exists to prevent,
			// with no signal. Surface the failure so the user (or a retry) knows
			// the intended agent build didn't happen.
			return s.errJSON(c, fiber.StatusInternalServerError, aerr)
		}
		s.stampDefaultLLM(&ares.Workflow) // make the runtime provider/model explicit in the YAML
		return c.JSON(ares)
	}

	res, err := studio.Compile(c.Context(), model, req.Intent, req.Catalog, req.Answers)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.stampDefaultLLM(&res.Workflow) // make the runtime provider/model explicit in the YAML

	// Deterministic self-heal at generation, regardless of model quality:
	//  1) RepairWiring — reconcile dangling {{ .var }} refs to the right upstream
	//     output and fill empty required tool args.
	//  2) ApplyTemplateFixes — scalarize any whole-object / wrong-nested refs
	//     (including ones step 1 may have wired to an object).
	studio.RepairWiring(&res.Workflow, req.Catalog)
	studio.ApplyTemplateFixes(&res.Workflow)

	// Auto-repair: if deterministic passes didn't clear every blocker (e.g. a
	// dangling {{ .id }} that should be {{ .notebook.id }}, an unsupported
	// template function, a malformed predicate), spend ONE LLM pass to fix the
	// residue — then deterministically heal its output so the model can only
	// improve correctness. This replaces the manual Validate→AI-review→Fix loop
	// with a single automatic pass and a clean flow on the canvas.
	if model != nil {
		if pf := studio.Preflight(res.Workflow, s.preflightInput(c, req.Catalog)); len(pf.Blockers) > 0 {
			s.autoRepairWorkflow(c.Context(), &res.Workflow, pf.Blockers, req.Catalog)
		}
	}

	// Surface setup gaps right at generation time (Story #12): run preflight on
	// the fresh draft and fold the fixes into the explanation's NeedsConfig so
	// the user sees what they still have to configure before this can run.
	if res.Explanation != nil {
		pf := studio.Preflight(res.Workflow, s.preflightInput(c, req.Catalog))
		var needs []string
		for _, b := range pf.Blockers {
			needs = append(needs, preflightLine(b))
		}
		for _, w := range pf.Warnings {
			needs = append(needs, preflightLine(w))
		}
		res.Explanation.NeedsConfig = needs
	}
	return c.JSON(res)
}

// applyLocalPreset fills patient timeout/turn defaults on an agent that will run
// on a LOCAL model, but only where the draft didn't already set them (Stories
// #23/#24). No-op for cloud-bound agents — the engine's defaults are fine there.
func (s *Server) applyLocalPreset(def *agent.Definition) {
	provider := def.LLM.Provider
	if provider == "" {
		provider = s.cfg.LLM.DefaultProvider
	}
	baseURL := ""
	if pc, ok := s.cfg.LLM.Providers[provider]; ok {
		baseURL = pc.BaseURL
	}
	if !studio.IsLocalProvider(provider, baseURL) {
		return
	}
	p := studio.LocalPresetFor(def.LLM.Model)
	if def.RunTimeout == "" && p.RunTimeout != "" {
		def.RunTimeout = p.RunTimeout
	}
	if def.Reasoning.StepTimeout == "" && p.StepTimeout != "" {
		def.Reasoning.StepTimeout = p.StepTimeout
	}
	if def.Reasoning.TotalTimeout == "" && p.TotalTimeout != "" {
		def.Reasoning.TotalTimeout = p.TotalTimeout
	}
}

// preflightLine renders a preflight issue as a single human-readable line for
// the explanation's NeedsConfig list.
func preflightLine(i studio.PreflightIssue) string {
	if i.Fix != "" {
		return i.Message + " " + i.Fix
	}
	return i.Message
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
	res := studio.Validate(req.Workflow)
	// Argument-schema check against the live catalog: flag a tool node passing an
	// argument the tool doesn't accept (the "unexpected keyword argument" class),
	// before it fails at run time.
	res.Warnings = append(res.Warnings, studio.ValidateToolArgs(req.Workflow, s.studioCatalogSnapshot())...)
	// Python validity: syntax-check every inline python node and require the
	// run(inputs) entrypoint — catches broken generated code at build time
	// instead of at run time. Parse-only; never executes the code.
	if pyErrs := s.validatePythonNodes(req.Workflow); len(pyErrs) > 0 {
		res.Ok = false
		res.Errors = append(res.Errors, pyErrs...)
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
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	res, err := studio.Plan(req.Workflow)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(res)
}

// studioYAMLRequest carries a draft to serialize into SOUL.yaml (the same form
// a Save would write), for the Studio "Code" view.
type studioYAMLRequest struct {
	Workflow studio.Draft `json:"workflow"`
}

// handleStudioYAML implements POST /api/v1/studio/yaml. It converts the current
// draft into the exact agent.Definition a Save would persist, then returns it
// marshalled as SOUL.yaml so the GUI can show (and let the user edit) the code
// behind the canvas. Conversion errors (e.g. an unnamed workflow) come back as
// 400s with a clear message.
func (s *Server) handleStudioYAML(c *fiber.Ctx) error {
	var req studioYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	def, err := studio.ToAgentDefinition(req.Workflow, true)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	out, err := yaml.Marshal(&def)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"yaml": string(out)})
}

// studioFromYAMLRequest carries edited SOUL.yaml to parse back into a draft for
// the canvas.
type studioFromYAMLRequest struct {
	YAML string `json:"yaml"`
}

// handleStudioFromYAML implements POST /api/v1/studio/from-yaml. The Code view is
// authoritative, so when the user switches back to Canvas we parse the edited
// SOUL.yaml into an agent.Definition and map it onto a Studio draft. Because the
// draft⇄definition mapping is intentionally lossy (the canvas shows the flow
// graph, not every agent field), we also return human-readable warnings naming
// anything in the YAML that the canvas can't represent — so the user knows the
// YAML remains the source of truth for those parts.
func (s *Server) handleStudioFromYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML error: "+err.Error())
	}
	draft := studio.FromAgentDefinition(def)

	// Warnings must reflect what the draft⇄definition mapping ACTUALLY preserves,
	// or they mislead the user about data loss. A reasoning agent round-trips its
	// system prompt, tools, skills, knowledge and peers losslessly (they're shown/
	// editable in the agent panel), so none of those warrant a "kept in YAML"
	// warning. Only a fixed WORKFLOW agent has canvas-invisible fields.
	strat := strings.ToLower(strings.TrimSpace(def.Reasoning.Strategy))
	isReasoningAgent := strat == "react" || strat == "plan_execute"
	var warnings []string
	switch {
	case isReasoningAgent:
		warnings = append(warnings, "This is a reasoning agent (no fixed graph) — edit its prompt, tools and skills in the agent panel or here in SOUL.yaml; everything round-trips.")
	case def.Workflow == nil || len(def.Workflow.Nodes) == 0:
		warnings = append(warnings, "This agent has no workflow graph and no reasoning strategy — it's incomplete. Add steps on the canvas, or switch it to a ReAct agent.")
	default:
		if strings.TrimSpace(def.SystemPrompt) != "" {
			warnings = append(warnings, "This workflow's system prompt is generated from its steps; a canvas re-save will regenerate it from the graph.")
		}
	}
	return c.JSON(fiber.Map{"workflow": draft, "warnings": warnings})
}

// handleStudioSaveYAML implements POST /api/v1/studio/save-yaml. In Code view the
// YAML is authoritative, so this writes it to disk directly (parse → validate →
// loader.Upsert) rather than re-deriving from the draft — preserving fields the
// canvas can't express. Privileged-node consent is still enforced fail-closed by
// the runtime, and Studio saves stay disabled unless the YAML says otherwise.
func (s *Server) handleStudioSaveYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML error: "+err.Error())
	}
	if strings.TrimSpace(def.ID) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "the YAML needs an 'id' field before it can be saved")
	}
	if isProtectedSystemAgent(def.ID) {
		return protectedSystemAgentResponse(c)
	}

	report := agentvalidate.Definition(&def, "", s.agentValidationOptions(c.Context()), agentvalidate.Report{})
	if report.Errors > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":      "validation failed",
			"validation": report,
		})
	}

	if def.LLM.Provider == "" {
		def.LLM.Provider = s.cfg.LLM.DefaultProvider
	}
	s.applyLocalPreset(&def)

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	// If this agent already exists, write back into its OWN directory and carry
	// its source path, so Save updates the agent in place instead of dropping a
	// duplicate copy under the first configured agent dir. SourcePath is
	// <baseDir>/<id>/SOUL.yaml, so the base dir is the parent of the agent dir.
	if existing := s.loader.Get(def.ID); existing != nil && existing.SourcePath != "" {
		def.SourcePath = existing.SourcePath
		dir = filepath.Dir(filepath.Dir(existing.SourcePath))
	}
	if err := s.loader.Upsert(dir, &def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.scheduler.DeregisterAgent(def.ID)
	if err := s.scheduler.RegisterAgent(&def); err != nil {
		s.log.Warn("scheduler registration failed", zap.String("agent", def.ID), zap.Error(err))
	}
	return c.JSON(fiber.Map{"id": def.ID, "agent": &def, "validation": report})
}

// yamlValidateItem is one problem found validating SOUL.yaml, normalized across
// the three checkers so the GUI can render a single list. Source says which
// layer found it; Severity is "error" (must fix) or "warning" (should review).
type yamlValidateItem struct {
	Severity string `json:"severity"` // error | warning
	Source   string `json:"source"`   // yaml | definition | graph | runtime
	NodeID   string `json:"nodeId,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// handleStudioValidateYAML implements POST /api/v1/studio/validate-yaml — the
// "Validate" button in the Code view. It runs the FULL battery against the
// edited SOUL.yaml so problems are caught before save/run:
//   - YAML syntax (it parses at all),
//   - definition correctness (agentvalidate: required fields, tool/channel sanity),
//   - graph integrity (studio.Validate → reasoning.CompileFlow: dangling edges,
//     bad entry/output, unreachable nodes),
//   - runtime-error avoidance (studio.Preflight against LIVE state: missing/
//     disconnected MCP servers, unfilled required tool args, unconfigured
//     channels, invalid schedules, and template-reference bugs like passing a
//     whole object where a scalar id is needed).
//
// It always returns 200 with a consolidated report (problems are data, not HTTP
// errors) so the UI can list them inline.
func (s *Server) handleStudioValidateYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}

	var items []yamlValidateItem
	add := func(sev, src, node, msg, fix string) {
		items = append(items, yamlValidateItem{Severity: sev, Source: src, NodeID: node, Message: msg, Fix: fix})
	}

	// 1) Syntax. A parse failure is terminal — nothing else can run.
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		add("error", "yaml", "", "YAML syntax error: "+err.Error(), "Fix the indentation, quoting, or a stray colon, then validate again.")
		return c.JSON(buildYAMLValidation(items, nil))
	}

	// 2) Definition correctness.
	if strings.TrimSpace(def.ID) == "" {
		add("error", "definition", "", "Missing required field 'id'.", "Add a top-level 'id:' so the agent can be saved and referenced.")
	}
	rep := agentvalidate.Definition(&def, "", s.agentValidationOptions(c.Context()), agentvalidate.Report{})
	for _, f := range rep.Findings {
		sev := "warning"
		if f.Severity == agentvalidate.Error {
			sev = "error"
		}
		msg := f.Message
		if strings.TrimSpace(f.Field) != "" {
			msg = f.Field + ": " + msg
		}
		add(sev, "definition", "", msg, f.Suggestion)
	}

	// 3) Graph integrity + 4) runtime checks operate on the flow form. Only run
	// the graph compiler when there IS a graph (a reasoning/ReAct agent has none;
	// its correctness is covered by the definition checks above).
	draft := studio.FromAgentDefinition(def)
	if def.Workflow != nil && len(def.Workflow.Nodes) > 0 {
		vr := studio.Validate(draft)
		for _, e := range vr.Errors {
			add("error", "graph", e.NodeID, e.Message, "Fix the workflow graph (edges, entry/output, node ids).")
		}
		for _, w := range vr.Warnings {
			add("warning", "graph", w.NodeID, w.Message, "")
		}
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	pf := studio.Preflight(draft, s.preflightInput(c, cat))
	for _, b := range pf.Blockers {
		add("error", "runtime", b.NodeID, b.Message, b.Fix)
	}
	for _, w := range pf.Warnings {
		add("warning", "runtime", w.NodeID, w.Message, w.Fix)
	}

	// One-click auto-fixes for the template-reference warnings.
	fixes := studio.SuggestTemplateFixes(draft)

	return c.JSON(buildYAMLValidation(items, fixes))
}

// buildYAMLValidation tallies the items into the response envelope, including any
// machine-applicable template fixes for the GUI's "Fix" button.
func buildYAMLValidation(items []yamlValidateItem, fixes []studio.TemplateFix) fiber.Map {
	errors, warnings := 0, 0
	for _, it := range items {
		if it.Severity == "error" {
			errors++
		} else {
			warnings++
		}
	}
	if items == nil {
		items = []yamlValidateItem{}
	}
	if fixes == nil {
		fixes = []studio.TemplateFix{}
	}
	return fiber.Map{"ok": errors == 0, "errors": errors, "warnings": warnings, "items": items, "fixes": fixes}
}

// issueLine formats one validation problem for the LLM fixer prompt.
func issueLine(sev, node, msg string) string {
	if strings.TrimSpace(msg) == "" {
		return ""
	}
	if strings.TrimSpace(node) != "" {
		return sev + " [" + node + "]: " + msg
	}
	return sev + ": " + msg
}

// handleStudioFixYAML implements POST /api/v1/studio/fix-yaml — the "Fix with
// AI" button. It collects the current validation problems and asks the framework
// LLM to rewrite the SOUL.yaml so they're resolved, then returns the corrected
// (and parse-checked) document. Unlike the deterministic auto-fix, the model can
// pick the right field and restructure, so it handles cases a string edit can't.
func (s *Server) handleStudioFixYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}
	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}

	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML error: "+err.Error())
	}
	draft := studio.FromAgentDefinition(def)

	// Gather every problem (graph + runtime + definition) to feed the model.
	var issues []string
	add := func(sev, node, msg string) {
		if line := issueLine(sev, node, msg); line != "" {
			issues = append(issues, line)
		}
	}
	if def.Workflow != nil && len(def.Workflow.Nodes) > 0 {
		vr := studio.Validate(draft)
		for _, e := range vr.Errors {
			add("ERROR", e.NodeID, e.Message)
		}
		for _, w := range vr.Warnings {
			add("WARNING", w.NodeID, w.Message)
		}
	}
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)
	pf := studio.Preflight(draft, s.preflightInput(c, cat))
	for _, b := range pf.Blockers {
		add("ERROR", b.NodeID, b.Message)
	}
	for _, w := range pf.Warnings {
		add("WARNING", w.NodeID, w.Message)
	}
	rep := agentvalidate.Definition(&def, "", s.agentValidationOptions(c.Context()), agentvalidate.Report{})
	for _, f := range rep.Findings {
		add(strings.ToUpper(string(f.Severity)), f.Field, f.Message)
	}
	if len(issues) == 0 {
		return c.JSON(fiber.Map{"yaml": req.YAML, "changed": false})
	}

	prompt := studio.BuildYAMLFixInstruction(req.YAML, issues, s.soulRules())
	raw, err := model.Complete(c.Context(), prompt)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	fixed := studio.CleanYAMLOutput(raw)
	if strings.TrimSpace(fixed) == "" {
		return s.errMsg(c, fiber.StatusInternalServerError, "the model did not return a corrected file")
	}
	// Make sure the model returned parseable YAML before handing it back.
	var check agent.Definition
	if err := yaml.Unmarshal([]byte(fixed), &check); err != nil {
		return s.errMsg(c, fiber.StatusUnprocessableEntity, "the AI returned invalid YAML; please fix manually or try again")
	}

	// Deterministic safety net on the AI's output: a weaker model's rewrite must
	// not be allowed to REINTRODUCE the bugs the deterministic layers guarantee
	// against. Re-run wiring repair + template-ref heal on the corrected flow
	// before returning, so Fix-with-AI can only ever improve correctness.
	if check.Workflow != nil && len(check.Workflow.Nodes) > 0 {
		d := studio.Draft{Flow: studio.Flow{
			Nodes:  check.Workflow.Nodes,
			Edges:  check.Workflow.Edges,
			Entry:  check.Workflow.Entry,
			Output: check.Workflow.Output,
		}}
		cat := s.studioCatalogSnapshot()
		s.groundCatalog(&cat)
		studio.RepairWiring(&d, cat)
		studio.ApplyTemplateFixes(&d)
		check.Workflow.Nodes = d.Flow.Nodes
		check.Workflow.Edges = d.Flow.Edges
		check.Workflow.Entry = d.Flow.Entry
		check.Workflow.Output = d.Flow.Output
		if out, merr := yaml.Marshal(&check); merr == nil {
			fixed = strings.TrimSpace(string(out))
		}
	}
	return c.JSON(fiber.Map{"yaml": fixed, "changed": fixed != req.YAML})
}

// autoRepairWorkflow runs ONE bounded LLM repair pass on a generated flow that
// still has blockers after deterministic repair, then deterministically heals
// the model's output before keeping it. It only replaces the flow GRAPH of the
// draft (nodes/edges/entry/output), preserving everything else (new_agents,
// knowledge, …). Best-effort: any failure leaves the draft unchanged.
func (s *Server) autoRepairWorkflow(ctx context.Context, draft *studio.Draft, blockers []studio.PreflightIssue, cat studio.Catalog) {
	model := s.studioLLM()
	if model == nil || draft == nil || len(draft.Flow.Nodes) == 0 {
		return
	}
	def, err := studio.ToAgentDefinition(*draft, true)
	if err != nil {
		return
	}
	yamlBytes, err := yaml.Marshal(&def)
	if err != nil {
		return
	}
	issues := make([]string, 0, len(blockers))
	for _, b := range blockers {
		if line := issueLine("ERROR", b.NodeID, b.Message); line != "" {
			issues = append(issues, line)
		}
	}
	if len(issues) == 0 {
		return
	}
	prompt := studio.BuildYAMLFixInstruction(string(yamlBytes), issues, s.soulRules())
	raw, err := model.Complete(ctx, prompt)
	if err != nil {
		return
	}
	fixed := studio.CleanYAMLOutput(raw)
	if strings.TrimSpace(fixed) == "" {
		return
	}
	var def2 agent.Definition
	if err := yaml.Unmarshal([]byte(fixed), &def2); err != nil || def2.Workflow == nil || len(def2.Workflow.Nodes) == 0 {
		return
	}
	// Deterministically heal the model's output before trusting it.
	d := studio.Draft{Flow: studio.Flow{
		Nodes:  def2.Workflow.Nodes,
		Edges:  def2.Workflow.Edges,
		Entry:  def2.Workflow.Entry,
		Output: def2.Workflow.Output,
	}}
	studio.RepairWiring(&d, cat)
	studio.ApplyTemplateFixes(&d)
	draft.Flow.Nodes = d.Flow.Nodes
	draft.Flow.Edges = d.Flow.Edges
	draft.Flow.Entry = d.Flow.Entry
	draft.Flow.Output = d.Flow.Output
}

// handleStudioReviewYAML implements POST /api/v1/studio/review-yaml — the
// rules-grounded LLM review. It complements the deterministic validator: the
// model checks the YAML against the (editable) rulebook and reports judgment-call
// problems a linter can't (wrong field/id, broken logic). Returns findings as
// items shaped like the validator's so the GUI merges them into one panel.
func (s *Server) handleStudioReviewYAML(c *fiber.Ctx) error {
	var req studioFromYAMLRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.YAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML is empty")
	}
	model := s.studioLLM()
	if model == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "LLM router unavailable")
	}
	// Only review parseable YAML — syntax is the deterministic validator's job.
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(req.YAML), &def); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "YAML error: "+err.Error())
	}

	prompt := studio.BuildYAMLReviewInstruction(req.YAML, s.soulRules())
	raw, err := model.Complete(c.Context(), prompt)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	findings := studio.ParseReviewFindings(raw)
	items := make([]yamlValidateItem, 0, len(findings))
	for _, f := range findings {
		items = append(items, yamlValidateItem{
			Severity: f.Severity, Source: "ai", NodeID: f.NodeID, Message: f.Message, Fix: f.Fix,
		})
	}
	return c.JSON(fiber.Map{"items": items, "count": len(items)})
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
	// Timeout-aware defaults for local-model agents (Stories #23/#24): if this
	// agent will run on a LOCAL model, apply patient timeout/turn presets where
	// the draft didn't set them, so a slow local run isn't killed mid-thought.
	s.applyLocalPreset(&def)
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
					// Prefer the profile the draft carries; if it is missing or
					// thin, synthesize a complete, reusable persona from the node
					// so no helper agent is ever saved blank (sub-agent quality).
					na, ok := newAgentsMap[node.Agent]
					if !ok || na.Name == "" || na.Description == "" || strings.TrimSpace(na.SystemPrompt) == "" {
						synth := studio.SynthesizeAgent(node.Agent, node, def.Name)
						if na.Name == "" {
							na.Name = synth.Name
						}
						if na.Description == "" {
							na.Description = synth.Description
						}
						if strings.TrimSpace(na.SystemPrompt) == "" {
							na.SystemPrompt = synth.SystemPrompt
						}
					}
					stub := agent.Definition{
						ID:           node.Agent,
						Name:         na.Name,
						Description:  na.Description,
						SystemPrompt: na.SystemPrompt,
						Enabled:      true,
						MaxTurns:     15,
						Memory:       agent.MemoryPolicy{MaxTokens: 8000},
						LLM: agent.LLMConfig{
							Provider:    s.cfg.LLM.DefaultProvider,
							Temperature: 0.7,
						},
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
	// Studio can edit two shapes: a fixed-workflow agent (canvas) AND a
	// reasoning agent (ReAct/Plan-Execute — the agent-spec editor + SOUL.yaml).
	// Previously this gated on HasWorkflow alone, so every reasoning agent was
	// rejected with "no editable workflow" and couldn't be opened at all. Since
	// FromAgentDefinition now round-trips the agent form losslessly, let those
	// through too.
	strat := strings.ToLower(strings.TrimSpace(def.Reasoning.Strategy))
	isReasoningAgent := strat == "react" || strat == "plan_execute"
	// Studio-authored agents (studio_intent set) are openable even with an empty
	// graph — e.g. a 0-step build the user needs to inspect, fix, or switch to an
	// agent. Only truly external/library agents with nothing Studio can edit are
	// rejected.
	studioAuthored := strings.TrimSpace(def.StudioIntent) != ""
	if !studio.HasWorkflow(*def) && !isReasoningAgent && !studioAuthored {
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

// studioCompileGateRequest is the POST /api/v1/studio/compile-gate body: a
// plain-language connector gate + the flow vars available at that edge.
type studioCompileGateRequest struct {
	Phrase string   `json:"phrase"`
	Vars   []string `json:"vars,omitempty"`
}

// handleStudioCompileGate implements POST /api/v1/studio/compile-gate (Phase B):
// turn a plain-language connector condition into a validated flow predicate.
func (s *Server) handleStudioCompileGate(c *fiber.Ctx) error {
	var req studioCompileGateRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	pred, err := studio.CompileGate(c.Context(), s.studioLLM(), req.Phrase, req.Vars)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(fiber.Map{"predicate": pred})
}

// handleStudioCompileNode implements POST /api/v1/studio/compile-node (Phase C):
// compile ONE node from its plain-language intent into concrete config.
func (s *Server) handleStudioCompileNode(c *fiber.Ctx) error {
	var req studio.CompileNodeRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	// Ground in the live catalog when the caller didn't supply one.
	if len(req.Catalog.Tools) == 0 && len(req.Catalog.MCP) == 0 && len(req.Catalog.Agents) == 0 {
		req.Catalog = s.studioCatalogSnapshot()
	}
	node, err := studio.CompileNode(c.Context(), s.studioLLM(), req)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(fiber.Map{"node": node})
}

// handleStudioCompositeBlocks implements GET /api/v1/studio/composite-blocks —
// returns the coarse composite-block catalog (Phase 2): each block's id, name,
// summary, requirements, typed port contract, and the ready-to-drop python
// FlowNode that encapsulates its whole multi-step dance. The palette/canvas
// consumes this to offer one-click coarse blocks instead of hand-wired graphs.
func (s *Server) handleStudioCompositeBlocks(c *fiber.Ctx) error {
	blocks := studio.CompositeBlocks()
	out := make([]fiber.Map, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, fiber.Map{
			"id":           b.ID,
			"name":         b.Name,
			"summary":      b.Summary,
			"requirements": b.Requirements,
			"inputs":       b.Inputs,
			"outputs":      b.Outputs,
			// The materialised, drop-ready node (typed ports + inline code +
			// classifier-derived requires).
			"node": b.MaterializeNode(),
		})
	}
	return c.JSON(fiber.Map{"blocks": out})
}

// --- Studio draft library (Story S6.2) ---

// soulRulesPath is the workspace path of the editable SOUL.yaml rulebook:
// <workspace>/studio/soul-yaml-rules.md.
func (s *Server) soulRulesPath() (string, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return "", err
	}
	return filepath.Join(ws.Root, "studio", "soul-yaml-rules.md"), nil
}

// soulRules returns the effective rulebook: the user's saved copy if present and
// non-empty, otherwise the built-in default. Never errors — a missing file just
// falls back to the default so generation/validation/fix always have rules.
func (s *Server) soulRules() string {
	if path, err := s.soulRulesPath(); err == nil {
		if b, rerr := os.ReadFile(path); rerr == nil && strings.TrimSpace(string(b)) != "" {
			return string(b)
		}
	}
	return studio.DefaultSOULRules
}

// handleStudioGetRules implements GET /api/v1/studio/rules — returns the
// effective rulebook, whether it's the built-in default, and the default text
// (so the GUI can offer a "reset to default").
func (s *Server) handleStudioGetRules(c *fiber.Ctx) error {
	rules := s.soulRules()
	return c.JSON(fiber.Map{
		"rules":     rules,
		"isDefault": rules == studio.DefaultSOULRules,
		"default":   studio.DefaultSOULRules,
	})
}

// studioRulesRequest is the PUT /api/v1/studio/rules body.
type studioRulesRequest struct {
	Rules string `json:"rules"`
}

// handleStudioSaveRules implements PUT /api/v1/studio/rules — saves the edited
// rulebook to the workspace. An empty body resets to the built-in default by
// removing the saved copy.
func (s *Server) handleStudioSaveRules(c *fiber.Ctx) error {
	var req studioRulesRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	path, err := s.soulRulesPath()
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if strings.TrimSpace(req.Rules) == "" {
		_ = os.Remove(path) // reset to built-in default
		return c.JSON(fiber.Map{"ok": true, "isDefault": true, "rules": studio.DefaultSOULRules})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if err := os.WriteFile(path, []byte(req.Rules), 0o644); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"ok": true, "isDefault": false})
}

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
