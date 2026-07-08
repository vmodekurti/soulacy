package reasoning

import (
	"encoding/json"
	"strings"
)

// This guard prevents the reasoning loop's internal control JSON (the
// thought/action/is_done step object, or a raw Reflect body) from ever
// surfacing as the user-facing reply. It happens when a model — often a smaller
// or local one — doesn't emit a clean final answer and the backend falls back to
// returning its raw output. SanitizeFinalOutput turns such output into a real
// answer (extracted from the control JSON when possible) or a graceful message,
// keeping the raw trace in the Thinking panel where it belongs.

// controlJSONShape captures the fields the loop uses internally. If a purported
// final answer decodes into this shape, it's leaked control JSON, not an answer.
type controlJSONShape struct {
	Thought     string          `json:"thought"`
	IsDone      *bool           `json:"is_done"`
	Action      json.RawMessage `json:"action"`
	FinalAnswer string          `json:"final_answer"`
	Output      string          `json:"output"`
}

// SanitizeFinalOutput returns a clean, user-facing answer for a reasoning run.
// If output is a real answer it is returned unchanged. If it is leaked control
// JSON, the embedded final_answer/output is extracted; failing that, a graceful
// message is returned. Empty output also yields the graceful message.
func SanitizeFinalOutput(output string, steps []Step) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return gracefulFallback(steps)
	}

	body := stripJSONFence(trimmed)
	if shape, ok := decodeControlJSON(body); ok {
		// It decoded into the control shape — treat it as leaked control JSON and
		// pull out any real answer the model did include.
		if ans := firstNonEmpty(shape.FinalAnswer, shape.Output); strings.TrimSpace(ans) != "" {
			return strings.TrimSpace(ans)
		}
		return gracefulFallback(steps)
	}
	return output
}

// decodeControlJSON reports whether s is a JSON object carrying the loop's
// control fields (thought/action/is_done) — i.e. leaked internal state rather
// than an answer. A bare {"output": "..."} or plain prose is NOT control JSON.
func decodeControlJSON(s string) (controlJSONShape, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return controlJSONShape{}, false
	}
	var shape controlJSONShape
	if err := json.Unmarshal([]byte(s), &shape); err != nil {
		// Malformed JSON that still smells like control output (common when a
		// model appends prose) — sniff for the tell-tale keys so we don't render
		// it verbatim.
		low := strings.ToLower(s)
		if strings.Contains(low, `"thought"`) &&
			(strings.Contains(low, `"action"`) || strings.Contains(low, `"is_done"`)) {
			return controlJSONShape{}, true
		}
		return controlJSONShape{}, false
	}
	isControl := shape.Thought != "" || shape.IsDone != nil || len(shape.Action) > 0
	return shape, isControl
}

// gracefulFallback derives a readable answer when no clean one is available:
// the most recent non-empty tool observation if it looks human-readable,
// otherwise a short message pointing at the reasoning trace.
func gracefulFallback(steps []Step) string {
	for i := len(steps) - 1; i >= 0; i-- {
		obs := strings.TrimSpace(steps[i].Obs.Content)
		if obs == "" {
			continue
		}
		if looksReadable(obs) {
			return obs
		}
		break
	}
	return "I worked through several reasoning steps but couldn't produce a clean final answer. Open the reasoning trace above to see what happened, or ask me to continue."
}

// looksReadable rejects observations that are themselves control JSON, HTML, or
// otherwise unsuitable to show as a final answer.
func looksReadable(s string) bool {
	if _, isControl := decodeControlJSON(s); isControl {
		return false
	}
	if len(s) > 4000 {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(s), "<") && strings.Contains(s, "</") {
		return false // looks like HTML
	}
	return true
}

// stripJSONFence removes a surrounding ```json … ``` (or bare ``` … ```) fence.
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		// drop an optional language tag on the first line (e.g. "json")
		if lang := strings.TrimSpace(s[:i]); lang == "" || !strings.ContainsAny(lang, " {}\"") {
			s = s[i+1:]
		}
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
