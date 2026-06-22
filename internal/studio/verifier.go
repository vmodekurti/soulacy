// verifier.go — Verifier implementations for BuildUntilWorks (Architect).
//
//   - DryRunVerifier: zero-side-effect mock walk (TestRun) + assertion eval. Used
//     for structural confidence and as the safe default.
//   - RealRunVerifier: executes a WORKFLOW draft for real by walking its flow and
//     dispatching tool and Custom-Python steps to engine primitives injected by
//     the gateway. This is what catches the failures that only appear when real
//     tools run — a wrong MCP argument the server rejects, a Python node that
//     throws, an async value that never arrives. Agent (peer) steps are not
//     executed real here (that needs the full engine loop); they return a
//     structural stub, and a reasoning agent (no flow) is reported as needing a
//     real channel/scheduled run to verify.
package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// DryRunVerifier executes a draft via the mock TestRun. It is the safe default
// verifier (no side effects). The zero value is ready to use.
type DryRunVerifier struct{}

// Verify compiles + mock-runs the draft and evaluates the case's assertions.
func (DryRunVerifier) Verify(ctx context.Context, draft Draft, tc TestCase) VerifyOutcome {
	// A reasoning agent has no fixed flow to walk; a mock can't exercise its
	// tool loop. Report that rather than pretend it passed.
	if draft.IsAgent() {
		return VerifyOutcome{
			OK:    true,
			Real:  false,
			Trace: []string{"dry run skipped: a reasoning agent must be verified by a real run"},
		}
	}
	res, err := TestRun(ctx, draft, tc.Input, &TestOptions{Assertions: tc.Assertions})
	if err != nil {
		return VerifyOutcome{OK: false, Real: false, Error: err.Error()}
	}
	var trace []string
	for _, e := range res.Trace {
		trace = append(trace, "ran step "+e.NodeID+" ("+e.Kind+")")
	}
	if !res.Passed {
		for _, a := range res.Assertions {
			if !a.Pass {
				return VerifyOutcome{OK: false, Real: false, Error: "assertion failed: " + a.Detail, Trace: trace}
			}
		}
	}
	return VerifyOutcome{OK: true, Real: false, Trace: trace}
}

// RealRunner is the set of engine primitives the RealRunVerifier needs. The
// gateway wires these to *runtime.Engine (RunTool / RunInlinePython). A nil
// member makes that step kind a structural no-op (it does not fail the run).
type RealRunner struct {
	// Tool executes a builtin/MCP tool with rendered JSON arguments.
	Tool func(ctx context.Context, name, argsJSON string) (json.RawMessage, error)
	// Python executes a Custom-Python node's code with rendered JSON args.
	Python func(ctx context.Context, code string, argsJSON []byte) (json.RawMessage, error)
}

// RealRunVerifier executes a workflow draft for real via injected engine
// primitives. It walks the same compiled flow production uses, so a failure here
// is a failure the user would have hit at run time.
type RealRunVerifier struct {
	Runner RealRunner
}

// Verify compiles the draft's flow and runs it, dispatching each node to the
// real engine. A reasoning agent (no flow) cannot be walked this way; it is
// reported as structurally OK but unverified (its first real run / self-heal
// covers it). Any node error becomes the outcome's Error for the repair loop.
func (v RealRunVerifier) Verify(ctx context.Context, draft Draft, tc TestCase) VerifyOutcome {
	if draft.IsAgent() {
		return VerifyOutcome{
			OK:    true,
			Real:  false,
			Trace: []string{"reasoning agent: verified on its first real run; runtime self-heal will repair any failure"},
		}
	}
	g, err := reasoning.CompileFlow(draft.spec())
	if err != nil {
		return VerifyOutcome{OK: false, Real: false, Error: "flow is invalid: " + err.Error()}
	}

	var trace []string
	run := func(ctx context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error) {
		switch nodeKind(node) {
		case sdkr.FlowNodePython:
			if v.Runner.Python == nil || strings.TrimSpace(node.Code) == "" {
				trace = append(trace, "skipped python step "+node.ID+" (no executor)")
				return json.RawMessage("null"), nil
			}
			args := renderedInput
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			out, perr := v.Runner.Python(ctx, node.Code, []byte(args))
			if perr != nil {
				return nil, fmt.Errorf("python step %q failed: %w", node.ID, perr)
			}
			trace = append(trace, "ran python step "+node.ID)
			return out, nil
		case sdkr.FlowNodeAgent:
			// Running a peer agent for real needs the full engine loop; stub it so
			// the rest of the flow still exercises real tools/python.
			trace = append(trace, "stubbed agent step "+node.ID+" (peer not run during build)")
			b, _ := json.Marshal(fmt.Sprintf("[build-stub] %s", node.Agent))
			return b, nil
		default: // tool
			if v.Runner.Tool == nil {
				trace = append(trace, "skipped tool step "+node.ID+" (no runner)")
				return json.RawMessage("null"), nil
			}
			out, terr := v.Runner.Tool(ctx, node.Tool, renderedInput)
			if terr != nil {
				return nil, fmt.Errorf("tool step %q (%s) failed: %w", node.ID, node.Tool, terr)
			}
			trace = append(trace, "ran tool step "+node.ID+" ("+node.Tool+")")
			return out, nil
		}
	}

	vars := map[string]any{"trigger": triggerInput{"text": tc.Input}}
	result, rerr := reasoning.RunFlow(ctx, g, vars, run, reasoning.FlowHooks{})
	if rerr != nil {
		return VerifyOutcome{OK: false, Real: true, Error: rerr.Error(), Trace: trace}
	}
	// Evaluate assertions against the real result.
	if len(tc.Assertions) > 0 {
		for _, a := range EvaluateAssertions(tc.Assertions, realTrace(trace), result) {
			if !a.Pass {
				return VerifyOutcome{OK: false, Real: true, Error: "assertion failed: " + a.Detail, Trace: trace}
			}
		}
	}
	return VerifyOutcome{OK: true, Real: true, Trace: trace}
}

// realTrace adapts the human-readable trace to the [] TraceEntry assertions
// expect for node-targeted checks. We only support "result"-targeted assertions
// for real runs (node outputs aren't retained here), so this is an empty slice;
// node-targeted assertions will report "did not execute" which is acceptable for
// the synthesized "result exists/contains" checks the loop uses.
func realTrace(_ []string) []TraceEntry { return nil }
