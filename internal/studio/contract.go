package studio

import (
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

// ContractOption tunes AssessContract without breaking backward callers. Story
// 2b (Cohort C): security- and persona-scoped checks need the fuller
// agent.Definition (Draft doesn't carry Policy / NonNegotiables / Builtins);
// callers that have the Definition on hand pass it via WithAgentDefinition so
// the enriched checks run.
type ContractOption func(*contractOpts)

type contractOpts struct {
	def *agent.Definition
}

// WithAgentDefinition supplies the source agent.Definition so contract checks
// that depend on fields not present on Draft (security, persona, builtins)
// can run. Callers that don't have a Definition (e.g. the pure Studio build
// loop) leave this off — those checks then quietly skip.
func WithAgentDefinition(def *agent.Definition) ContractOption {
	return func(o *contractOpts) { o.def = def }
}

// ContractResult is Studio's platform-wide generation contract. It consolidates
// graph compile checks, runtime preflight checks, and authoring-rule hygiene into
// one deterministic report so every generated workflow can be judged the same
// way before save, build, or repair.
type ContractResult struct {
	OK       bool            `json:"ok"`
	Score    int             `json:"score"`
	Blockers int             `json:"blockers"`
	Warnings int             `json:"warnings"`
	Checks   []ContractCheck `json:"checks"`
	Summary  string          `json:"summary"`
}

// ContractCheck is one rule verdict. Status is "pass", "warn", or "block".
type ContractCheck struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	NodeID  string `json:"nodeId,omitempty"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

// AssessContract runs the Studio generation contract over a draft. It is pure
// and LLM-free; callers provide the same live-state input used by Preflight.
// Options (see ContractOption) supply extra context — currently the source
// agent.Definition for security/persona/builtin checks (Story 2b).
func AssessContract(draft Draft, cat Catalog, in PreflightInput, options ...ContractOption) ContractResult {
	opts := contractOpts{}
	for _, opt := range options {
		if opt != nil {
			opt(&opts)
		}
	}
	if in.Catalog.Tools == nil && in.Catalog.MCP == nil && in.Catalog.Agents == nil {
		in.Catalog = cat
	}
	var res ContractResult
	add := func(id, title, status, node, msg, fix string) {
		res.Checks = append(res.Checks, ContractCheck{
			ID: id, Title: title, Status: status, NodeID: node, Message: msg, Fix: fix,
		})
		switch status {
		case "block":
			res.Blockers++
		case "warn":
			res.Warnings++
		}
	}
	pass := func(id, title, msg string) { add(id, title, "pass", "", msg, "") }

	if draft.IsAgent() {
		pass("agent.shape", "Reasoning-agent shape", "Reasoning-agent draft has no fixed graph to compile; contract checks against system prompt, tool allowlists, peer graph, and step budget run below.")
	} else {
		vr := Validate(draft)
		if vr.Ok {
			pass("graph.integrity", "Graph integrity", "The workflow graph compiles: node ids, entry, edges, ports, and output contracts are coherent.")
		} else {
			for _, e := range vr.Errors {
				add("graph.integrity", "Graph integrity", "block", e.NodeID, e.Message, "Fix the broken graph structure before saving or running.")
			}
		}
		for _, w := range vr.Warnings {
			add("graph.warning", "Graph warning", "warn", w.NodeID, w.Message, "Review this warning; generated workflows should not rely on ambiguous graph shape.")
		}
	}

	pf := Preflight(draft, in)
	if pf.OK {
		pass("runtime.preflight", "Runtime readiness", "All required runtime setup, tool arguments, templates, data-flow references, and delivery channels passed preflight.")
	} else {
		for _, b := range pf.Blockers {
			add("runtime."+nonEmpty(b.Kind, "preflight"), "Runtime readiness", "block", b.NodeID, b.Message, b.Fix)
		}
	}
	for _, w := range pf.Warnings {
		add("runtime."+nonEmpty(w.Kind, "warning"), "Runtime warning", "warn", w.NodeID, w.Message, w.Fix)
	}

	assessAuthoringRules(draft, opts, add, pass)
	res.OK = res.Blockers == 0
	res.Score = contractScore(res.Blockers, res.Warnings)
	res.Summary = contractSummary(res)
	return res
}

func assessAuthoringRules(draft Draft, opts contractOpts, add func(id, title, status, node, msg, fix string), pass func(id, title, msg string)) {
	if draft.IsAgent() {
		pass("architecture.fit", "Architecture fit", "This draft is a reasoning agent, so Studio will not force it into a brittle fixed workflow graph.")
		assessReasoningAgentRules(draft, opts, add, pass)
		assessCompletionContractRules(draft, add, pass)
		return
	}

	nodeCount := len(draft.Flow.Nodes)
	switch {
	case nodeCount == 0:
		add("architecture.empty", "Architecture fit", "block", "", "This workflow has no runnable steps.", "Add at least one tool, Python, LLM, or agent step.")
	case nodeCount <= 5:
		pass("architecture.size", "Macro-workflow size", fmt.Sprintf("The workflow has %d node(s), which fits the simple high-level Macro-Workflow guideline.", nodeCount))
	case nodeCount <= 8:
		add("architecture.size", "Macro-workflow size", "warn", "", fmt.Sprintf("This workflow has %d nodes. Studio workflows should usually stay at 3-5 high-level steps.", nodeCount), "Combine extraction/formatting/filtering into one Python or LLM Extract block, or switch to an agent if runtime tool choice is needed.")
	default:
		add("architecture.size", "Macro-workflow size", "block", "", fmt.Sprintf("This workflow has %d nodes and is likely too brittle for a visual Macro-Workflow.", nodeCount), "Collapse low-level steps or switch to a ReAct/Auto agent that can choose tools dynamically.")
	}

	if risky := freeformHandoffWarnings(draft); len(risky) == 0 {
		pass("data.contracts", "Data contracts", "No obvious free-form agent/LLM output is wired directly into a structured tool call.")
	} else {
		for _, r := range risky {
			add("data.contracts", "Data contracts", "warn", r.nodeID, r.message, "Insert an LLM Extract or Python Transform node, or pass structured values with typed ports / {{ toJson .var }}.")
		}
	}

	if bad := thinNewAgents(draft); len(bad) == 0 {
		pass("agents.prompts", "Helper-agent prompts", "Helper agents are either absent or have enough prompt detail to run independently.")
	} else {
		for _, a := range bad {
			add("agents.prompts", "Helper-agent prompts", "warn", a, "Helper agent \""+a+"\" has a very short or missing system prompt.", "Give each helper agent a self-contained role, constraints, available inputs, and expected output format.")
		}
	}
	assessCompletionContractRules(draft, add, pass)
}

func assessCompletionContractRules(draft Draft, add func(id, title, status, node, msg, fix string), pass func(id, title, msg string)) {
	errs, warns := completionContractValidateIssues(draft)
	if len(errs) == 0 && len(warns) == 0 {
		if requiresCompletionContract(draft) {
			pass("completion.contract", "Completion contract", "The draft has an explicit done-condition contract for multi-step work.")
		}
		return
	}
	for _, e := range errs {
		add("completion.contract", "Completion contract", "block", e.NodeID, e.Message, "Add the missing operation(s), set a real output route, or switch to an Auto reasoning agent for adaptive multi-step work.")
	}
	for _, w := range warns {
		add("completion.contract", "Completion contract", "warn", w.NodeID, w.Message, "Add a completion contract and/or configure the missing output/storage route.")
	}
}

type handoffWarning struct {
	nodeID  string
	message string
}

func freeformHandoffWarnings(draft Draft) []handoffWarning {
	byID := map[string]struct {
		kind, tool, input string
	}{}
	for _, n := range draft.Flow.Nodes {
		byID[n.ID] = struct {
			kind, tool, input string
		}{kind: strings.ToLower(strings.TrimSpace(n.Kind)), tool: strings.TrimSpace(n.Tool), input: strings.TrimSpace(n.Input)}
	}
	var out []handoffWarning
	for _, e := range draft.Flow.Edges {
		src, ok1 := byID[e.From]
		dst, ok2 := byID[e.To]
		if !ok1 || !ok2 {
			continue
		}
		if dst.kind != "tool" || strings.TrimSpace(e.ToPort) != "" {
			continue
		}
		if src.kind != "agent" && src.kind != "llm" {
			continue
		}
		if strings.Contains(dst.input, "toJson") || strings.Contains(dst.input, "{{") && strings.HasPrefix(dst.input, "{") {
			continue
		}
		out = append(out, handoffWarning{
			nodeID:  e.To,
			message: "Step \"" + e.To + "\" receives free-form " + src.kind + " output before calling structured tool \"" + dst.tool + "\".",
		})
	}
	return out
}

func thinNewAgents(draft Draft) []string {
	var out []string
	for _, a := range draft.NewAgents {
		body := strings.TrimSpace(a.SystemPrompt)
		if len(strings.Fields(body)) < 18 {
			id := strings.TrimSpace(a.ID)
			if id == "" {
				id = strings.TrimSpace(a.Name)
			}
			if id == "" {
				id = "unnamed"
			}
			out = append(out, id)
		}
	}
	return out
}

func contractScore(blockers, warnings int) int {
	score := 100 - blockers*25 - warnings*5
	if score < 0 {
		return 0
	}
	return score
}

func contractSummary(r ContractResult) string {
	if r.Blockers == 0 && r.Warnings == 0 {
		return "Studio contract passed cleanly."
	}
	if r.Blockers == 0 {
		return fmt.Sprintf("Studio contract passed with %d warning(s).", r.Warnings)
	}
	return fmt.Sprintf("Studio contract blocked by %d issue(s), with %d warning(s).", r.Blockers, r.Warnings)
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimSpace(s)
}

// assessReasoningAgentRules runs the reasoning-agent-specific contract checks
// that used to be skipped entirely (contract.go treated `Draft.IsAgent()` as a
// blanket pass). Story 2 (Cohort B): the contract is now platform-wide, so
// react / plan_execute / auto agents get validated on the surfaces that make
// their runtime succeed — system prompt richness, tool allowlist sanity, peer
// graph coherence, step-budget realism, prompt hygiene, and channel/delivery
// wiring. Preflight still enforces the runtime blockers (missing tool, MCP
// disconnected, etc.); this function promotes the shape-level concerns into
// the scored contract view so operators see them before Save + Build.
//
// Checks in this first slice (Story 2 MVP):
//   - agent.system_prompt   — block on empty, warn on <40 words
//   - agent.tool_allowlist  — warn/block when a react/plan_execute agent has nothing to act on
//   - agent.peer_graph      — dangling agent__<id> references, thin peer prompts
//   - agent.prompt_hygiene  — SystemPrompt cites tools/skills/peers not in the allowlist
//   - agent.step_budget     — MaxTurns/StepTimeout/TotalTimeout realism
//   - agent.channel_delivery — channel.send in tools but no Channels configured
//
// The llm_fit / capability_scope / persona_consistency / builtin_scope checks
// from the design memo are deferred to a follow-up so this first slice ships
// without importing internal/agentvalidate primitives.
func assessReasoningAgentRules(draft Draft, opts contractOpts, add func(id, title, status, node, msg, fix string), pass func(id, title, msg string)) {
	prompt := strings.TrimSpace(draft.SystemPrompt)
	promptWords := len(strings.Fields(prompt))
	strategy := strings.ToLower(strings.TrimSpace(draft.Strategy))

	// 1. system_prompt — for a reasoning agent the prompt IS the agent spec.
	switch {
	case prompt == "":
		add("agent.system_prompt", "System prompt", "block", "",
			"This reasoning agent has an empty system prompt, so there is nothing to guide it at runtime.",
			"Write a system prompt covering the agent's role, allowed tools, output format, and any constraints.")
	case promptWords < 40:
		add("agent.system_prompt", "System prompt", "warn", "",
			fmt.Sprintf("System prompt is only %d word(s). Reasoning agents need a richer spec because the prompt replaces the graph.", promptWords),
			"Extend the prompt to cover role, allowed tools, output constraints, and refusal/safety rules; aim for 40+ words.")
	default:
		pass("agent.system_prompt", "System prompt", fmt.Sprintf("System prompt is %d words — enough for a reasoning agent to act on.", promptWords))
	}

	// 2. tool_allowlist — nothing to act on = a loop with no exit.
	hasTools := len(draft.Tools) > 0
	hasPeers := len(draft.NewAgents) > 0
	hasSkills := len(draft.Skills) > 0
	hasKB := len(draft.Knowledge) > 0
	switch {
	case !hasTools && !hasPeers && !hasSkills && !hasKB && strategy == "react":
		add("agent.tool_allowlist", "Tool allowlist", "block", "",
			"A ReAct agent has no tools, peer agents, skills, or knowledge bases to act on — the loop cannot make progress.",
			"Add at least one entry to Tools, NewAgents, Skills, or Knowledge before saving.")
	case !hasTools && !hasPeers && !hasSkills && !hasKB:
		add("agent.tool_allowlist", "Tool allowlist", "warn", "",
			"This agent has no tools, peers, skills, or knowledge bases; runtime behaviour is limited to plain-LLM responses.",
			"Wire the agent to at least one tool or peer if it needs to take action.")
	default:
		pass("agent.tool_allowlist", "Tool allowlist", "The agent has at least one tool, peer, skill, or knowledge base to act on.")
	}

	// 3. peer_graph — extend thin-prompt to reasoning-agent peers, and flag
	// dangling agent__<id> references from the system prompt.
	if thin := thinNewAgents(draft); len(thin) > 0 {
		for _, id := range thin {
			add("agent.peer_graph", "Peer agents", "warn", id,
				"Peer agent \""+id+"\" has a very short or missing system prompt.",
				"Give each peer a self-contained role, tools it may use, and expected output.")
		}
	} else if hasPeers {
		pass("agent.peer_graph", "Peer agents", "Every peer agent has enough prompt detail to run independently.")
	}
	dangling := danglingPeerRefs(draft)
	for _, ref := range dangling {
		add("agent.peer_graph", "Peer agents", "warn", "",
			"System prompt references peer \""+ref+"\" but no such agent is declared in NewAgents.",
			"Either add the peer to NewAgents, remove the reference, or match the name exactly.")
	}

	// 4. prompt_hygiene — flag references to tools that aren't in the allowlist.
	if len(prompt) > 0 && hasTools {
		toolSet := map[string]bool{}
		for _, t := range draft.Tools {
			toolSet[strings.ToLower(strings.TrimSpace(t))] = true
		}
		promptLower := strings.ToLower(prompt)
		// Look at obvious `tool` / "use X" citations; conservative match — only
		// flag when the tool name appears near an action verb so casual mentions
		// don't fire.
		for _, verb := range []string{"use `", "call `", "invoke `"} {
			idx := 0
			for {
				pos := strings.Index(promptLower[idx:], verb)
				if pos < 0 {
					break
				}
				start := idx + pos + len(verb)
				end := strings.Index(promptLower[start:], "`")
				if end < 0 {
					break
				}
				name := promptLower[start : start+end]
				idx = start + end + 1
				if name == "" || toolSet[name] {
					continue
				}
				// Skip citations that map to a known peer, skill, or KB.
				if hasPeers {
					peerHit := false
					for _, p := range draft.NewAgents {
						if strings.EqualFold(strings.TrimSpace(p.ID), name) || strings.EqualFold(strings.TrimSpace(p.Name), name) {
							peerHit = true
							break
						}
					}
					if peerHit {
						continue
					}
				}
				add("agent.prompt_hygiene", "Prompt hygiene", "warn", "",
					"System prompt tells the agent to use tool \""+name+"\" but it is not in the allowlist.",
					"Add \""+name+"\" to Tools, or reword the prompt to reference an allowed tool.")
			}
		}
	}

	// 5. step_budget — realism on the reasoning loop caps.
	stepTimeoutDur := parseContractDuration(draft.StepTimeout)
	totalTimeoutDur := parseContractDuration(draft.TotalTimeout)
	runTimeoutDur := parseContractDuration(draft.RunTimeout)
	if strings.TrimSpace(draft.StepTimeout) != "" && stepTimeoutDur <= 0 {
		add("agent.step_budget", "Step budget", "warn", "",
			"step_timeout \""+draft.StepTimeout+"\" could not be parsed as a duration.",
			"Use Go duration syntax (e.g. \"30s\", \"2m\", \"1h30m\").")
	}
	if strings.TrimSpace(draft.TotalTimeout) != "" && totalTimeoutDur <= 0 {
		add("agent.step_budget", "Step budget", "warn", "",
			"total_timeout \""+draft.TotalTimeout+"\" could not be parsed as a duration.",
			"Use Go duration syntax (e.g. \"5m\", \"30m\", \"2h\").")
	}
	if strings.TrimSpace(draft.RunTimeout) != "" && runTimeoutDur <= 0 {
		add("agent.step_budget", "Step budget", "warn", "",
			"run_timeout \""+draft.RunTimeout+"\" could not be parsed as a duration.",
			"Use Go duration syntax (e.g. \"10m\", \"1h\").")
	}
	if strategy == "react" && draft.MaxTurns > 40 {
		add("agent.step_budget", "Step budget", "block", "",
			fmt.Sprintf("max_turns is %d — a ReAct loop this deep is nearly guaranteed to blow the token budget or spin.", draft.MaxTurns),
			"Lower max_turns to ≤ 20 (Studio's sensible default is 15).")
	}
	if strategy == "react" && draft.MaxTurns == 0 && totalTimeoutDur <= 0 && runTimeoutDur <= 0 {
		add("agent.step_budget", "Step budget", "warn", "",
			"ReAct loop has no max_turns and no total_timeout / run_timeout — a stuck loop cannot self-terminate.",
			"Set at least one of max_turns, total_timeout, or run_timeout.")
	}
	if stepTimeoutDur > 0 && totalTimeoutDur > 0 && draft.MaxTurns > 0 {
		if totalTimeoutDur < time.Duration(draft.MaxTurns)*stepTimeoutDur {
			add("agent.step_budget", "Step budget", "warn", "",
				fmt.Sprintf("total_timeout (%s) is less than max_turns × step_timeout (%d × %s); the agent cannot complete a full loop.",
					totalTimeoutDur, draft.MaxTurns, stepTimeoutDur),
				"Either raise total_timeout, or lower max_turns / step_timeout so the arithmetic fits.")
		}
	}

	// 6. channel_delivery — channel.send used but Channels not declared.
	if hasTools {
		usesChannelSend := false
		for _, t := range draft.Tools {
			if strings.EqualFold(strings.TrimSpace(t), "channel.send") {
				usesChannelSend = true
				break
			}
		}
		if usesChannelSend && len(draft.Channels) == 0 {
			add("agent.channel_delivery", "Channel delivery", "warn", "",
				"Agent has channel.send in its tool allowlist but declares no Channels — it will guess a route at runtime.",
				"Add the target channel id(s) to Channels so channel.send has an explicit default.")
		}
	}

	// 7. llm_fit — surface model choices that are known to trip a reasoning loop.
	// Runs on Draft alone; opts.def not required.
	assessAgentLLMFit(draft, add)

	// 8-10. Security / persona / builtin scope — need the fuller Definition
	// (Draft doesn't round-trip Policy / NonNegotiables / Builtins). When
	// callers passed WithAgentDefinition, these run; otherwise they skip.
	if opts.def != nil {
		assessAgentCapabilityScope(draft, opts.def, add)
		assessAgentPersonaConsistency(opts.def, add)
		assessAgentBuiltinScope(draft, opts.def, add)
	}
}

// assessAgentLLMFit turns the "wrong model for the job" heuristics from
// internal/agentvalidate into contract-scored checks. Model-only, no Definition
// needed.
func assessAgentLLMFit(draft Draft, add func(id, title, status, node, msg, fix string)) {
	provider := strings.ToLower(strings.TrimSpace(draft.LLM.Provider))
	model := strings.TrimSpace(draft.LLM.Model)
	strategy := strings.ToLower(strings.TrimSpace(draft.Strategy))
	if model == "" {
		return
	}
	if isEmbeddingModel(model) {
		add("agent.llm_fit", "Model fit", "block", "",
			"\""+model+"\" is an embedding model — it cannot drive a reasoning loop.",
			"Pick a chat/instruct model in the provider dropdown. Suggestions: "+strings.Join(reasoningModelSuggestions(provider), ", ")+".")
		return
	}
	if weakJSONModel(provider, model) {
		add("agent.llm_fit", "Model fit", "warn", "",
			"\""+model+"\" is known to produce unreliable JSON tool calls in a reasoning loop.",
			"Consider a stronger model. Suggestions: "+strings.Join(reasoningModelSuggestions(provider), ", ")+".")
	}
	if smallContextModel(provider, model) {
		steps := draft.MaxTurns
		if steps <= 4 {
			// heuristic ceiling — the default agent max_steps sits around 4-8
			// depending on strategy. Threshold intentionally matches
			// agentvalidate.assessReasoningLLM.
		}
		if draft.MaxTurns > 4 || (strategy == "plan_execute" && draft.MaxTurns == 0) {
			add("agent.llm_fit", "Model fit", "warn", "",
				"\""+model+"\" has a small context window; a reasoning loop with >4 turns tends to overflow it.",
				"Lower max_turns to ≤ 4, or pick a larger-context model.")
		}
	}
	if provider == "groq" && draft.MaxTurns > 4 {
		add("agent.llm_fit", "Model fit", "warn", "",
			fmt.Sprintf("Groq's free tier throttles TPM aggressively; %d turns per run risks 429s mid-loop.", draft.MaxTurns),
			"Lower max_turns to ≤ 4, or switch to a paid Groq tier / another provider.")
	}
	if len(draft.LLM.AllowedProviders) > 0 {
		allowed := false
		for _, ap := range draft.LLM.AllowedProviders {
			if strings.EqualFold(strings.TrimSpace(ap), provider) {
				allowed = true
				break
			}
		}
		if !allowed && provider != "" {
			add("agent.llm_fit", "Model fit", "block", "",
				"Provider \""+provider+"\" is not in the agent's allowed_providers list ("+strings.Join(draft.LLM.AllowedProviders, ", ")+").",
				"Either add \""+provider+"\" to allowed_providers, or switch the agent's provider dropdown to one that is already allowed.")
		}
	}
}

// assessAgentCapabilityScope warns when a privileged / shell-capable agent
// runs unattended on a cron trigger (no human approver on failure) or when
// the tool policy is wide open with no allow-list narrowing.
func assessAgentCapabilityScope(draft Draft, def *agent.Definition, add func(id, title, status, node, msg, fix string)) {
	privileged := def.SystemTools || def.AllowShell || def.HasCapability("system")
	trigger := strings.ToLower(strings.TrimSpace(string(def.Trigger)))
	isScheduled := trigger == "cron"
	if def.Schedule != nil && strings.TrimSpace(def.Schedule.Cron) != "" {
		isScheduled = true
	}
	if privileged && isScheduled && !def.Unattended {
		add("agent.capability_scope", "Capability scope", "warn", "",
			"This agent has system-level capabilities and runs on a cron schedule, but is not marked Unattended — scheduled fires will silently stall waiting for a human approval that no one is there to give.",
			"Either drop the system capability (safer) or set Unattended and audit the confirm_tools list so scheduled runs proceed knowingly.")
	}
	pol := def.Policy
	polAllowShell := strings.EqualFold(strings.TrimSpace(pol.Shell), "allow")
	polAllowNetwork := strings.EqualFold(strings.TrimSpace(pol.Network), "allow")
	if polAllowShell && len(pol.DenyPaths) == 0 && !def.Unattended {
		add("agent.capability_scope", "Capability scope", "warn", "",
			"policy.shell = allow with no deny_paths — every shell tool call is unfiltered.",
			"Narrow with policy.shell: prompt, or add specific deny_paths / a confirm_tools list.")
	}
	if polAllowNetwork && len(pol.AllowDomains) == 0 {
		add("agent.capability_scope", "Capability scope", "warn", "",
			"policy.network = allow with no allow_domains — the agent can reach any host on the internet.",
			"Set policy.network: prompt, or add an allow_domains list of the specific hosts the agent needs.")
	}
}

// assessAgentPersonaConsistency looks for contradictions between the operator's
// stated Non-Negotiables and the runtime configuration that would prevent them
// from being honoured. NonNegotiables lives at the top level of Definition
// (not under Persona) and is a nil-able pointer, as is OutputConstraints.
func assessAgentPersonaConsistency(def *agent.Definition, add func(id, title, status, node, msg, fix string)) {
	nn := def.NonNegotiables
	if nn == nil {
		return
	}
	if len(nn.MustNot) > 0 && strings.EqualFold(strings.TrimSpace(def.LLM.ToolChoice), "required") {
		add("agent.persona_consistency", "Persona consistency", "warn", "",
			"The agent has MustNot rules but LLM.tool_choice is \"required\" — the first turn is forced to call a tool even if a MustNot rule would refuse.",
			"Change tool_choice to \"auto\" so the agent can honour refusals, or drop the MustNot rules that conflict with mandatory tool use.")
	}
	if nn.OutputConstraints != nil {
		format := strings.ToLower(strings.TrimSpace(nn.OutputConstraints.Format))
		if format == "json" && strings.TrimSpace(def.LLM.ResponseFormat) == "" && len(def.LLM.OutputSchema) == 0 {
			add("agent.persona_consistency", "Persona consistency", "warn", "",
				"Non-Negotiables set output format to JSON, but LLM.response_format and output_schema are both empty — the constraint is aspirational, not enforced.",
				"Set LLM.response_format: json_object (or provide an output_schema) so the provider actually returns JSON.")
		}
	}
}

// assessAgentBuiltinScope catches misconfigurations where the operator opted
// out of every tool surface or lists builtins that need setup they haven't
// done (kb_search without any Knowledge, read_skill without any Skills).
func assessAgentBuiltinScope(draft Draft, def *agent.Definition, add func(id, title, status, node, msg, fix string)) {
	// Total opt-out: Builtins explicitly empty and no other surface.
	mcpToolsLen := 0
	if def.MCPTools != nil {
		mcpToolsLen = len(*def.MCPTools)
	}
	if def.Builtins != nil && len(*def.Builtins) == 0 &&
		mcpToolsLen == 0 && len(def.Skills) == 0 && len(def.Agents) == 0 && len(def.Knowledge) == 0 {
		add("agent.builtin_scope", "Builtin scope", "block", "",
			"The agent explicitly opts out of every builtin and has no MCP tools, skills, peer agents, or knowledge bases — there is nothing for a reasoning loop to call.",
			"Add at least one Builtin, MCPTool, Skill, peer Agent, or Knowledge base.")
		return
	}
	// Setup mismatches: a builtin listed without its dependencies.
	activeBuiltins := map[string]bool{}
	if def.Builtins != nil {
		for _, b := range *def.Builtins {
			activeBuiltins[strings.ToLower(strings.TrimSpace(b))] = true
		}
	}
	// If the *Draft* lists these tools it counts too (Draft.Tools is the
	// authoring-time allowlist, def.Builtins the compiled form).
	for _, t := range draft.Tools {
		activeBuiltins[strings.ToLower(strings.TrimSpace(t))] = true
	}
	if activeBuiltins["kb_search"] && len(def.Knowledge) == 0 {
		add("agent.builtin_scope", "Builtin scope", "warn", "",
			"kb_search is in the tool allowlist but no Knowledge bases are attached — every call will return \"no knowledge configured\".",
			"Attach at least one Knowledge base under agent.knowledge, or remove kb_search from the allowlist.")
	}
	if (activeBuiltins["read_skill"] || activeBuiltins["read_skill_file"]) && len(def.Skills) == 0 {
		add("agent.builtin_scope", "Builtin scope", "warn", "",
			"read_skill is in the tool allowlist but no Skills are attached — the agent has nothing to read.",
			"Attach at least one Skill under agent.skills, or remove read_skill from the allowlist.")
	}
	if activeBuiltins["kb_write"] && len(def.Knowledge) == 0 {
		add("agent.builtin_scope", "Builtin scope", "warn", "",
			"kb_write is in the tool allowlist but no Knowledge bases are attached — writes will have nowhere to land.",
			"Attach a Knowledge base or remove kb_write from the allowlist.")
	}
}

// danglingPeerRefs finds `agent__<id>` (or `agent:<id>`) mentions in the
// system prompt whose target is not declared in NewAgents. Case-insensitive,
// conservative — only flags explicit tool-name references, not casual peer
// mentions.
func danglingPeerRefs(draft Draft) []string {
	prompt := strings.ToLower(draft.SystemPrompt)
	if prompt == "" {
		return nil
	}
	known := map[string]bool{}
	for _, p := range draft.NewAgents {
		id := strings.ToLower(strings.TrimSpace(p.ID))
		if id != "" {
			known[id] = true
		}
	}
	var out []string
	// Scan for `agent__` prefix (the canonical tool-name form) — capture the
	// following identifier chars.
	for _, prefix := range []string{"agent__", "agent:"} {
		idx := 0
		for {
			pos := strings.Index(prompt[idx:], prefix)
			if pos < 0 {
				break
			}
			start := idx + pos + len(prefix)
			// Consume identifier chars: [a-z0-9_-].
			end := start
			for end < len(prompt) {
				c := prompt[end]
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
					break
				}
				end++
			}
			if end > start {
				name := prompt[start:end]
				if !known[name] {
					out = append(out, name)
				}
			}
			idx = end
		}
	}
	return dedupeStrings(out)
}

// parseContractDuration returns 0 on empty or unparseable input so callers can
// treat "invalid" as "unset" without an extra bool.
func parseContractDuration(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// dedupeStrings is defined in buildloop.go and shared across the studio pkg.
