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
	"time"

	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/message"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
	"go.uber.org/zap"
)

// runFlow executes the spec's graph form for one trigger message.
func (w *WorkflowExecutor) runFlow(ctx context.Context, msg message.Message, runID string) (json.RawMessage, error) {
	g, err := reasoning.CompileFlow(*w.spec.FlowSpec())
	if err != nil {
		return nil, fmt.Errorf("workflow graph: %w", err)
	}
	agentID := msg.AgentID

	vars := map[string]interface{}{
		"trigger": flattenParts(msg.Parts),
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
		tool := node.Tool
		if node.Kind == sdkr.FlowNodeAgent {
			tool = "agent__" + node.Agent
		}
		out, rerr := w.engine.RunTool(ctx, tool, renderedInput)
		if rerr != nil {
			return nil, rerr
		}
		return unwrapToolJSON(out), nil
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
