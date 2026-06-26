package studio

import (
	"regexp"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// tmplVarRe matches a flow-variable reference inside a Go-template expression:
// the identifier immediately after a dot, e.g. `.articles` in
// `{{ toJson .articles }}` or `{{ .notebook_id }}`. It deliberately ignores
// method-style chains (only the leading identifier matters for our purpose).
var tmplVarRe = regexp.MustCompile(`(?:{{|\s)\.([A-Za-z_][A-Za-z0-9_]*)`)

// checkDataFlow validates cross-step data dependencies (Story #3): every flow
// variable a node references in its Input template must be PRODUCED by another
// node, and that producer must run BEFORE the consumer. A reference to a var no
// node outputs is a blocker (dangling); a reference to a var produced only by a
// non-ancestor (so it isn't guaranteed to exist when this node runs) is a
// warning. Pure + deterministic; appended to a PreflightResult by Preflight.
//
// This catches the broken-sequence class the per-node required-arg check can't:
// e.g. an "add sources" node templating {{ .notebook_id }} when the
// notebook-creating node runs AFTER it (or doesn't output notebook_id at all).
func checkDataFlow(draft Draft, add func(sev, kind, node, msg, fix string)) {
	nodes := draft.Flow.Nodes
	if len(nodes) == 0 {
		return
	}

	// producer: output var -> node id that produces it.
	producer := map[string]string{}
	for _, n := range nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n.ID
		}
	}

	// Build ancestor sets via the edges (who can run before whom).
	anc := ancestors(draft.Flow.Edges, nodes)

	for _, n := range nodes {
		for _, v := range referencedVars(n.Input) {
			prod, ok := producer[v]
			if !ok {
				fix := "Add a step that outputs \"" + v + "\" before this one, or fix the variable name."
				if strings.Contains(n.Input, "range ") || v == "url" || v == "id" {
					fix += " If you are trying to extract a list of fields from an array of objects, use {{ pluck \"" + v + "\" .upstream_var | toJson }} instead of loops."
				}
				add("block", "dependency", n.ID,
					"Step references {{ ."+v+" }} but no earlier step produces \""+v+"\".",
					fix)
				continue
			}
			if prod == n.ID {
				continue // self-reference (unusual but not a missing dep)
			}
			if a := anc[n.ID]; a != nil && !a[prod] {
				add("warn", "dependency", n.ID,
					"Step uses {{ ."+v+" }} from \""+prod+"\", which is not guaranteed to run before it.",
					"Wire an edge so \""+prod+"\" runs before \""+n.ID+"\".")
			}
		}
	}
}

// referencedVars extracts the distinct flow-var identifiers referenced in a
// node's input template. Returns nil for non-template inputs.
func referencedVars(input string) []string {
	if !strings.Contains(input, "{{") {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range tmplVarRe.FindAllStringSubmatch(input, -1) {
		v := m[1]
		// Skip template builtins that can follow a dot in rare cases; the common
		// ones (toJson, len, gt) appear WITHOUT a leading dot, so this is mostly
		// a guard against noise.
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ancestors returns, for each node id, the set of node ids that can run before
// it (transitive predecessors over the directed edges). Robust to cycles.
func ancestors(edges []sdkr.FlowEdge, nodes []sdkr.FlowNode) map[string]map[string]bool {
	preds := map[string][]string{}
	for _, e := range edges {
		from := strings.TrimSpace(e.From)
		to := strings.TrimSpace(e.To)
		if from == "" || to == "" {
			continue
		}
		preds[to] = append(preds[to], from)
	}
	out := map[string]map[string]bool{}
	for _, n := range nodes {
		set := map[string]bool{}
		var walk func(id string)
		walk = func(id string) {
			for _, p := range preds[id] {
				if !set[p] {
					set[p] = true
					walk(p)
				}
			}
		}
		walk(n.ID)
		out[n.ID] = set
	}
	return out
}
