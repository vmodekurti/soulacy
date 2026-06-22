package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// agentSpecPayload is the JSON the model returns for a ReAct/Plan-Execute agent
// (local-first pivot). Unlike a workflow it carries NO flow graph — the engine's
// reasoning loop drives the listed tools/skills/peers dynamically.
type agentSpecPayload struct {
	Name         string     `json:"name"`
	SystemPrompt string     `json:"system_prompt"`
	Trigger      Trigger    `json:"trigger"`
	Channels     []string   `json:"channels"`
	Tools        []string   `json:"tools"`     // EXACT builtin + mcp__ tool names
	Skills       []string   `json:"skills"`    // installed skill names to enable
	Knowledge    []string   `json:"knowledge"` // KB names to attach
	NewAgents    []NewAgent `json:"new_agents"`
	Rationale    string     `json:"rationale"`
}

// BuildAgentPrompt builds the instruction for generating a ReAct/Plan-Execute
// AGENT (not a workflow). The model picks tools from the grounded catalog and
// writes a system prompt that teaches the agent how to accomplish the task by
// reasoning over those tools — including loops and async polling that a fixed
// graph can't express.
func BuildAgentPrompt(intent string, catalog Catalog, strategy string, answers map[string]string) string {
	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio agent designer. Turn the user's intent into a ")
	if strings.EqualFold(strategy, "plan_execute") {
		sb.WriteString("PLAN-EXECUTE")
	} else {
		sb.WriteString("ReAct (reasoning-loop)")
	}
	sb.WriteString(" agent — NOT a fixed workflow. The agent reasons step by step and calls tools dynamically, so it can loop over lists and poll asynchronous jobs until they finish (things a frozen graph cannot do).\n\n")

	sb.WriteString("Output RULES:\n")
	sb.WriteString("- Respond with ONLY a single JSON object. No prose, no markdown, no code fences.\n")
	sb.WriteString("- Shape EXACTLY:\n")
	sb.WriteString(`{
  "name": "<short human name>",
  "system_prompt": "<rich instructions: the agent's role, the goal, the ordered approach in plain language (incl. looping over each item and polling async jobs until ready), the exact tools to use and when, edge-case/error handling, and the final output format>",
  "trigger": { "type": "schedule|channel|webhook|manual", "config": { "cron": "0 7 * * *" } },
  "channels": ["telegram"],
  "tools": ["<EXACT builtin or mcp__server__tool names from the catalog the agent may call>"],
  "skills": ["<EXACT installed skill names to enable, if any>"],
  "knowledge": ["<EXACT KB names to attach, if relevant>"],
  "new_agents": [ { "id":"...", "name":"...", "description":"...", "system_prompt":"..." } ],
  "rationale": "<1-2 sentences on why a reasoning agent fits this task>"
}` + "\n\n")

	sb.WriteString("Guidance:\n")
	sb.WriteString("- DO NOT emit a flow/graph. The agent is driven by its system_prompt + tools, not nodes/edges.\n")
	sb.WriteString("- tools MUST be EXACT names from the catalog below (builtins like web_search, or full mcp__server__tool names). Never invent tool names; if a capability is missing, describe it in the prompt and pick the closest real tool.\n")
	sb.WriteString("- The system_prompt is where the procedure lives: spell out the steps as INSTRUCTIONS (e.g. \"create the notebook, then add EACH source one at a time, then start audio generation, then POLL the status until it reports ready, then deliver the link\").\n")
	sb.WriteString("- Include authentication/setup steps the user asked for as the FIRST instruction if a matching tool exists (e.g. refresh/login tools).\n")
	sb.WriteString("- Invent a peer agent ONLY if needed, and give it a full reusable system_prompt in new_agents.\n")
	sb.WriteString("- Pull concrete values from the user's words (queries, counts, schedule cadence, target channel).\n\n")

	writeCatalogGrounding(&sb, catalog)
	writePatternGrounding(&sb, intent, catalog)

	if len(answers) > 0 {
		sb.WriteString("\nThe user already answered these clarifying questions — honor them:\n")
		for _, k := range sortedKeys(answers) {
			if v := strings.TrimSpace(answers[k]); v != "" {
				sb.WriteString("- ")
				sb.WriteString(k)
				sb.WriteString(": ")
				sb.WriteString(v)
				sb.WriteString("\n")
			}
		}
	}

	sb.WriteString("\nIntent:\n")
	sb.WriteString(intent)
	sb.WriteString("\n")
	return sb.String()
}

// CompileAgent generates a ReAct/Plan-Execute agent Draft (no flow) from an
// intent. strategy is "react" (default) or "plan_execute". Like Compile it is
// tolerant of fenced/prose-wrapped model output; it validates that the result
// has a system prompt and at least one tool or peer agent.
func CompileAgent(ctx context.Context, llm LLM, intent string, catalog Catalog, strategy string, answers map[string]string) (Result, error) {
	if strings.TrimSpace(intent) == "" {
		return Result{}, fmt.Errorf("studio: intent is required")
	}
	if llm == nil {
		return Result{}, fmt.Errorf("studio: no LLM configured")
	}
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy != "plan_execute" {
		strategy = "react"
	}

	raw, err := llm.Complete(ctx, BuildAgentPrompt(intent, catalog, strategy, answers))
	if err != nil {
		return Result{}, fmt.Errorf("studio: llm complete: %w", err)
	}
	payload, perr := parseAgentSpec(raw)
	if perr != nil {
		return Result{}, perr
	}

	draft := Draft{
		Name:         strings.TrimSpace(payload.Name),
		SystemPrompt: strings.TrimSpace(payload.SystemPrompt),
		Intent:       intent,
		Trigger:      payload.Trigger,
		Channels:     trimStrings(payload.Channels),
		Strategy:     strategy,
		Tools:        trimStrings(payload.Tools),
		Skills:       trimStrings(payload.Skills),
		Knowledge:    trimStrings(payload.Knowledge),
		NewAgents:    payload.NewAgents,
		Recommendation: &Recommendation{
			Mode:      strategy,
			Rationale: strings.TrimSpace(payload.Rationale),
		},
	}
	if draft.Name == "" {
		draft.Name = "Studio Agent"
	}
	normalizeTrigger(&draft, intent)
	// Guarantee every referenced/invented peer agent has a full reusable profile.
	ensureNewAgents(&draft, catalog)

	// Hard contract: a usable agent needs a system prompt AND at least one tool
	// or peer to act with.
	if draft.SystemPrompt == "" {
		return Result{}, fmt.Errorf("studio: agent spec has no system prompt")
	}
	if len(draft.Tools) == 0 && len(draft.NewAgents) == 0 {
		return Result{}, fmt.Errorf("studio: agent spec lists no tools or agents to act with")
	}

	explanation := ExplainDraft(draft)
	notes := []string{"Generated a " + recoLabelGo(strategy) + " agent — it reasons over its tools rather than running a fixed graph."}
	if sk := draft.Skills; len(sk) > 0 {
		notes = append(notes, "Enables skill(s): "+strings.Join(sk, ", ")+".")
	}
	return Result{
		Workflow:    draft,
		Notes:       notes,
		Suggestions: suggestMissingAgent(draft, catalog),
		Explanation: &explanation,
	}, nil
}

// recoLabelGo is a tiny server-side label for the strategy.
func recoLabelGo(strategy string) string {
	if strings.EqualFold(strategy, "plan_execute") {
		return "Plan-Execute"
	}
	return "ReAct"
}

// parseAgentSpec tolerantly extracts the agent JSON (fence/prose tolerant).
func parseAgentSpec(raw string) (agentSpecPayload, error) {
	s := stripFences(strings.TrimSpace(raw))
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return agentSpecPayload{}, fmt.Errorf("studio: no JSON object found in agent spec output")
	}
	var p agentSpecPayload
	if err := json.Unmarshal([]byte(s[start:end+1]), &p); err != nil {
		return agentSpecPayload{}, fmt.Errorf("studio: parse agent spec: %w", err)
	}
	return p, nil
}

// suggestMissingAgent flags tools/skills/KBs the agent references that aren't in
// the catalog, reusing the MCP-aware tool check.
func suggestMissingAgent(draft Draft, cat Catalog) []Suggestion {
	if len(cat.Tools) == 0 && len(cat.MCP) == 0 {
		return nil
	}
	toolSet := lowerSet(cat.Tools)
	mcpSet := map[string]bool{}
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			if n := strings.TrimSpace(t.Name); n != "" {
				mcpSet[strings.ToLower(n)] = true
			}
		}
	}
	var out []Suggestion
	for _, t := range draft.Tools {
		key := strings.ToLower(strings.TrimSpace(t))
		if key == "" {
			continue
		}
		installed := toolSet[key]
		if !installed && strings.HasPrefix(key, "mcp__") {
			installed = mcpSet[key]
		}
		if !installed {
			out = append(out, Suggestion{Kind: "tool", Name: t, Installed: false, Reason: capabilityReason("tool", t, false)})
		}
	}
	return out
}
