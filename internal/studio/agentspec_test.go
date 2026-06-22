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
