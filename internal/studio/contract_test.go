package studio

import (
	"context"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestAssessContract_CleanWorkflowPasses(t *testing.T) {
	d := cleanWorkflow()
	r := AssessContract(d, Catalog{Tools: []string{"web_search"}}, PreflightInput{
		Catalog: Catalog{Tools: []string{"web_search"}},
	})
	if !r.OK {
		t.Fatalf("clean workflow contract should pass: %+v", r)
	}
	if r.Score != 100 {
		t.Fatalf("score = %d, want 100", r.Score)
	}
	if !hasContractCheck(r, "graph.integrity", "pass") || !hasContractCheck(r, "runtime.preflight", "pass") {
		t.Fatalf("expected graph + runtime pass checks, got %+v", r.Checks)
	}
}

func TestAssessContract_FlagsOversizedMacroWorkflow(t *testing.T) {
	var nodes []sdkr.FlowNode
	var edges []sdkr.FlowEdge
	for i := 0; i < 9; i++ {
		id := "n" + string(rune('a'+i))
		nodes = append(nodes, sdkr.FlowNode{ID: id, Kind: "python", Code: "def run(inputs):\n    return inputs\n", Output: id})
		if i > 0 {
			edges = append(edges, sdkr.FlowEdge{From: nodes[i-1].ID, To: id})
		}
	}
	d := Draft{Trigger: Trigger{Type: "manual"}, Flow: Flow{Nodes: nodes, Edges: edges, Entry: nodes[0].ID}}
	r := AssessContract(d, Catalog{}, PreflightInput{})
	if r.OK {
		t.Fatalf("oversized fixed workflow should be blocked as brittle: %+v", r)
	}
	if !hasContractCheck(r, "architecture.size", "block") {
		t.Fatalf("expected architecture.size blocker, got %+v", r.Checks)
	}
}

func TestAssessContract_WarnsOnFreeformHandoffToStructuredTool(t *testing.T) {
	d := Draft{Trigger: Trigger{Type: "manual"}, Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "summarize", Kind: "agent", Agent: "summarizer", Output: "reply"},
			{ID: "store", Kind: "tool", Tool: "kb_write", Input: `please store it`, Output: "stored"},
		},
		Edges: []sdkr.FlowEdge{{From: "summarize", To: "store"}},
		Entry: "summarize",
	}}
	r := AssessContract(d, Catalog{Agents: []string{"summarizer"}, Tools: []string{"kb_write"}}, PreflightInput{
		Catalog: Catalog{Agents: []string{"summarizer"}, Tools: []string{"kb_write"}},
	})
	if !hasContractCheck(r, "data.contracts", "warn") {
		t.Fatalf("expected free-form handoff warning, got %+v", r.Checks)
	}
}

func TestBuildUntilWorks_AttachesContract(t *testing.T) {
	rep := BuildUntilWorks(context.Background(), fakeLLM{}, cleanWorkflow(), Catalog{Tools: []string{"web_search"}}, BuildOptions{})
	if rep.Contract.Score == 0 || rep.Contract.Summary == "" {
		t.Fatalf("build report should include a populated contract: %+v", rep.Contract)
	}
}

func hasContractCheck(r ContractResult, id, status string) bool {
	for _, c := range r.Checks {
		if c.ID == id && c.Status == status {
			return true
		}
	}
	return false
}

func TestContractSummaryText(t *testing.T) {
	r := ContractResult{Blockers: 1, Warnings: 2}
	if !strings.Contains(contractSummary(r), "blocked") {
		t.Fatalf("summary should explain blocked state")
	}
}
