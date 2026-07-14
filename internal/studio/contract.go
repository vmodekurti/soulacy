package studio

import (
	"fmt"
	"strings"
)

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
func AssessContract(draft Draft, cat Catalog, in PreflightInput) ContractResult {
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
		pass("graph.integrity", "Graph integrity", "Reasoning-agent draft has no fixed graph to compile; tool allowlists are checked by runtime preflight.")
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

	assessAuthoringRules(draft, add, pass)
	res.OK = res.Blockers == 0
	res.Score = contractScore(res.Blockers, res.Warnings)
	res.Summary = contractSummary(res)
	return res
}

func assessAuthoringRules(draft Draft, add func(id, title, status, node, msg, fix string), pass func(id, title, msg string)) {
	if draft.IsAgent() {
		pass("architecture.fit", "Architecture fit", "This draft is a reasoning agent, so Studio will not force it into a brittle fixed workflow graph.")
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
