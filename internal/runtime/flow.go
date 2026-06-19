// flow.go — Story E25: the graph form of workflows, compiled onto the
// existing WorkflowExecutor + CheckpointStore.
//
// Checkpoint keys are visit-indexed ("<node>#<visit>") so bounded cycles
// persist each iteration separately; resume restores completed visits in
// walk order and recomputes edge decisions from the restored vars — the
// deterministic predicates take the same path, so execution continues
// exactly where the crash happened.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/studio/consent"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
	"go.uber.org/zap"
)

// runFlow executes the spec's graph form for one trigger message.
type triggerInput map[string]any

func (t triggerInput) String() string {
	if text, ok := t["text"].(string); ok {
		return text
	}
	return ""
}

func (w *WorkflowExecutor) runFlow(ctx context.Context, msg message.Message, runID string) (json.RawMessage, error) {
	spec := w.spec.FlowSpec()
	g, err := reasoning.CompileFlow(*spec)
	if err != nil {
		return nil, fmt.Errorf("workflow graph: %w", err)
	}
	agentID := msg.AgentID

	// Conversation continuity: pull a compact transcript of recent turns for
	// this session so the flow's entry agent can resolve follow-up references
	// ("its price", "that company") without the user restating context. Empty
	// on the first turn. Exposed to every node as {{.history}} and auto-prepended
	// to the entry agent's message below.
	history := w.engine.flowHistoryTranscript(msg.SessionID, msg.AgentID, flowHistoryMaxMsgs)
	entryID := spec.Entry
	if entryID == "" {
		if len(spec.Nodes) > 0 {
			entryID = spec.Nodes[0].ID
		}
	}

	vars := map[string]interface{}{
		"trigger": triggerInput{
			"text": flattenParts(msg.Parts),
		},
		"history": history,
	}

	hooks := reasoning.FlowHooks{}
	if w.store != nil {
		hooks.Restore = func(visitKey string) (json.RawMessage, bool) {
			cp, gerr := w.store.Get(ctx, agentID, runID, visitKey)
			if gerr != nil || cp.Status != CheckpointCompleted {
				return nil, false
			}
			w.log.Debug("flow visit already completed, restoring",
				zap.String("run_id", runID), zap.String("visit", visitKey))
			return cp.State, true
		}
		hooks.Started = func(visitKey string, node sdkr.FlowNode) {
			_ = w.store.Upsert(ctx, Checkpoint{
				AgentID: agentID, RunID: runID, StepID: visitKey,
				Status: CheckpointInProgress, UpdatedAt: time.Now().UTC(),
			})
		}
		hooks.Completed = func(visitKey string, state json.RawMessage) {
			_ = w.store.Upsert(ctx, Checkpoint{
				AgentID: agentID, RunID: runID, StepID: visitKey,
				Status: CheckpointCompleted, State: state, UpdatedAt: time.Now().UTC(),
			})
		}
		hooks.Failed = func(visitKey string, ferr error) {
			_ = w.store.Upsert(ctx, Checkpoint{
				AgentID: agentID, RunID: runID, StepID: visitKey,
				Status: CheckpointFailed, UpdatedAt: time.Now().UTC(),
			})
		}
	}

	runNode := func(ctx context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error) {
		// Studio "Custom Python" node with inline code: execute it in the
		// sandboxed Python executor. The rendered input is delivered as the
		// run(inputs) argument. (A python node that instead references a deployed
		// tool — Code empty, Tool set — falls through to the RunTool path.)
		if node.Kind == sdkr.FlowNodePython && node.Code != "" {
			// Fail-closed per-case consent (§13): refuse to run code beyond the
			// ReadOnly guardrails unless the node carries a matching, valid
			// consent stamp and the operator ceiling permits it.
			def := w.engine.loader.Get(msg.AgentID)
			if cerr := consent.Authorize(node, w.engine.IsSystemAgentAllowed(def)); cerr != nil {
				return nil, cerr
			}
			return w.engine.RunInlinePython(ctx, node.Code, []byte(renderedInput))
		}
		tool := node.Tool
		if node.Kind == sdkr.FlowNodeAgent {
			tool = "agent__" + node.Agent
		}

		tc := message.ToolCall{
			ID:        "flow-" + node.ID,
			Name:      tool,
			Arguments: map[string]any{},
		}
		if renderedInput != "" {
			if node.Kind == sdkr.FlowNodeAgent {
				if strings.HasPrefix(strings.TrimSpace(renderedInput), "{") {
					if args, perr := parseToolArgs(renderedInput); perr == nil {
						tc.Arguments = args
					} else {
						tc.Arguments["message"] = renderedInput
					}
				} else {
					tc.Arguments["message"] = renderedInput
				}
			} else {
				args, perr := parseToolArgs(renderedInput)
				if perr != nil {
					return nil, fmt.Errorf("workflow: tool args for %q not valid JSON: %w", tool, perr)
				}
				tc.Arguments = args
			}
		}

		// Entry agent gets the conversation history prepended to its message so
		// follow-ups resolve against prior turns. Only the entry node — downstream
		// agents act on this turn's extracted intent, not the raw transcript.
		if node.Kind == sdkr.FlowNodeAgent && node.ID == entryID && history != "" {
			base, _ := tc.Arguments["message"].(string)
			if base == "" {
				base = flattenParts(msg.Parts)
			}
			tc.Arguments["message"] = "Conversation so far (context for resolving follow-up references; " +
				"do not answer it directly):\n" + history + "\n\nUser's new message: " + base
		}

		var def *agent.Definition
		if w.engine.loader != nil {
			def = w.engine.loader.Get(msg.AgentID)
		}
		if def == nil {
			def = &agent.Definition{}
		}
		outStr, rerr := w.engine.runTool(ctx, def, msg.SessionID, tc)
		if rerr != nil {
			return nil, rerr
		}

		outBytes := []byte(outStr)
		if json.Valid(outBytes) {
			return unwrapToolJSON(outBytes), nil
		}
		wrapped, _ := json.Marshal(outStr)
		return json.RawMessage(wrapped), nil
	}

	out, err := reasoning.RunFlow(ctx, g, vars, runNode, hooks)
	if err != nil {
		w.log.Error("workflow flow failed",
			zap.String("run_id", runID), zap.Error(err))
		return nil, err
	}

	// If the graph designates an explicit output node, its result becomes the
	// flow's final output (what gets delivered to channels) — rather than the
	// last-executed node's. RunFlow mutates `vars` in place, so each node's
	// output is available here by its output-var name. Falls back to `out` when
	// the output node has no var or produced nothing.
	if outID := spec.Output; outID != "" {
		for _, n := range spec.Nodes {
			if n.ID == outID && n.Output != "" {
				if v, ok := vars[n.Output]; ok {
					if b, jerr := json.Marshal(v); jerr == nil {
						return json.RawMessage(b), nil
					}
				}
			}
		}
	}
	return out, nil
}

// parseToolArgs unmarshals a flow node's rendered input into a tool-args map.
// Node inputs are produced by text/template substitution (e.g. {{.search_query}}
// inside a JSON template), so an upstream agent's output containing a raw newline
// lands unescaped inside a JSON string literal and breaks json.Unmarshal. When
// the first parse fails, we escape bare control characters inside string literals
// and retry once before giving up — returning the ORIGINAL error so the message
// reflects the real defect, not the repaired text.
func parseToolArgs(in string) (map[string]any, error) {
	args := map[string]any{}
	if err := json.Unmarshal([]byte(in), &args); err == nil {
		return args, nil
	} else if repaired := repairJSONControlChars(in); repaired != in {
		args = map[string]any{}
		if err2 := json.Unmarshal([]byte(repaired), &args); err2 == nil {
			return args, nil
		}
		return nil, err
	} else {
		return nil, err
	}
}

// repairJSONControlChars escapes literal newline, carriage-return, and tab
// characters that appear INSIDE JSON string literals (which are invalid JSON).
// Control characters outside string literals — the structural whitespace between
// tokens — are left untouched. Returns the input unchanged when no repair was
// needed, so callers can detect a no-op.
func repairJSONControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	esc := false
	changed := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inStr {
			if c == '"' {
				inStr = true
			}
			b.WriteByte(c)
			continue
		}
		if esc {
			b.WriteByte(c)
			esc = false
			continue
		}
		switch c {
		case '\\':
			b.WriteByte(c)
			esc = true
		case '"':
			b.WriteByte(c)
			inStr = false
		case '\n':
			b.WriteString(`\n`)
			changed = true
		case '\r':
			b.WriteString(`\r`)
			changed = true
		case '\t':
			b.WriteString(`\t`)
			changed = true
		default:
			b.WriteByte(c)
		}
	}
	if !changed {
		return s
	}
	return b.String()
}

// unwrapToolJSON undoes RunTool's string wrapping when the tool's textual
// output is itself a JSON document: `"{\"ok\":true}"` → `{"ok":true}`.
// Edge predicates and node input templates can then address the fields
// ({{.verdict.ok}}). Non-JSON text stays a JSON string.
func unwrapToolJSON(raw json.RawMessage) json.RawMessage {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return raw // already an object/array/number — pass through
	}
	if json.Valid([]byte(s)) && len(s) > 0 {
		return json.RawMessage(s)
	}
	return raw
}
