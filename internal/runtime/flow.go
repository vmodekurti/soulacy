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
	g, err := reasoning.CompileFlow(*w.spec.FlowSpec())
	if err != nil {
		return nil, fmt.Errorf("workflow graph: %w", err)
	}
	agentID := msg.AgentID

	vars := map[string]interface{}{
		"trigger": triggerInput{
			"text": flattenParts(msg.Parts),
		},
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
					if err := json.Unmarshal([]byte(renderedInput), &tc.Arguments); err != nil {
						tc.Arguments["message"] = renderedInput
					}
				} else {
					tc.Arguments["message"] = renderedInput
				}
			} else {
				if err := json.Unmarshal([]byte(renderedInput), &tc.Arguments); err != nil {
					return nil, fmt.Errorf("workflow: tool args for %q not valid JSON: %w", tool, err)
				}
			}
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
	return out, nil
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
