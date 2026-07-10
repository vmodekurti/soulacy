package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// flowadapt.go keeps a flow RUNNING through surprises. When an adaptive node
// fails — or "succeeds" but its output reports an error — because a real tool/API
// returned a shape it didn't expect, the runtime asks the model to produce the
// node's intended output from the ACTUAL input, so downstream nodes get usable
// data instead of the flow aborting. This is the on-the-fly counterpart to the
// post-run repair engine: repair fixes the node's code/template for next time;
// adaptation salvages THIS run. Bounded to one attempt per node, and only for
// shape/format surprises — never auth/network/consent failures, which are real.

// adaptFlowNode attempts an LLM salvage for a failed/soft-failed node. It returns
// (salvagedOutput, true) when it produced usable output, or (_, false) to leave
// the original result untouched. Never itself returns an error — a failed
// salvage just declines.
func (e *Engine) adaptFlowNode(ctx context.Context, msg message.Message, node sdkr.FlowNode, renderedInput string, prevOut json.RawMessage, prevErr error) (json.RawMessage, bool) {
	if e.llmRouter == nil {
		return nil, false
	}
	// What went wrong? A hard error, or a soft error the node reported in output.
	reason := ""
	if prevErr != nil {
		reason = prevErr.Error()
	} else if s := flowSoftError(prevOut); s != "" {
		reason = s
	}
	if reason == "" || !isAdaptableFailure(reason) {
		return nil, false
	}

	def := &agent.Definition{}
	if e.loader != nil {
		if d := e.loader.Get(msg.AgentID); d != nil {
			def = d
		}
	}

	prompt := buildAdaptPrompt(node, renderedInput, reason, prevOut)
	req := llm.CompletionRequest{
		Model:          def.LLM.Model,
		Temperature:    def.LLM.Temperature,
		ResponseFormat: "json",
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a resilient workflow step. The upstream data came back in an unexpected shape. Extract/produce this step's intended output from the ACTUAL input. Reply with ONLY the JSON value this step should output — no prose, no code fences."},
			{Role: "user", Content: prompt},
		},
	}
	if e.sink != nil {
		e.sink.Emit(message.Event{
			Type: "flow.adapt", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload:   map[string]any{"node": node.ID, "reason": truncateReason(reason)},
			Timestamp: time.Now().UTC(),
		})
	}
	resp, err := e.llmRouter.Complete(ctx, def.LLM.Provider, req)
	if err != nil || resp == nil {
		return nil, false
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return nil, false
	}
	// Prefer valid JSON; otherwise wrap as a JSON string so downstream vars stay
	// well-typed. Reject a salvage that still reports an error.
	if json.Valid([]byte(out)) {
		raw := json.RawMessage(out)
		if flowSoftError(raw) != "" {
			return nil, false
		}
		return raw, true
	}
	b, _ := json.Marshal(out)
	return b, true
}

// buildAdaptPrompt describes the node's job and hands the model the real input.
func buildAdaptPrompt(node sdkr.FlowNode, renderedInput, reason string, prevOut json.RawMessage) string {
	var sb strings.Builder
	sb.WriteString("Workflow step id: " + node.ID + " (kind: " + node.Kind + ")\n")
	if d := strings.TrimSpace(node.Description); d != "" {
		sb.WriteString("What this step is supposed to do: " + d + "\n")
	} else if in := strings.TrimSpace(node.Intent); in != "" {
		sb.WriteString("What this step is supposed to do: " + in + "\n")
	}
	if node.Kind == string(sdkr.FlowNodePython) && strings.TrimSpace(node.Code) != "" {
		sb.WriteString("\nThe step's python code (for intent; it failed on the real input):\n" + node.Code + "\n")
	}
	sb.WriteString("\nIt failed with: " + truncateReason(reason) + "\n")
	sb.WriteString("\nThe ACTUAL input it received (parse THIS real shape — it may be a string, may have headers/prefix before JSON, may nest differently):\n")
	sb.WriteString(truncateRunes2(renderedInput, 6000) + "\n")
	if len(prevOut) > 0 {
		sb.WriteString("\nThe (wrong) output it produced: " + truncateRunes2(string(prevOut), 800) + "\n")
	}
	sb.WriteString("\nReturn ONLY the correct JSON output for this step, derived from the actual input above.")
	return sb.String()
}

// flowSoftError reports an error a node put in its own JSON output (top-level
// error/errors/err string) despite not raising — the soft-failure signal.
func flowSoftError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"error", "errors", "err"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// isAdaptableFailure is true for shape/format surprises and false for real
// failures (auth, rate-limit, network, consent, timeout) that salvage can't fix.
func isAdaptableFailure(reason string) bool {
	r := strings.ToLower(reason)
	for _, s := range []string{
		"unauthorized", "forbidden", "401", "403", "429", "rate limit",
		"no such host", "connection refused", "timeout", "deadline exceeded",
		"consent", "not permitted", "permission",
	} {
		if strings.Contains(r, s) {
			return false
		}
	}
	return true
}

func truncateReason(s string) string { return truncateRunes2(s, 300) }

func truncateRunes2(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
