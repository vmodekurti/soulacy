// validate.go — Studio's "validate" step (Story M3). The canvas calls this
// while the user edits a draft graph to get fast, structured feedback: hard
// errors (the graph won't compile) and soft warnings (the graph compiles but
// has a smell, e.g. an unreachable node or an unknown trigger type).
//
// The logic lives here (not in the gateway) so it is unit-testable without an
// HTTP server: POST /api/v1/studio/validate is a thin wrapper over Validate.
// Validate NEVER returns a Go error — a bad graph is data, reported via the
// result's Errors. This is the deterministic contract the frontend pins:
//
//	validate.request{workflow} ->
//	  validate.response{ ok, errors[{nodeId?,edgeIndex?,message}], warnings[{nodeId?,message}] }
package studio

import (
	"fmt"
	"strings"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
)

// ValidateError is one hard problem that stops the graph compiling. NodeID
// and/or EdgeIndex locate it on the canvas when they can be attributed; the
// Message is always set. EdgeIndex is a pointer so 0 ("edge 0") is
// distinguishable from "not an edge problem" (nil).
type ValidateError struct {
	NodeID    string `json:"nodeId,omitempty"`
	EdgeIndex *int   `json:"edgeIndex,omitempty"`
	Message   string `json:"message"`
}

// ValidateWarning is one soft problem: the graph still compiles, but the
// shape is suspicious. NodeID locates it when applicable.
type ValidateWarning struct {
	NodeID  string `json:"nodeId,omitempty"`
	Message string `json:"message"`
}

// ValidateResult is the POST /api/v1/studio/validate response. Ok is true iff
// there are no hard errors; warnings never affect Ok.
type ValidateResult struct {
	Ok       bool              `json:"ok"`
	Errors   []ValidateError   `json:"errors"`
	Warnings []ValidateWarning `json:"warnings"`
}

// knownTriggerTypes is the closed set the compiler/normalizer recognizes; any
// other (non-empty) trigger.type earns a soft warning.
var knownTriggerTypes = map[string]bool{
	"schedule": true, "channel": true, "webhook": true, "manual": true,
}

// Validate runs reasoning.CompileFlow on the draft's flow and collects the
// outcome as structured data. It never returns an error: a graph that fails
// to compile yields Ok=false with one (best-attributed) ValidateError; a
// graph that compiles yields Ok=true plus any soft warnings.
//
// Errors (hard — block compile):
//   - whatever CompileFlow rejects (missing ids, dangling/bad-target edges,
//     undeclared ports, bad entry, unknown kind/on_error, …), attributed to a
//     node or edge when the message lets us.
//
// Warnings (soft — graph compiles):
//   - a node with no incoming edge that is not the entry (unreachable);
//   - an unknown/unrecognized trigger.type.
func Validate(draft Draft) ValidateResult {
	res := ValidateResult{
		Ok:       true,
		Errors:   []ValidateError{},
		Warnings: []ValidateWarning{},
	}

	if _, err := reasoning.CompileFlow(draft.spec()); err != nil {
		res.Ok = false
		res.Errors = append(res.Errors, attributeFlowError(draft.Flow, err))
		// A graph that doesn't compile: report the hard error only. Soft
		// warnings about reachability are unreliable on an invalid graph.
		return res
	}

	res.Warnings = append(res.Warnings, flowWarnings(draft.Flow)...)
	res.Warnings = append(res.Warnings, triggerWarnings(draft.Trigger)...)
	return res
}

// attributeFlowError turns a CompileFlow error into a structured ValidateError,
// best-effort attaching a nodeId or edgeIndex parsed from the message. The
// message text is the source of truth (CompileFlow's messages are stable and
// quote the offending id/index); when nothing can be attributed we still
// surface the precise message.
func attributeFlowError(flow Flow, err error) ValidateError {
	msg := err.Error()
	ve := ValidateError{Message: msg}

	// Edge-shaped errors: "flow: edge <i> ...". Pull the index out so the
	// canvas can highlight the offending edge.
	if idx, ok := parseEdgeIndex(msg); ok {
		ve.EdgeIndex = &idx
		// Edge errors that quote a target/source node id also carry a nodeId.
		if id := firstQuotedKnownNode(msg, flow); id != "" {
			ve.NodeID = id
		}
		return ve
	}

	// Node-shaped errors: "flow: node <i> ..." or "flow: node \"id\" ...".
	if id := firstQuotedKnownNode(msg, flow); id != "" {
		ve.NodeID = id
		return ve
	}
	// "flow: entry node \"x\" does not exist" quotes an id that is (by
	// definition) NOT a known node — still surface it as the nodeId so the
	// canvas can point at the entry selector.
	if strings.Contains(msg, "entry node") {
		if id := firstQuoted(msg); id != "" {
			ve.NodeID = id
		}
	}
	return ve
}

// parseEdgeIndex extracts <i> from "flow: edge <i> ..." messages.
func parseEdgeIndex(msg string) (int, bool) {
	const marker = "edge "
	i := strings.Index(msg, marker)
	if i < 0 {
		return 0, false
	}
	rest := msg[i+len(marker):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0, false
	}
	n := 0
	for k := 0; k < j; k++ {
		n = n*10 + int(rest[k]-'0')
	}
	return n, true
}

// firstQuotedKnownNode returns the first double-quoted token in msg that names
// a node actually present in the flow (so we attribute to a real node, not to
// a tool name or a missing-entry id).
func firstQuotedKnownNode(msg string, flow Flow) string {
	ids := map[string]bool{}
	for _, n := range flow.Nodes {
		ids[n.ID] = true
	}
	rest := msg
	for {
		q := firstQuoted(rest)
		if q == "" {
			return ""
		}
		if ids[q] {
			return q
		}
		// Advance past this quoted token and keep looking.
		idx := strings.Index(rest, `"`+q+`"`)
		if idx < 0 {
			return ""
		}
		rest = rest[idx+len(q)+2:]
	}
}

// firstQuoted returns the contents of the first "double-quoted" token in s, or
// "" when there is none.
func firstQuoted(s string) string {
	i := strings.IndexByte(s, '"')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i+1:], '"')
	if j < 0 {
		return ""
	}
	return s[i+1 : i+1+j]
}

// flowWarnings flags nodes that have no incoming edge yet are not the entry —
// they are unreachable, which usually means the author forgot to wire them.
func flowWarnings(flow Flow) []ValidateWarning {
	if len(flow.Nodes) == 0 {
		return nil
	}
	entry := flow.Entry
	if entry == "" {
		entry = flow.Nodes[0].ID
	}
	hasIncoming := map[string]bool{}
	for _, e := range flow.Edges {
		if !flowTerminal(e.To) {
			hasIncoming[e.To] = true
		}
	}
	var out []ValidateWarning
	for _, n := range flow.Nodes {
		if n.ID == entry {
			continue
		}
		if !hasIncoming[n.ID] {
			out = append(out, ValidateWarning{
				NodeID:  n.ID,
				Message: fmt.Sprintf("node %q has no incoming edge and is not the entry node (unreachable)", n.ID),
			})
		}
	}
	return out
}

// triggerWarnings flags a non-empty trigger.type the compiler does not
// recognize. An empty type is left to the compiler's clarify flow (not a
// validate-time warning).
func triggerWarnings(t Trigger) []ValidateWarning {
	typ := strings.ToLower(strings.TrimSpace(t.Type))
	if typ == "" || knownTriggerTypes[typ] {
		return nil
	}
	return []ValidateWarning{{
		Message: fmt.Sprintf("unknown trigger type %q (expected one of: schedule, channel, webhook, manual)", t.Type),
	}}
}

// flowTerminal mirrors reasoning's terminal-edge check ("" or "end").
func flowTerminal(to string) bool { return to == "" || to == "end" }
