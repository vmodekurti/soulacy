package studio

import (
	"reflect"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// ToAgentDefinition → FromAgentDefinition must round-trip the fields Studio owns
// (name, trigger+cron, channels, flow graph), so a saved agent re-opens on the
// canvas exactly as authored.
func TestAgentDefinitionRoundTrip(t *testing.T) {
	orig := Draft{
		Name:     "My Flow",
		Trigger:  Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels: []string{"slack", "email"},
		Flow: Flow{
			Entry: "a",
			Nodes: []sdkr.FlowNode{
				{ID: "a", Kind: sdkr.FlowNodeTool, Tool: "web_search", Output: "r"},
				{ID: "b", Kind: sdkr.FlowNodePython, Code: "def run(i):\n    return i"},
			},
			Edges: []sdkr.FlowEdge{{From: "a", To: "b"}, {From: "b", To: "end"}},
		},
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if !HasWorkflow(def) {
		t.Fatal("saved agent should report HasWorkflow")
	}
	back := FromAgentDefinition(def)

	if back.Name != orig.Name {
		t.Fatalf("name: %q != %q", back.Name, orig.Name)
	}
	if back.Trigger.Type != "schedule" || back.Trigger.Config["cron"] != "0 7 * * *" {
		t.Fatalf("trigger not preserved: %+v", back.Trigger)
	}
	if !reflect.DeepEqual(back.Channels, orig.Channels) {
		t.Fatalf("channels: %v != %v", back.Channels, orig.Channels)
	}
	if back.Flow.Entry != "a" || len(back.Flow.Nodes) != 2 || len(back.Flow.Edges) != 2 {
		t.Fatalf("flow not preserved: %+v", back.Flow)
	}
	if back.Flow.Nodes[1].Code != "def run(i):\n    return i" {
		t.Fatalf("python code not preserved: %q", back.Flow.Nodes[1].Code)
	}
}

// A ReAct/Plan-Execute agent must survive the round-trip as an AGENT — keeping
// its strategy, prompt, tools, skills, knowledge and unattended flag — so that
// switching to Canvas or re-opening it never silently turns it into a workflow
// (which would add a stray `workflow:` block on save and mislabel the toggle).
func TestAgentDefinitionRoundTrip_PreservesReasoningStrategy(t *testing.T) {
	orig := Draft{
		Name:         "Finance Assistant",
		Strategy:     "react",
		SystemPrompt: "You are a finance assistant. Reason over the skills.",
		Trigger:      Trigger{Type: "channel"},
		Channels:     []string{"chat"},
		Tools:        []string{"web_search", "mcp__finance__quote"},
		Skills:       []string{"stock_performance", "market_news"},
		Knowledge:    []string{"sec_filings"},
		Unattended:   true,
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if HasWorkflow(def) {
		t.Fatal("a reasoning agent must NOT carry a workflow graph")
	}
	back := FromAgentDefinition(def)

	if !back.IsAgent() || back.Strategy != "react" {
		t.Fatalf("strategy not preserved: %q (IsAgent=%v)", back.Strategy, back.IsAgent())
	}
	if back.SystemPrompt == "" {
		t.Error("system prompt not preserved")
	}
	if !back.Unattended {
		t.Error("unattended flag not preserved")
	}
	if !reflect.DeepEqual(back.Tools, []string{"web_search", "mcp__finance__quote"}) {
		t.Errorf("tools not preserved (builtin+mcp split should reassemble): %v", back.Tools)
	}
	if !reflect.DeepEqual(back.Skills, orig.Skills) {
		t.Errorf("skills not preserved: %v", back.Skills)
	}
	if !reflect.DeepEqual(back.Knowledge, orig.Knowledge) {
		t.Errorf("knowledge not preserved: %v", back.Knowledge)
	}
	// And it must NOT have been given a flow graph.
	if len(back.Flow.Nodes) != 0 {
		t.Errorf("an agent round-trip must not synthesize flow nodes: %+v", back.Flow)
	}
}

// A reasoning agent's tuned reasoning budgets and max_turns must survive the
// round-trip (regression for the "canvas re-save resets timeouts" bug).
func TestAgentRoundTrip_PreservesReasoningBudgets(t *testing.T) {
	orig := Draft{
		Name: "Tuned", Strategy: "react", SystemPrompt: "p",
		Trigger: Trigger{Type: "channel"}, Channels: []string{"chat"},
		Tools:        []string{"web_search"},
		StepTimeout:  "45s",
		TotalTimeout: "300s",
		MaxTurns:     25,
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Reasoning.StepTimeout != "45s" || def.Reasoning.TotalTimeout != "300s" || def.MaxTurns != 25 {
		t.Fatalf("save dropped tuned budgets: step=%q total=%q turns=%d", def.Reasoning.StepTimeout, def.Reasoning.TotalTimeout, def.MaxTurns)
	}
	back := FromAgentDefinition(def)
	if back.StepTimeout != "45s" || back.TotalTimeout != "300s" || back.MaxTurns != 25 {
		t.Fatalf("load dropped tuned budgets: %+v", back)
	}
	// Re-save must NOT reset them to defaults.
	def2, _ := ToAgentDefinition(back, false)
	if def2.Reasoning.StepTimeout != "45s" || def2.Reasoning.TotalTimeout != "300s" || def2.MaxTurns != 25 {
		t.Fatalf("re-save reset budgets: step=%q total=%q turns=%d", def2.Reasoning.StepTimeout, def2.Reasoning.TotalTimeout, def2.MaxTurns)
	}
}

// A WORKFLOW agent's canvas-owned fields (knowledge, unattended) must survive
// open-in-canvas → re-save (regression for the silent-wipe bug).
func TestWorkflowRoundTrip_PreservesKnowledgeAndUnattended(t *testing.T) {
	orig := Draft{
		Name:       "Flow", Trigger: Trigger{Type: "manual"},
		Knowledge:  []string{"sec_filings"},
		Unattended: true,
		Flow: Flow{Entry: "a", Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: sdkr.FlowNodeTool, Tool: "web_search", Output: "r"},
		}},
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	back := FromAgentDefinition(def)
	if len(back.Knowledge) != 1 || back.Knowledge[0] != "sec_filings" {
		t.Errorf("workflow round-trip dropped knowledge: %v", back.Knowledge)
	}
	if !back.Unattended {
		t.Errorf("workflow round-trip dropped unattended flag")
	}
}
