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
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
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

	res, err := studio.RefinePrompt(c.Context(), model, req.Intent, req.Catalog)
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
	problems := []string{"At RUN TIME the agent failed with this error — change the workflow so it cannot happen again: " + strings.TrimSpace(req.Error)}
	repaired, changed := studio.RepairWithProblems(c.Context(), model, req.Workflow, problems, cat)
	studio.RepairWiring(&repaired, cat)
	pf := studio.Preflight(repaired, s.preflightInput(c, cat))
	return c.JSON(fiber.Map{"workflow": repaired, "changed": changed, "preflight": pf})
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

	// (1) Fill capability holes with generated glue code before the loop.
	var glueNotes []string
	if _, notes := studio.EnsureCapabilities(c.Context(), model, &req.Workflow, cat); len(notes) > 0 {
		glueNotes = notes
	}

	// (2) Synthesize self-tests so "it works" is checked, not assumed.
	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		intent = req.Workflow.Intent
	}
	tests := studio.SynthesizeTests(c.Context(), model, intent, req.Workflow, cat)

	// (3) Choose the verifier. Default: REAL execution via the engine.
	verify := true
	if req.Verify != nil {
		verify = *req.Verify
	}
	opts := studio.BuildOptions{In: in, Tests: tests}
	if verify {
		opts.Verifier = studio.RealRunVerifier{Runner: s.studioRealRunner()}
	}

	rep := studio.BuildUntilWorks(c.Context(), model, req.Workflow, cat, opts)

	final := studio.Preflight(rep.Workflow, in)
	return c.JSON(fiber.Map{
		"report":    rep,
		"preflight": final,
		"glue":      glueNotes,
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

	go func() {
		defer close(events)
		emit := func(kind, data string) {
			select {
			case events <- sse{event: kind, data: data}:
			default: // drop if the client is gone / buffer full
			}
		}
		// (1) Glue, then (2) self-tests — reported as their own steps.
		if _, notes := studio.EnsureCapabilities(ctx, model, &req.Workflow, cat); len(notes) > 0 {
			for _, nt := range notes {
				emit("event", jsonMsg("glue", "🧩 "+nt))
			}
		}
		intent := strings.TrimSpace(req.Intent)
		if intent == "" {
			intent = req.Workflow.Intent
		}
		emit("event", jsonMsg("tests", "Writing self-tests…"))
		tests := studio.SynthesizeTests(ctx, model, intent, req.Workflow, cat)

		verify := true
		if req.Verify != nil {
			verify = *req.Verify
		}
		opts := studio.BuildOptions{In: in, Tests: tests}
		if verify {
			opts.Verifier = studio.RealRunVerifier{Runner: s.studioRealRunner()}
		}
		opts.OnEvent = func(ev studio.BuildEvent) {
			b, _ := json.Marshal(ev)
			emit("event", string(b))
		}

		rep := studio.BuildUntilWorks(ctx, model, req.Workflow, cat, opts)
		final := studio.Preflight(rep.Workflow, in)
		done, _ := json.Marshal(fiber.Map{"report": rep, "preflight": final})
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

// studioDiagnoseRunRequest is the POST /api/v1/studio/diagnose-run body: a
// dead-letter entry id to diagnose and self-heal.
type studioDiagnoseRunRequest struct {
	ID string `json:"id"`
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
		In:       in,
		Verifier: studio.RealRunVerifier{Runner: s.studioRealRunner()},
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

	res, err := studio.Compile(c.Context(), model, req.Intent, req.Catalog, req.Answers)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
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
