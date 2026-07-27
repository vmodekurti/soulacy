package studio

// zzprobe_test.go — the property the transactional repair replay depends on.
//
// ApplyRepairTransactionally promotes a proposal only when replaying the
// original failing input against the repaired copy succeeds. That is only worth
// anything if the mocked walk actually FAILS on a template that cannot resolve:
// if TestRun quietly rendered an unresolvable reference as an empty string, every
// bad repair would replay "successfully" and the gate would be decorative.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func replayProbeDraft(input string) Draft {
	return Draft{
		Name:    "probe",
		Trigger: Trigger{Type: "channel"},
		Flow: Flow{
			Entry: "search",
			Nodes: []sdkr.FlowNode{
				{ID: "search", Kind: sdkr.FlowNodeTool, Tool: "web_search", Output: "search"},
				{ID: "fmt", Kind: sdkr.FlowNodeTool, Tool: "publish", Input: input, Output: "reply"},
			},
			Edges: []sdkr.FlowEdge{{From: "search", To: "fmt"}, {From: "fmt", To: "end"}},
		},
	}
}

func TestTestRunRejectsUnresolvableInputTemplate(t *testing.T) {
	_, err := TestRun(context.Background(), replayProbeDraft(`{{ toJson .search.results }}`), "", nil)
	if err == nil {
		t.Fatal("a node input referencing a key nothing produced must fail the mocked walk; " +
			"otherwise a repair replay can never disprove a bad patch")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Errorf("the error should name the unresolved reference, got %v", err)
	}
}

func TestTestRunMocksSeedTheUpstreamShape(t *testing.T) {
	// The counterpart: seeded with the shape the provider ACTUALLY returned, the
	// same walk resolves. This is what lets the gateway prove a repair against the
	// bytes that broke it rather than against a synthetic stub.
	res, err := TestRun(context.Background(), replayProbeDraft(`{{ toJson .search.items }}`), "",
		&TestOptions{Mocks: map[string]json.RawMessage{"search": json.RawMessage(`{"items":[{"title":"a"}]}`)}})
	if err != nil {
		t.Fatalf("seeded replay should render: %v", err)
	}
	if len(res.Trace) != 2 {
		t.Fatalf("both steps should run, got %d entries", len(res.Trace))
	}
}
