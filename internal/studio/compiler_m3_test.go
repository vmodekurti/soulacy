package studio

import (
	"context"
	"strings"
	"testing"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// TestCompile_MultiStepBranchGraph (Story M3, test a): a multi-step intent
// compiles to >=3 nodes including a branch node with 2 conditional out-edges,
// and the result passes reasoning.CompileFlow.
func TestCompile_MultiStepBranchGraph(t *testing.T) {
	canned := `{
  "name": "Triage Pipeline",
  "trigger": { "type": "schedule", "config": { "cron": "0 8 * * 1-5" } },
  "channels": ["telegram"],
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{}", "output": "stories" },
      { "id": "triage", "kind": "branch" },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "do it", "output": "summary" },
      { "id": "skip", "kind": "agent", "agent": "notifier", "input": "nothing", "output": "note" }
    ],
    "edges": [
      { "from": "fetch", "to": "triage" },
      { "from": "triage", "to": "summarize", "if": "{{ gt (len .stories) 0 }}" },
      { "from": "triage", "to": "skip" },
      { "from": "summarize", "to": "end" },
      { "from": "skip", "to": "end" }
    ],
    "entry": "fetch"
  }
}`
	res, err := Compile(context.Background(), fakeLLM{out: canned}, "fetch then branch", Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	nodes := res.Workflow.Flow.Nodes
	if len(nodes) < 3 {
		t.Fatalf("expected >=3 nodes, got %d", len(nodes))
	}

	// Locate the branch node and confirm it has 2 conditional-capable out-edges.
	var branchID string
	for _, n := range nodes {
		if n.Kind == "branch" {
			branchID = n.ID
		}
	}
	if branchID == "" {
		t.Fatalf("expected a branch node in the compiled graph")
	}
	outEdges := 0
	conditional := 0
	for _, e := range res.Workflow.Flow.Edges {
		if e.From == branchID {
			outEdges++
			if strings.TrimSpace(e.If) != "" {
				conditional++
			}
		}
	}
	if outEdges < 2 {
		t.Fatalf("branch %q should fan out to >=2 edges, got %d", branchID, outEdges)
	}
	if conditional < 1 {
		t.Fatalf("branch %q should have >=1 conditional out-edge, got %d", branchID, conditional)
	}

	// Hard contract: the compiled graph passes CompileFlow.
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("compiled branch graph failed CompileFlow: %v", err)
	}
}

// TestCompile_PeerAgentHandoff (Story M3, test b): a kind=agent node
// referencing a catalog agent compiles, and multiple agent nodes are peers.
func TestCompile_PeerAgentHandoff(t *testing.T) {
	canned := `{
  "name": "Research Handoff",
  "trigger": { "type": "manual" },
  "channels": ["slack"],
  "flow": {
    "nodes": [
      { "id": "research", "kind": "agent", "agent": "researcher", "input": "find sources", "output": "sources" },
      { "id": "write", "kind": "agent", "agent": "writer", "input": "draft from {{.sources}}", "output": "draft" }
    ],
    "edges": [
      { "from": "research", "to": "write" },
      { "from": "write", "to": "end" }
    ],
    "entry": "research"
  }
}`
	cat := Catalog{Agents: []string{"researcher", "writer"}}
	res, err := Compile(context.Background(), fakeLLM{out: canned}, "research then write", cat, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	peers := flowPeers(res.Workflow.Flow)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peer agents, got %d (%v)", len(peers), peers)
	}
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("peer-agent graph failed CompileFlow: %v", err)
	}
}

// TestCompile_TypedPortsPass (Story M3, test c happy path): a graph using
// from_port/to_port with DECLARED node ports compiles.
func TestCompile_TypedPortsPass(t *testing.T) {
	canned := `{
  "name": "Ported Flow",
  "trigger": { "type": "manual" },
  "channels": ["email"],
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{}", "output": "data",
        "outputs": [ { "name": "ok" } ] },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "go",
        "inputs": [ { "name": "in" } ] }
    ],
    "edges": [
      { "from": "fetch", "to": "summarize", "from_port": "ok", "to_port": "in" },
      { "from": "summarize", "to": "end" }
    ],
    "entry": "fetch"
  }
}`
	res, err := Compile(context.Background(), fakeLLM{out: canned}, "ported", Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("typed-port graph failed CompileFlow: %v", err)
	}
}

// TestCompile_UndeclaredPortErrors (Story M3, test c error path): an edge that
// names a port NOT declared on the referenced node is rejected — Compile
// returns an error and does not produce a draft.
func TestCompile_UndeclaredPortErrors(t *testing.T) {
	canned := `{
  "name": "Bad Ports",
  "trigger": { "type": "manual" },
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{}", "output": "data" },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "go" }
    ],
    "edges": [
      { "from": "fetch", "to": "summarize", "from_port": "nope" },
      { "from": "summarize", "to": "end" }
    ],
    "entry": "fetch"
  }
}`
	_, err := Compile(context.Background(), fakeLLM{out: canned}, "bad ports", Catalog{}, nil)
	if err == nil {
		t.Fatalf("expected an error for an undeclared from_port, got nil")
	}
	if !strings.Contains(err.Error(), "from_port") {
		t.Fatalf("expected error to mention from_port, got: %v", err)
	}
}

// TestNormalizeFlow_FillsKinds confirms post-parse normalization makes implied
// kinds explicit (tool/agent/branch) without altering graph shape.
func TestNormalizeFlow_FillsKinds(t *testing.T) {
	dd := Draft{Flow: Flow{Entry: "a", Nodes: []sdkr.FlowNode{
		{ID: "a", Tool: "http_get"},    // -> tool
		{ID: "b", Agent: "summarizer"}, // -> agent
		{ID: "c"},                      // -> branch
	}}}
	normalizeFlow(&dd)
	want := []string{"tool", "agent", "branch"}
	for i, n := range dd.Flow.Nodes {
		if n.Kind != want[i] {
			t.Fatalf("node %d: kind = %q, want %q", i, n.Kind, want[i])
		}
	}
}
