package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// collect runs checkTemplateReferences and returns the warnings it produced.
func collectTemplateRefs(draft Draft) []PreflightIssue {
	var issues []PreflightIssue
	add := func(sev, kind, node, msg, fix string) {
		issues = append(issues, PreflightIssue{Severity: sev, Kind: kind, NodeID: node, Message: msg, Fix: fix})
	}
	checkTemplateReferences(draft, add)
	return issues
}

func toolNode(id, output, tool, input string) sdkr.FlowNode {
	return sdkr.FlowNode{ID: id, Kind: "tool", Tool: tool, Output: output, Input: input}
}

// The reported bug: a wrong nested path {{ .notebook.notebook }} that resolves
// to a map instead of the id string.
func TestCheckTemplateRefs_FlagsRepeatedNestedPath(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("add", "", "mcp__notebooklm__add_sources", `{"notebook_id":"{{ .notebook.notebook }}"}`),
	}}}
	issues := collectTemplateRefs(draft)
	if len(issues) == 0 {
		t.Fatal("expected a warning for {{ .notebook.notebook }}")
	}
	got := issues[0]
	if got.Severity != "warn" || got.NodeID != "add" {
		t.Errorf("unexpected issue: %+v", got)
	}
	if !strings.Contains(got.Message, "nested object") {
		t.Errorf("message should explain the nested-object problem: %q", got.Message)
	}
	if !strings.Contains(got.Fix, ".notebook.id") {
		t.Errorf("fix should suggest a scalar field: %q", got.Fix)
	}
}

// A bare whole-object interpolation of a structured (MCP) output.
func TestCheckTemplateRefs_FlagsWholeObjectInterpolation(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("add", "", "mcp__notebooklm__add_sources", `{"notebook_id":"{{ .notebook }}"}`),
	}}}
	issues := collectTemplateRefs(draft)
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d: %+v", len(issues), issues)
	}
	if !strings.Contains(issues[0].Message, "whole") {
		t.Errorf("message should flag the whole-object pass: %q", issues[0].Message)
	}
}

// toJson legitimately serializes the whole object — must NOT warn.
func TestCheckTemplateRefs_AllowsToJson(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("dump", "", "mcp__x__y", `{"payload":"{{ toJson .notebook }}"}`),
	}}}
	if issues := collectTemplateRefs(draft); len(issues) != 0 {
		t.Errorf("toJson should be allowed, got: %+v", issues)
	}
}

// A correct scalar field reference must NOT warn.
func TestCheckTemplateRefs_AllowsScalarField(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		toolNode("create", "notebook", "mcp__notebooklm__create", `{"title":"AI news"}`),
		toolNode("add", "", "mcp__notebooklm__add_sources", `{"notebook_id":"{{ .notebook.id }}"}`),
	}}}
	if issues := collectTemplateRefs(draft); len(issues) != 0 {
		t.Errorf("a scalar field ref should be clean, got: %+v", issues)
	}
}

// An agent step returns text (scalar), so a bare interpolation is fine.
func TestCheckTemplateRefs_AgentOutputIsScalar(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "writer", Kind: "agent", Agent: "summarizer", Output: "summary"},
		toolNode("post", "", "mcp__telegram__send", `{"text":"{{ .summary }}"}`),
	}}}
	if issues := collectTemplateRefs(draft); len(issues) != 0 {
		t.Errorf("agent text output should not warn, got: %+v", issues)
	}
}

// If a var is accessed with a field elsewhere, that proves it's an object, so a
// bare interpolation of it (even from a non-MCP producer) should warn.
func TestCheckTemplateRefs_InfersObjectFromFieldAccess(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "py", Kind: "python", Output: "data", Code: "def run(inputs):\n  return {}"},
		toolNode("a", "", "builtin_x", `{"v":"{{ .data.id }}"}`),
		toolNode("b", "", "builtin_y", `{"v":"{{ .data }}"}`),
	}}}
	issues := collectTemplateRefs(draft)
	var flaggedB bool
	for _, is := range issues {
		if is.NodeID == "b" {
			flaggedB = true
		}
	}
	if !flaggedB {
		t.Errorf("bare {{ .data }} should warn once .data.id proves it's an object; got: %+v", issues)
	}
}
