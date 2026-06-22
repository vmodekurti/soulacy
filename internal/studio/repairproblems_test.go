package studio

import (
	"context"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestRepairWithProblems_FixesFromMessages(t *testing.T) {
	d := Draft{Name: "X", Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "tool", Tool: "mk", Output: "notebook", Input: `{"title":"t"}`},
		{ID: "b", Kind: "tool", Tool: "use", Input: `{"id":"{{ .missing }}"}`},
	}, Edges: []sdkr.FlowEdge{{From: "a", To: "b"}}}}
	// Model returns the corrected full draft (wires id from notebook).
	fixedJSON := `{"name":"X","trigger":{"type":"manual"},"flow":{"nodes":[
	  {"id":"a","kind":"tool","tool":"mk","output":"notebook","input":"{\"title\":\"t\"}"},
	  {"id":"b","kind":"tool","tool":"use","input":"{\"id\":\"{{ .notebook }}\"}"}
	],"edges":[{"from":"a","to":"b"}],"entry":"a"}}`
	out, changed := RepairWithProblems(context.Background(), fakeLLM{out: fixedJSON}, d, []string{`node "b": references {{ .missing }} but no step produces it`}, Catalog{})
	if !changed {
		t.Fatal("expected repair to apply")
	}
	if !strings.Contains(out.Flow.Nodes[1].Input, "{{ .notebook }}") {
		t.Errorf("not repaired: %q", out.Flow.Nodes[1].Input)
	}
}

func TestRepairWithProblems_NoProblemsNoCall(t *testing.T) {
	d := Draft{Name: "X"}
	if _, changed := RepairWithProblems(context.Background(), fakeLLM{err: context.DeadlineExceeded}, d, nil, Catalog{}); changed {
		t.Error("no problems → no call/change")
	}
}
