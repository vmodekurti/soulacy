// testrun.go — Studio's "test" step (Story S1.x, Wave 2). It dry-runs a
// draft workflow through the reasoning flow engine with a MOCK node runner
// that never touches real tools/agents/LLMs, producing a deterministic
// trace + final result the GUI can show before the user saves anything.
//
// The runnable logic lives here (not in the gateway) so it is unit-testable
// without an HTTP server: TestRun(ctx, draft, input) walks the compiled flow
// exactly like production (reasoning.CompileFlow + reasoning.RunFlow, seeded
// with {"trigger": input} to match flowstrategy.go) but substitutes a stub
// FlowRunNode and records one TraceEntry per executed node.
package studio

import (
	"context"
	"encoding/json"
	"fmt"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// TraceEntry is one executed node's record in a test run, in execution
// order. Branch nodes perform no action and are not recorded (they are
// never passed to the FlowRunNode).
type TraceEntry struct {
	NodeID string          `json:"nodeId"`
	Kind   string          `json:"kind"`
	Input  string          `json:"input"`
	Output json.RawMessage `json:"output"`
}

// TestResult is the response of a Studio test run: the per-node trace and
// the final flow output (the last executed node's result).
type TestResult struct {
	Trace  []TraceEntry    `json:"trace"`
	Result json.RawMessage `json:"result"`
}

// TestRun compiles the draft's flow and executes it with a deterministic
// MOCK runner: tool nodes return {"tool":<name>,"mocked":true,"input":<rendered>},
// agent nodes return a short canned summary string, and every executed node
// appends a trace entry in execution order. It seeds vars with
// {"trigger": input} so flow templates that reference the trigger render.
//
// An invalid flow (fails reasoning.CompileFlow) is an error; a valid flow
// always yields a TestResult.
func TestRun(ctx context.Context, draft Draft, input string) (TestResult, error) {
	g, err := reasoning.CompileFlow(draft.spec())
	if err != nil {
		return TestResult{}, fmt.Errorf("studio: test flow is invalid: %w", err)
	}

	var trace []TraceEntry
	run := func(_ context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error) {
		out := mockNodeOutput(node, renderedInput)
		trace = append(trace, TraceEntry{
			NodeID: node.ID,
			Kind:   nodeKind(node),
			Input:  renderedInput,
			Output: out,
		})
		return out, nil
	}

	vars := map[string]any{"trigger": input}
	result, err := reasoning.RunFlow(ctx, g, vars, run, reasoning.FlowHooks{})
	if err != nil {
		return TestResult{}, fmt.Errorf("studio: test run: %w", err)
	}
	if trace == nil {
		trace = []TraceEntry{}
	}
	return TestResult{Trace: trace, Result: result}, nil
}

// nodeKind returns the effective kind, mirroring CompileFlow's inference so
// the trace reports the same kind the engine executed (tool/agent/branch).
func nodeKind(node sdkr.FlowNode) string {
	if node.Kind != "" {
		return node.Kind
	}
	switch {
	case node.Tool != "":
		return sdkr.FlowNodeTool
	case node.Agent != "":
		return sdkr.FlowNodeAgent
	default:
		return sdkr.FlowNodeBranch
	}
}

// mockNodeOutput produces the deterministic stub a node "returns" in a test
// run. It NEVER invokes a real tool, agent, or LLM.
func mockNodeOutput(node sdkr.FlowNode, renderedInput string) json.RawMessage {
	switch nodeKind(node) {
	case sdkr.FlowNodeAgent:
		summary := fmt.Sprintf("[mock] agent %q would respond here based on its input.", node.Agent)
		b, _ := json.Marshal(summary)
		return b
	case sdkr.FlowNodeTool:
		b, _ := json.Marshal(map[string]any{
			"tool":   node.Tool,
			"mocked": true,
			"input":  renderedInput,
		})
		return b
	default:
		// Branch nodes never reach here (RunFlow skips them), but be safe.
		b, _ := json.Marshal(map[string]any{"node": node.ID, "mocked": true})
		return b
	}
}
