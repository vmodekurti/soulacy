package studio

import (
	"regexp"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// tmplActionRe captures the body of each {{ … }} action so a reference can be
// judged in context (e.g. whether it's wrapped in an object-coercing helper).
var tmplActionRe = regexp.MustCompile(`\{\{(.*?)\}\}`)

// tmplPathRe captures a FULL dotted flow-var path inside a template expression:
// `.notebook` → "notebook", `.notebook.id` → "notebook.id",
// `.notebook.notebook` → "notebook.notebook". Unlike flowdeps' tmplVarRe (which
// keeps only the leading identifier for dependency checks) this preserves the
// whole chain so we can reason about nested-object vs scalar access.
var tmplPathRe = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)`)

// objectCoercers are template helpers that legitimately consume a whole object
// or array (so a bare reference inside one is intentional, not a bug). Matches
// the helper set registered by the flow renderer (reasoning.FlowTemplateFuncs).
var objectCoercers = []string{"toJson", "json", "pluck"}

// checkTemplateReferences catches a class of bug the syntax + dependency checks
// miss: a template that interpolates a whole STRUCTURED value (an object/array
// produced by an earlier step) where a scalar is expected. Go's text/template
// renders a map as "map[id:… title:…]" — not JSON — so a step that writes
// {{ .notebook }} (or the wrong nested path {{ .notebook.notebook }}) feeds a
// malformed string like "map[id:real-uuid title:…]" to downstream tools
// (add_sources, studio_create, studio_status) instead of the bare id.
//
// It is heuristic but conservative: it only flags references to vars PRODUCED by
// a step (dangling refs are left to checkDataFlow) that are known/likely to be
// structured, and it offers the concrete fix (reference a scalar field, or use
// toJson for the whole object). Warnings only — never blocks a save.
func checkTemplateReferences(draft Draft, add func(sev, kind, node, msg, fix string)) {
	nodes := draft.Flow.Nodes
	if len(nodes) == 0 {
		return
	}

	// producer: output var name -> the node that produces it.
	producer := map[string]sdkr.FlowNode{}
	for _, n := range nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n
		}
	}

	// A produced var is definitely an object if it is accessed with a field
	// (`.X.field`) ANYWHERE in the flow — that's a schema-free proof of shape.
	accessedWithField := map[string]bool{}
	for _, n := range nodes {
		for _, path := range templatePaths(n.Input) {
			if segs := strings.Split(path, "."); len(segs) >= 2 {
				accessedWithField[segs[0]] = true
			}
		}
	}

	for _, n := range nodes {
		if !strings.Contains(n.Input, "{{") {
			continue
		}
		seen := map[string]bool{} // dedupe messages per node
		for _, action := range templateActions(n.Input) {
			coerced := mentionsAny(action, objectCoercers)
			for _, path := range templatePaths(action) {
				segs := strings.Split(path, ".")
				root := segs[0]
				prod, ok := producer[root]
				if !ok || prod.ID == n.ID {
					continue // dangling (checkDataFlow) or self-ref
				}

				// Case B: a repeated adjacent segment like `.notebook.notebook`
				// almost always means the author drilled into the object's own
				// nested copy instead of the scalar field they wanted.
				if hasRepeatedAdjacentSegment(segs) {
					if key := "B:" + path; !seen[key] {
						seen[key] = true
						add("warn", "template", n.ID,
							"This step references {{ ."+path+" }}, which points at a nested object, not a single value — it typically renders as a Go map like \"map[id:… title:…]\" instead of the value you want.",
							"Reference the exact scalar field instead, e.g. {{ ."+root+".id }}.")
					}
					continue
				}

				// Case A: a bare whole-object interpolation ({{ .notebook }})
				// of a structured value, with no coercing helper, gets rendered
				// as "map[…]" and corrupts the downstream JSON.
				structured := accessedWithField[root] || producerEmitsObject(prod)
				if len(segs) == 1 && structured && !coerced {
					if key := "A:" + root; !seen[key] {
						seen[key] = true
						add("warn", "template", n.ID,
							"This step passes the whole \""+root+"\" object via {{ ."+root+" }}. An object renders as \"map[…]\", not JSON, so the downstream step receives a malformed value.",
							"Pass a scalar field like {{ ."+root+".id }}, or use {{ toJson ."+root+" }} to send the whole object as JSON.")
					}
				}
			}
		}
	}
}

// templateActions returns the inner text of each {{ … }} action in s.
func templateActions(s string) []string {
	if !strings.Contains(s, "{{") {
		return nil
	}
	var out []string
	for _, m := range tmplActionRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// templatePaths returns the distinct dotted flow-var paths referenced in s
// (e.g. "notebook", "notebook.id"). Returns nil for non-template strings.
func templatePaths(s string) []string {
	if !strings.Contains(s, "{{") && !strings.Contains(s, ".") {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range tmplPathRe.FindAllStringSubmatch(s, -1) {
		p := m[1]
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// producerEmitsObject reports whether a node's output is likely a structured
// object/array (so interpolating it bare would render as "map[…]"). Conservative
// to avoid false positives: agent nodes return text (scalar), python nodes and
// MCP tools return structured JSON; unknown builtin tools are treated as scalar.
func producerEmitsObject(n sdkr.FlowNode) bool {
	switch strings.TrimSpace(n.Kind) {
	case sdkr.FlowNodeAgent:
		return false
	case sdkr.FlowNodePython:
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(n.Tool), "mcp__")
}

// hasRepeatedAdjacentSegment reports whether a dotted path repeats a segment
// back-to-back (e.g. notebook.notebook) — a strong signal of a wrong nested ref.
func hasRepeatedAdjacentSegment(segs []string) bool {
	for i := 1; i < len(segs); i++ {
		if segs[i] == segs[i-1] && segs[i] != "" {
			return true
		}
	}
	return false
}

// mentionsAny reports whether s contains any of the given substrings.
func mentionsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
