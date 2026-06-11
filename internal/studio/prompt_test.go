package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// A saved Studio agent must carry a well-defined system prompt (not the old
// placeholder): the trigger, the ordered steps, the output channel, and — when
// host execution is involved — a scope-of-action line.
func TestToAgentDefinition_WellDefinedPrompt(t *testing.T) {
	draft := Draft{
		Name:     "Daily Digest",
		Trigger:  Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels: []string{"slack"},
		Flow: Flow{
			Entry: "curate",
			Nodes: []sdkr.FlowNode{
				{ID: "curate", Kind: sdkr.FlowNodeAgent, Agent: "curator", Output: "urls"},
				{ID: "pipeline", Kind: sdkr.FlowNodePython, Code: "import subprocess\n", Output: "audio"},
				{ID: "notify", Kind: sdkr.FlowNodeTool, Tool: "channel.send"},
			},
			Edges: []sdkr.FlowEdge{
				{From: "curate", To: "pipeline"},
				{From: "pipeline", To: "notify"},
				{From: "notify", To: "end"},
			},
		},
	}
	def, err := ToAgentDefinition(draft, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	p := def.SystemPrompt
	if strings.Contains(p, "Studio-authored workflow agent.") || len(p) < 80 {
		t.Fatalf("prompt looks like a placeholder: %q", p)
	}
	for _, want := range []string{"Daily Digest", "cron", "0 7 * * *", "curator", "Python", "channel.send", "slack", "Steps:"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q.\n---\n%s", want, p)
		}
	}
	// The python+subprocess step must trigger the host-execution scope line.
	if !strings.Contains(strings.ToLower(p), "system commands on the host") {
		t.Fatalf("prompt missing host-execution scope note:\n%s", p)
	}
	// Steps must be ordered from the entry (curate before notify).
	if strings.Index(p, "curator") > strings.Index(p, "channel.send") {
		t.Fatalf("steps not in execution order:\n%s", p)
	}
	if !strings.Contains(def.Description, "step") {
		t.Fatalf("Description should summarize steps, got %q", def.Description)
	}
}
