package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
	"go.uber.org/zap"
)

// Story E25: graph-mode workflows on the existing executor + checkpoints.

func TestWorkflowExecutor_GraphMode_BoundedCycle(t *testing.T) {
	store := newTestCheckpointStore(t)
	e := &Engine{}
	judgeCalls := 0
	var path []string
	e.builtins = []BuiltinTool{
		{
			Name: "improve",
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				path = append(path, "improve")
				return `"draft"`, nil
			},
		},
		{
			Name: "evaluate",
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				path = append(path, "evaluate")
				judgeCalls++
				if judgeCalls >= 3 {
					return `{"ok": true}`, nil
				}
				return `{"ok": false}`, nil
			},
		},
		{
			Name: "publish",
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				path = append(path, "publish")
				return `"published"`, nil
			},
		},
	}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "refine", Tool: "improve"},
			{ID: "judge", Tool: "evaluate", Output: "verdict"},
			{ID: "ship", Tool: "publish"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "refine", To: "judge", MaxIterations: 10},
			{From: "judge", To: "refine", If: "{{not .verdict.ok}}", MaxIterations: 5},
			{From: "judge", To: "ship"},
		},
	}, e, store, zap.NewNop())

	out, err := w.Run(context.Background(), workflowTestMessage("go"), "flow-run-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rawJSONString(t, out); got != "published" {
		t.Fatalf("output = %q", got)
	}
	// refine judge ×3 (verdict ok on 3rd), then ship.
	want := "improve evaluate improve evaluate improve evaluate publish"
	if got := joinPath(path); got != want {
		t.Fatalf("path = %q\nwant   %q", got, want)
	}
	// Visit-indexed checkpoints persisted per cycle iteration.
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-1", "judge#1", CheckpointCompleted)
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-1", "judge#3", CheckpointCompleted)
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-1", "ship#1", CheckpointCompleted)
}

func TestWorkflowExecutor_GraphMode_ResumeSkipsCompletedVisits(t *testing.T) {
	store := newTestCheckpointStore(t)

	build := func(failSecond bool, calls *[]string) *WorkflowExecutor {
		e := &Engine{}
		e.builtins = []BuiltinTool{
			{
				Name: "stage1",
				Handler: func(_ context.Context, _ map[string]any) (string, error) {
					*calls = append(*calls, "stage1")
					return `{"v":"ONE"}`, nil
				},
			},
			{
				Name: "stage2",
				Handler: func(_ context.Context, args map[string]any) (string, error) {
					*calls = append(*calls, "stage2")
					if failSecond {
						return "", errors.New("crash")
					}
					return `"` + args["carry"].(string) + `-TWO"`, nil
				},
			},
		}
		return NewWorkflowExecutor(agent.WorkflowSpec{
			Nodes: []sdkr.FlowNode{
				{ID: "one", Tool: "stage1", Output: "r1"},
				{ID: "two", Tool: "stage2", Input: `{"carry":"{{.r1.v}}"}`},
			},
			Edges: []sdkr.FlowEdge{{From: "one", To: "two"}},
		}, e, store, zap.NewNop())
	}

	var calls1 []string
	if _, err := build(true, &calls1).Run(context.Background(), workflowTestMessage("x"), "flow-run-2"); err == nil {
		t.Fatal("first run should fail at node two")
	}
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-2", "one#1", CheckpointCompleted)
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-2", "two#1", CheckpointFailed)

	var calls2 []string
	out, err := build(false, &calls2).Run(context.Background(), workflowTestMessage("x"), "flow-run-2")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	// stage1 must NOT re-run; stage2 gets the RESTORED r1 var.
	if got := joinPath(calls2); got != "stage2" {
		t.Fatalf("resume calls = %q, want only stage2", got)
	}
	if got := rawJSONString(t, out); got != "ONE-TWO" {
		t.Fatalf("output = %q, want ONE-TWO (proves restored vars)", got)
	}
}

func TestWorkflowExecutor_GraphMode_InvalidGraphRefused(t *testing.T) {
	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "ghost"}},
	}, &Engine{}, newTestCheckpointStore(t), zap.NewNop())
	if _, err := w.Run(context.Background(), workflowTestMessage("x"), ""); err == nil {
		t.Fatal("expected compile error for edge to unknown node")
	}
}

func joinPath(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
