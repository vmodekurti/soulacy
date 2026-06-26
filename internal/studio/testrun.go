// testrun.go — Studio's "test" step (Story S1.x, Wave 2; extended in M5,
// Stories S5.2/S5.3). It dry-runs a draft workflow through the reasoning flow
// engine with a MOCK node runner that never touches real tools/agents/LLMs,
// producing a deterministic trace + final result the GUI can show before the
// user saves anything. M5 adds per-node mock overrides and assertions that are
// evaluated against the run.
//
// The runnable logic lives here (not in the gateway) so it is unit-testable
// without an HTTP server: TestRun(ctx, draft, input, opts) walks the compiled
// flow exactly like production (reasoning.CompileFlow + reasoning.RunFlow,
// seeded with {"trigger": input} to match flowstrategy.go) but substitutes a
// stub FlowRunNode and records one TraceEntry per executed node.
//
// Safety: only "dry" mode is implemented. "live" execution of an UNSAVED draft
// is deliberately NOT supported — it would mean running real, possibly
// side-effecting tools/LLMs from unreviewed content. TestRun never does that.
package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// TraceEntry is one executed node's record in a test run, in execution
// order. Branch nodes perform no action and are not recorded (they are
// never passed to the FlowRunNode). Mocked reports whether the node's output
// was overridden by a TestOptions.Mocks entry (instead of the default stub).
type TraceEntry struct {
	NodeID string          `json:"nodeId"`
	Kind   string          `json:"kind"`
	Input  string          `json:"input"`
	Output json.RawMessage `json:"output"`
	Mocked bool            `json:"mocked,omitempty"`
	// DurationMS and Error mirror the live run trace (reasoning.FlowNodeRun) so
	// the GUI renders dry-run and production traces with one component. In a dry
	// run Error is normally empty (the mock runner never calls real tools).
	DurationMS int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
	// WiredPorts reports that this node's input was assembled from typed port
	// wires rather than a Go template (template-free handoff).
	WiredPorts bool `json:"wiredPorts,omitempty"`
}

// Assertion is a single check evaluated against the run after it completes.
// Target is a node id, or the literal "result" to target the final flow
// output. Op is one of "contains" | "equals" | "exists". Value is the literal
// the operator compares against (ignored for "exists").
type Assertion struct {
	Target string `json:"target"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

// AssertionResult is an Assertion plus its outcome. Detail is a short
// human-readable explanation, useful when Pass is false.
type AssertionResult struct {
	Target string `json:"target"`
	Op     string `json:"op"`
	Value  string `json:"value"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// TestOptions carries the editable extras a caller may attach to a test run.
// The zero value is a plain dry-run with no mocks/assertions (the original,
// backward-compatible behavior).
type TestOptions struct {
	// Mocks overrides a node's output by node id. When a node executes in the
	// mock runner and a mock exists for node.ID, the decoded value is used as
	// the node's output (instead of the default stub) and the trace entry is
	// marked Mocked. Unknown node ids are ignored (surfaced as warnings).
	Mocks map[string]json.RawMessage
	// Assertions are evaluated against the trace+result after the run.
	Assertions []Assertion
	// Mode is "dry" (default) or "live". Only "dry" is implemented; "live"
	// returns a result with a clear note that live execution of an unsaved
	// draft is unsupported and runs nothing real.
	Mode string
}

// TestResult is the response of a Studio test run: the per-node trace, the
// final flow output (the last executed node's result), the evaluated
// assertions, an aggregate Passed flag, the echoed Mode, and any Warnings.
type TestResult struct {
	Trace      []TraceEntry      `json:"trace"`
	Result     json.RawMessage   `json:"result"`
	Assertions []AssertionResult `json:"assertions"`
	Passed     bool              `json:"passed"`
	Mode       string            `json:"mode"`
	Warnings   []string          `json:"warnings,omitempty"`
	// Shapes are the captured output shapes per flow var from this run (Phase D):
	// real data the per-node compiler (CompileNode) can be grounded in so it wires
	// downstream steps by actual field names instead of guessing.
	Shapes []UpstreamVar `json:"shapes,omitempty"`
}

// liveNotSupportedNote is the user-facing explanation returned for Mode=="live".
const liveNotSupportedNote = "live execution of an unsaved draft is not supported; " +
	"save and enable the agent, then exercise it via its channel"

// TestRun compiles the draft's flow and executes it with a deterministic MOCK
// runner: tool nodes return {"tool":<name>,"mocked":true,"input":<rendered>},
// agent nodes return a short canned summary string, and every executed node
// appends a trace entry in execution order. It seeds vars with
// {"trigger": input} so flow templates that reference the trigger render.
//
// opts may be nil for the original no-options behavior. When opts.Mocks has an
// entry for an executing node, that decoded value replaces the default stub and
// the trace entry is marked Mocked. After the run, opts.Assertions are
// evaluated against the trace+result and aggregated into Passed.
//
// An invalid flow (fails reasoning.CompileFlow) is an error; a valid flow
// always yields a TestResult. Mode=="live" is a valid request that runs
// nothing real and returns a clear not-supported note.
type triggerInput map[string]any

func (t triggerInput) String() string {
	if text, ok := t["text"].(string); ok {
		return text
	}
	return ""
}


func TestRun(ctx context.Context, draft Draft, input string, opts *TestOptions) (TestResult, error) {
	if opts == nil {
		opts = &TestOptions{}
	}
	mode := opts.Mode
	if mode == "" {
		mode = "dry"
	}

	// Compile up front so even a "live" request reports an invalid flow
	// (the caller's draft is malformed regardless of mode).
	g, err := reasoning.CompileFlow(draft.spec())
	if err != nil {
		return TestResult{}, fmt.Errorf("studio: test flow is invalid: %w", err)
	}

	// Safety: never execute a live, side-effecting run from an unsaved draft.
	if mode == "live" {
		return TestResult{
			Trace:      []TraceEntry{},
			Result:     nil,
			Assertions: []AssertionResult{},
			Passed:     true,
			Mode:       "live",
			Warnings:   []string{liveNotSupportedNote},
		}, nil
	}

	var warnings []string
	// Warn about mock entries that name nodes not present in the draft.
	if len(opts.Mocks) > 0 {
		known := map[string]bool{}
		for _, n := range draft.Flow.Nodes {
			known[n.ID] = true
		}
		for id := range opts.Mocks {
			if !known[id] {
				warnings = append(warnings, fmt.Sprintf("mock for unknown node id %q ignored", id))
			}
		}
	}

	var trace []TraceEntry
	mockedByNode := map[string]bool{}
	run := func(_ context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error) {
		out := mockNodeOutput(node, renderedInput)
		if raw, ok := opts.Mocks[node.ID]; ok && len(raw) > 0 {
			out = append(json.RawMessage(nil), raw...) // copy to decouple from caller's map
			mockedByNode[node.ID] = true
		}
		return out, nil
	}

	// Build the trace from the Observe hook so the dry run captures the same
	// per-block fields as a live run (duration, wired-ports), keeping a single
	// trace shape across dry and production.
	hooks := reasoning.FlowHooks{
		Observe: func(rec reasoning.FlowNodeRun) {
			trace = append(trace, TraceEntry{
				NodeID:     rec.NodeID,
				Kind:       rec.Kind,
				Input:      rec.Input,
				Output:     rec.Output,
				Mocked:     mockedByNode[rec.NodeID],
				DurationMS: rec.DurationMS,
				Error:      rec.Error,
				WiredPorts: rec.WiredPorts,
			})
		},
	}

	vars := map[string]any{
		"trigger": triggerInput{
			"text": input,
		},
	}
	result, err := reasoning.RunFlow(ctx, g, vars, run, hooks)
	if err != nil {
		return TestResult{}, fmt.Errorf("studio: test run: %w", err)
	}
	if trace == nil {
		trace = []TraceEntry{}
	}

	asserts := EvaluateAssertions(opts.Assertions, trace, result)
	passed := true
	for _, a := range asserts {
		if !a.Pass {
			passed = false
			break
		}
	}

	return TestResult{
		Trace:      trace,
		Result:     result,
		Assertions: asserts,
		Passed:     passed,
		Mode:       "dry",
		Warnings:   warnings,
		Shapes:     ShapesFromTrace(draft, trace),
	}, nil
}

// EvaluateAssertions is a pure function over a completed run's trace and final
// result. For each assertion it resolves the target value (a node's trace
// Output by id, or the final Result for the literal "result"), applies the op,
// and returns an AssertionResult. An always-non-nil slice is returned; with no
// assertions it is empty (and the caller treats Passed as true).
//
//   - "contains": stringified target output contains Value.
//   - "equals":   trimmed stringified target output equals trimmed Value.
//   - "exists":   the target executed and produced a non-empty output.
func EvaluateAssertions(assertions []Assertion, trace []TraceEntry, result json.RawMessage) []AssertionResult {
	out := make([]AssertionResult, 0, len(assertions))
	for _, a := range assertions {
		out = append(out, evalAssertion(a, trace, result))
	}
	return out
}

// evalAssertion evaluates one assertion. It is pure and self-contained so the
// whole evaluation pass is deterministic and testable.
func evalAssertion(a Assertion, trace []TraceEntry, result json.RawMessage) AssertionResult {
	res := AssertionResult{Target: a.Target, Op: a.Op, Value: a.Value}

	// Resolve the raw target output and whether the target executed at all.
	var raw json.RawMessage
	found := false
	if a.Target == "result" {
		raw = result
		found = true // the run always produces a result slot (possibly empty)
	} else {
		for _, e := range trace {
			if e.NodeID == a.Target {
				raw = e.Output
				found = true
				break
			}
		}
	}

	if !found {
		res.Pass = false
		res.Detail = fmt.Sprintf("target %q did not execute (no trace entry)", a.Target)
		return res
	}

	got := stringifyOutput(raw)

	switch a.Op {
	case "contains":
		res.Pass = strings.Contains(got, a.Value)
		if res.Pass {
			res.Detail = fmt.Sprintf("output of %q contains %q", a.Target, a.Value)
		} else {
			res.Detail = fmt.Sprintf("output of %q does not contain %q; got %s", a.Target, a.Value, got)
		}
	case "equals":
		res.Pass = strings.TrimSpace(got) == strings.TrimSpace(a.Value)
		if res.Pass {
			res.Detail = fmt.Sprintf("output of %q equals %q", a.Target, a.Value)
		} else {
			res.Detail = fmt.Sprintf("output of %q != %q; got %s", a.Target, a.Value, got)
		}
	case "exists":
		nonEmpty := len(strings.TrimSpace(got)) > 0 && got != "null"
		res.Pass = nonEmpty
		if res.Pass {
			res.Detail = fmt.Sprintf("target %q executed with non-empty output", a.Target)
		} else {
			res.Detail = fmt.Sprintf("target %q produced no output", a.Target)
		}
	default:
		res.Pass = false
		res.Detail = fmt.Sprintf("unknown assertion op %q (want contains|equals|exists)", a.Op)
	}
	return res
}

// stringifyOutput renders a node/flow output deterministically for assertion
// matching. A JSON string is unquoted to its plain text; any other JSON value
// is re-marshaled compactly (json.Compact strips insignificant whitespace) so
// the string form is stable across runs. Empty/nil renders as "".
func stringifyOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		// Not valid JSON (shouldn't happen for engine output) — use as-is.
		return string(raw)
	}
	return buf.String()
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
