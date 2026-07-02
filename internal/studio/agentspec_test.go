package studio

import (
	"context"
	"strings"
	"testing"
)

func TestCompileAgent_ProducesReActDraft(t *testing.T) {
	out := `{
	  "name": "Daily AI Podcast",
	  "system_prompt": "You are an AI news producer. Create a NotebookLM notebook, add each source, generate audio, poll status until ready, then deliver.",
	  "trigger": {"type":"schedule","config":{"cron":"0 7 * * *"}},
	  "channels": ["telegram"],
	  "tools": ["web_search","mcp__notebooklm__create","mcp__notebooklm__audio"],
	  "skills": [],
	  "knowledge": [],
	  "rationale": "Async polling + per-item loop need a reasoning loop."
	}`
	res, err := CompileAgent(context.Background(), fakeLLM{out: out}, "daily ai podcast to telegram", Catalog{}, "react", nil)
	if err != nil {
		t.Fatalf("CompileAgent: %v", err)
	}
	d := res.Workflow
	if !d.IsAgent() || d.Strategy != "react" {
		t.Errorf("expected react agent, got strategy %q", d.Strategy)
	}
	if len(d.Flow.Nodes) != 0 {
		t.Errorf("agent draft must have NO flow nodes, got %d", len(d.Flow.Nodes))
	}
	if len(d.Tools) != 3 {
		t.Errorf("tools allowlist: %+v", d.Tools)
	}
}

func TestCompileAgent_PreservesAutoStrategy(t *testing.T) {
	out := `{
	  "name": "Weather Assistant",
	  "system_prompt": "Answer weather questions by selecting the right available weather tool and returning practical guidance.",
	  "trigger": {"type":"channel"},
	  "channels": ["http"],
	  "tools": ["mcp__weather__get_forecast"],
	  "skills": [],
	  "knowledge": [],
	  "rationale": "Ordinary runtime tool selection fits auto mode."
	}`
	res, err := CompileAgent(context.Background(), fakeLLM{out: out}, "interactive weather assistant", Catalog{
		Tools: []string{"mcp__weather__get_forecast"},
	}, "auto", nil)
	if err != nil {
		t.Fatalf("CompileAgent: %v", err)
	}
	if res.Workflow.Strategy != "auto" {
		t.Fatalf("strategy = %q, want auto", res.Workflow.Strategy)
	}
	if res.Workflow.Recommendation == nil || res.Workflow.Recommendation.Mode != "auto" {
		t.Fatalf("recommendation = %+v, want auto", res.Workflow.Recommendation)
	}
}

// End-to-end grounding through CompileAgent: a near-miss skill the model named is
// corrected to the installed one, an installed skill the intent clearly references
// but the model omitted is injected, and a named-but-uninstalled skill surfaces as
// a "Needs setup" suggestion rather than vanishing.
func TestCompileAgent_GroundsSkillsEndToEnd(t *testing.T) {
	out := `{
	  "name": "Finance QA",
	  "system_prompt": "Answer questions about stocks using the right finance skill.",
	  "trigger": {"type":"channel"},
	  "channels": ["http"],
	  "tools": ["web_search"],
	  "skills": ["yahoo finance", "totally-made-up-skill"],
	  "knowledge": [],
	  "rationale": "Dynamic skill routing."
	}`
	cat := Catalog{Skills: []CatalogSkill{
		{Name: "yfinance", Description: "Yahoo Finance market data: stock quotes, history"},
		{Name: "market-news", Description: "Latest market news headlines"},
	}}
	res, err := CompileAgent(context.Background(),
		fakeLLM{out: out},
		"on-demand assistant that answers stock questions and the latest market-news",
		cat, "react", nil)
	if err != nil {
		t.Fatalf("CompileAgent: %v", err)
	}
	has := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}
	if !has(res.Workflow.Skills, "yfinance") {
		t.Errorf("near-miss 'yahoo finance' should be corrected to installed 'yfinance'; got %v", res.Workflow.Skills)
	}
	if !has(res.Workflow.Skills, "market-news") {
		t.Errorf("'market-news' referenced in intent should be injected; got %v", res.Workflow.Skills)
	}
	if has(res.Workflow.Skills, "totally-made-up-skill") {
		t.Errorf("an uninstalled skill must not be kept on the agent; got %v", res.Workflow.Skills)
	}
	var flaggedMissing bool
	for _, sg := range res.Suggestions {
		if sg.Kind == "skill" && sg.Name == "totally-made-up-skill" && !sg.Installed {
			flaggedMissing = true
		}
	}
	if !flaggedMissing {
		t.Errorf("uninstalled named skill should surface as a Needs-setup suggestion; got %+v", res.Suggestions)
	}
}

func TestToAgentDefinition_ReActHasNoWorkflow(t *testing.T) {
	d := Draft{
		Name:         "Daily AI Podcast",
		Strategy:     "react",
		SystemPrompt: "You produce a podcast.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels:     []string{"telegram"},
		Tools:        []string{"web_search", "mcp__notebooklm__create"},
	}
	def, err := ToAgentDefinition(d, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Workflow != nil {
		t.Error("ReAct agent must NOT have a workflow block (would override strategy)")
	}
	if def.Reasoning.Strategy != "react" {
		t.Errorf("strategy not set: %q", def.Reasoning.Strategy)
	}
	if def.Builtins == nil || len(*def.Builtins) != 1 || (*def.Builtins)[0] != "web_search" {
		t.Errorf("builtins: %+v", def.Builtins)
	}
	if def.MCPTools == nil || len(*def.MCPTools) != 1 {
		t.Errorf("mcp tools: %+v", def.MCPTools)
	}
	if !strings.Contains(def.SystemPrompt, "reasoning") && !strings.Contains(def.SystemPrompt, "step by step") {
		t.Errorf("system prompt should carry a loop directive: %q", def.SystemPrompt)
	}
}

func TestPreflight_ReActDisconnectedMCPBlocks(t *testing.T) {
	d := Draft{Strategy: "react", SystemPrompt: "x", Tools: []string{"mcp__notebooklm__create"}}
	r := Preflight(d, PreflightInput{ConnectedMCP: map[string]bool{}})
	found := false
	for _, b := range r.Blockers {
		if b.Kind == "mcp" {
			found = true
		}
	}
	if !found {
		t.Errorf("react agent with disconnected MCP tool should block: %+v", r.Blockers)
	}
}
