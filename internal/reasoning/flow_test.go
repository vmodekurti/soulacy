package reasoning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
	"github.com/soulacy/soulacy/sdk/registry"
)

// recNode records each node execution and returns canned results.
type recRunner struct {
	calls   []string // "<nodeID>:<renderedInput>"
	results map[string]string
	errs    map[string]error
	errOnce map[string]bool // error on first call only (retry tests)
	seen    map[string]int
}

func (r *recRunner) run(ctx context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error) {
	if r.seen == nil {
		r.seen = map[string]int{}
	}
	r.seen[node.ID]++
	r.calls = append(r.calls, node.ID+":"+renderedInput)
	if err := r.errs[node.ID]; err != nil {
		if !r.errOnce[node.ID] || r.seen[node.ID] == 1 {
			return nil, err
		}
	}
	if out, ok := r.results[node.ID]; ok {
		return json.RawMessage(out), nil
	}
	return json.RawMessage(fmt.Sprintf(`{"node":%q}`, node.ID)), nil
}

func nodeIDs(calls []string) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, strings.SplitN(c, ":", 2)[0])
	}
	return out
}

func TestCompileFlow_Validation(t *testing.T) {
	cases := map[string]sdkr.FlowSpec{
		"no nodes": {},
		"dup node ids": {Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t"}, {ID: "a", Tool: "t"},
		}},
		"edge from unknown": {
			Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}},
			Edges: []sdkr.FlowEdge{{From: "ghost", To: "a"}},
		},
		"edge to unknown": {
			Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}},
			Edges: []sdkr.FlowEdge{{From: "a", To: "ghost"}},
		},
		"unknown entry": {
			Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}},
			Entry: "ghost",
		},
		"tool node without tool": {
			Nodes: []sdkr.FlowNode{{ID: "a", Kind: sdkr.FlowNodeTool}},
		},
		"agent node without agent": {
			Nodes: []sdkr.FlowNode{{ID: "a", Kind: sdkr.FlowNodeAgent}},
		},
		"bad kind": {
			Nodes: []sdkr.FlowNode{{ID: "a", Kind: "warp", Tool: "t"}},
		},
		"missing node id": {
			Nodes: []sdkr.FlowNode{{Tool: "t"}},
		},
	}
	for name, spec := range cases {
		if _, err := CompileFlow(spec); err == nil {
			t.Errorf("%s: expected compile error", name)
		}
	}

	// Valid graph compiles; kinds are inferred.
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t1"},    // inferred kind=tool
			{ID: "b", Agent: "peer"}, // inferred kind=agent
			{ID: "c"},                // inferred kind=branch
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}, {From: "b", To: "c"}},
	})
	if err != nil {
		t.Fatalf("CompileFlow: %v", err)
	}
	if g.Node("a").Kind != sdkr.FlowNodeTool || g.Node("b").Kind != sdkr.FlowNodeAgent || g.Node("c").Kind != sdkr.FlowNodeBranch {
		t.Errorf("kind inference wrong: %+v %+v %+v", g.Node("a"), g.Node("b"), g.Node("c"))
	}
}

func TestRunFlow_LinearChain(t *testing.T) {
	r := &recRunner{results: map[string]string{
		"fetch":   `{"items": 3}`,
		"publish": `"done"`,
	}}
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "fetch", Tool: "web_search", Input: `{"q":"{{.trigger}}"}`, Output: "found"},
			{ID: "publish", Tool: "post", Input: `{"count":{{.found.items}}}`},
		},
		Edges: []sdkr.FlowEdge{{From: "fetch", To: "publish"}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := RunFlow(context.Background(), g, map[string]any{"trigger": "news"}, r.run, FlowHooks{})
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if string(out) != `"done"` {
		t.Errorf("final output = %s", out)
	}
	want := []string{`fetch:{"q":"news"}`, `publish:{"count":3}`}
	if len(r.calls) != 2 || r.calls[0] != want[0] || r.calls[1] != want[1] {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
}

func TestRunFlow_ConditionalBranch(t *testing.T) {
	r := &recRunner{results: map[string]string{
		"check": `{"severity": "high"}`,
	}}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "check", Tool: "probe", Output: "res"},
			{ID: "page", Tool: "pagerduty"},
			{ID: "log", Tool: "logger"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "check", To: "page", If: `{{eq .res.severity "high"}}`},
			{From: "check", To: "log"}, // fallback
		},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	got := nodeIDs(r.calls)
	if len(got) != 2 || got[1] != "page" {
		t.Errorf("path = %v, want [check page]", got)
	}
}

func TestRunFlow_BoundedCycle(t *testing.T) {
	// refine ↔ judge cycle: back edge capped at 3 traversals, judge never
	// says done, so the loop drains the budget then exits via the
	// fallback edge to finish.
	r := &recRunner{results: map[string]string{
		"judge": `{"ok": false}`,
	}}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "refine", Tool: "improve"},
			{ID: "judge", Tool: "evaluate", Output: "verdict"},
			{ID: "finish", Tool: "publish"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "refine", To: "judge", MaxIterations: 4},
			{From: "judge", To: "refine", If: `{{not .verdict.ok}}`, MaxIterations: 3},
			{From: "judge", To: "finish"},
		},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	got := nodeIDs(r.calls)
	// refine judge ×4 (1 initial + 3 loop-backs), then finish.
	want := []string{"refine", "judge", "refine", "judge", "refine", "judge", "refine", "judge", "finish"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("path = %v\nwant  %v", got, want)
	}
}

func TestRunFlow_DefaultEdgeBudgetIsOne(t *testing.T) {
	// A cycle with no explicit max_iterations must run at most once around.
	r := &recRunner{}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t"},
			{ID: "b", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "a", To: "b", MaxIterations: 2},
			{From: "b", To: "a"}, // default budget 1: one loop-back only
		},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	got := strings.Join(nodeIDs(r.calls), " ")
	if got != "a b a b" {
		t.Errorf("path = %q, want \"a b a b\"", got)
	}
}

func TestRunFlow_GlobalBudgetAborts(t *testing.T) {
	r := &recRunner{}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t"},
			{ID: "b", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "a", To: "b", MaxIterations: 1000},
			{From: "b", To: "a", MaxIterations: 1000},
		},
		MaxNodeExecutions: 7,
	})
	_, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget abort, got %v (calls=%d)", err, len(r.calls))
	}
	if len(r.calls) != 7 {
		t.Errorf("executed %d nodes, want exactly 7", len(r.calls))
	}
}

func TestRunFlow_OnErrorSemantics(t *testing.T) {
	boom := errors.New("boom")

	// abort (default)
	r := &recRunner{errs: map[string]error{"a": boom}}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}, {ID: "b", Tool: "t"}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err == nil {
		t.Error("abort: expected error")
	}

	// skip: error swallowed, edges still followed
	r = &recRunner{errs: map[string]error{"a": boom}}
	g, _ = CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", OnError: "skip"}, {ID: "b", Tool: "t"}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Errorf("skip: %v", err)
	}
	if got := strings.Join(nodeIDs(r.calls), " "); got != "a b" {
		t.Errorf("skip path = %q", got)
	}

	// retry: first call fails, retry succeeds
	r = &recRunner{errs: map[string]error{"a": boom}, errOnce: map[string]bool{"a": true}}
	g, _ = CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", OnError: "retry"}, {ID: "b", Tool: "t"}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Errorf("retry: %v", err)
	}
	if got := strings.Join(nodeIDs(r.calls), " "); got != "a a b" {
		t.Errorf("retry path = %q", got)
	}
}

func TestRunFlow_BranchNodeRunsNothing(t *testing.T) {
	r := &recRunner{results: map[string]string{"probe": `{"n": 2}`}}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "probe", Tool: "t", Output: "p"},
			{ID: "fork"}, // branch
			{ID: "small", Tool: "t"},
			{ID: "big", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "probe", To: "fork"},
			{From: "fork", To: "big", If: `{{gt (printf "%v" .p.n) "5"}}`},
			{From: "fork", To: "small"},
		},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	got := strings.Join(nodeIDs(r.calls), " ")
	if got != "probe small" {
		t.Errorf("path = %q, want \"probe small\" (branch node must not execute)", got)
	}
}

func TestRunFlow_HooksAndResume(t *testing.T) {
	// First run: node b aborts. Second run: hooks replay a's completed
	// visit (restore) so only b (and c) execute.
	completed := map[string]json.RawMessage{} // visitKey → state

	hooks := FlowHooks{
		Restore: func(visitKey string) (json.RawMessage, bool) {
			st, ok := completed[visitKey]
			return st, ok
		},
		Completed: func(visitKey string, state json.RawMessage) {
			completed[visitKey] = state
		},
	}

	spec := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t", Output: "ares"},
			{ID: "b", Tool: "t", Output: "bres"},
			{ID: "c", Tool: "t", Input: `{{.ares.v}}-{{.bres.v}}`},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}, {From: "b", To: "c"}},
	}
	g, _ := CompileFlow(spec)

	r1 := &recRunner{
		results: map[string]string{"a": `{"v":"A"}`},
		errs:    map[string]error{"b": errors.New("crash")},
	}
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r1.run, hooks); err == nil {
		t.Fatal("first run should fail at b")
	}
	if len(completed) != 1 {
		t.Fatalf("completed after crash = %v, want only a's visit", completed)
	}

	r2 := &recRunner{results: map[string]string{
		"a": `{"v":"WRONG"}`, // must NOT be called
		"b": `{"v":"B"}`,
	}}
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r2.run, hooks); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	got := nodeIDs(r2.calls)
	if strings.Join(got, " ") != "b c" {
		t.Fatalf("resume path = %v, want [b c]", got)
	}
	// c's input proves a's state was RESTORED, not re-run.
	if want := "c:A-B"; r2.calls[1] != want {
		t.Errorf("c call = %q, want %q", r2.calls[1], want)
	}
}

func TestFlowStrategy_RegisteredAndRuns(t *testing.T) {
	// The "flow" strategy must be in the registry and execute the graph
	// through env.Tools.
	exec := &fakeFlowExecutor{out: map[string]string{
		"greet": "hello from tool",
	}}
	strat, ok, err := registry.NewReasoningStrategy("flow", nil)
	if !ok || err != nil || strat == nil {
		t.Fatalf("flow strategy not registered: ok=%v err=%v", ok, err)
	}
	env := Env{
		Config: LoopConfig{
			Strategy: "flow",
			Flow: &sdkr.FlowSpec{
				Nodes: []sdkr.FlowNode{{ID: "n1", Tool: "greet", Input: `{{.trigger}}`, Output: "g"}},
			},
		},
		Tools: exec,
	}
	steps, resp := strat.Run(context.Background(), env, "hi there")
	if len(steps) != 1 || steps[0].Action.Tool != "greet" {
		t.Fatalf("steps = %+v", steps)
	}
	if !strings.Contains(resp.Output, "hello from tool") {
		t.Errorf("output = %q", resp.Output)
	}
	if exec.calls[0].Tool != "greet" {
		t.Errorf("executor saw %+v", exec.calls)
	}
}

type fakeFlowExecutor struct {
	calls []ToolCall
	out   map[string]string
}

func (f *fakeFlowExecutor) Execute(ctx context.Context, call ToolCall) Observation {
	f.calls = append(f.calls, call)
	return Observation{Content: f.out[call.Tool], Source: call.Tool}
}
