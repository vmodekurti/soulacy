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
