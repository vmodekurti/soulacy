package studio

import (
	"encoding/json"
	"regexp"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// RepairWiring runs the full deterministic data-flow repair over a draft: fill
// empty required tool args from same-named upstream outputs (AutoWire), then
// reconcile dangling template-variable references to the right upstream output
// (ReconcileVars) — e.g. a node referencing {{ .date_str }} when the producing
// step actually outputs date_info. Returns the total number of fixes applied.
// Pure except for mutating draft.
func RepairWiring(draft *Draft, cat Catalog) int {
	if draft == nil {
		return 0
	}
	return AutoWire(draft, cat) + ReconcileVars(draft) + fixTemplateTypos(draft)
}

// templateActionRe matches a {{ … }} template action (no nested braces).
var templateActionRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// doubleDotRe matches a run of 2+ dots — only ever a typo inside a template
// action (e.g. {{ ..date_info }}); valid field chains use single dots.
var doubleDotRe = regexp.MustCompile(`\.\.+`)

// fixTemplateTypos collapses accidental double-dots ({{ ..x }} → {{ .x }}) and
// is applied ONLY inside {{ … }} actions, so real text (URLs, "…", file paths)
// is never touched. Runs over node inputs and edge predicates. Returns the
// number of strings it changed.
func fixTemplateTypos(draft *Draft) int {
	clean := func(s string) (string, bool) {
		if !strings.Contains(s, "{{") {
			return s, false
		}
		out := templateActionRe.ReplaceAllStringFunc(s, func(act string) string {
			return doubleDotRe.ReplaceAllString(act, ".")
		})
		return out, out != s
	}
	fixed := 0
	for i := range draft.Flow.Nodes {
		if v, changed := clean(draft.Flow.Nodes[i].Input); changed {
			draft.Flow.Nodes[i].Input = v
			fixed++
		}
	}
	for i := range draft.Flow.Edges {
		if v, changed := clean(draft.Flow.Edges[i].If); changed {
			draft.Flow.Edges[i].If = v
			fixed++
		}
	}
	return fixed
}

// ReconcileVars rewrites a node's dangling {{ .X }} references — vars that NO
// node produces — to the best-matching variable produced by an upstream
// (ancestor) step. "Best match" is decided by shared name tokens (split on "_"),
// and only applied when exactly ONE ancestor output is the clear winner, so an
// ambiguous case is left for the focused-LLM repair or the user rather than
// guessed wrong. Fixes the common model slip where a step invents a slightly
// different variable name than the one its producer emits. Returns the count of
// references rewritten.
func ReconcileVars(draft *Draft) int {
	if draft == nil || len(draft.Flow.Nodes) == 0 {
		return 0
	}
	producer := map[string]string{} // output var -> producing node id
	for _, n := range draft.Flow.Nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n.ID
		}
	}
	anc := ancestors(draft.Flow.Edges, draft.Flow.Nodes)

	fixed := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		for _, ref := range referencedVars(n.Input) {
			if _, produced := producer[ref]; produced {
				continue // the var exists; ordering issues are handled elsewhere
			}
			// Dangling ref: gather candidate vars produced by this node's ancestors.
			var cand []string
			for v, pid := range producer {
				if a := anc[n.ID]; a != nil && a[pid] {
					cand = append(cand, v)
				}
			}
			if best := bestTokenMatch(ref, cand); best != "" {
				n.Input = rewriteVarRef(n.Input, ref, best)
				fixed++
			}
		}
	}
	return fixed
}

// bestTokenMatch returns the single candidate sharing the most name tokens with
// ref (tokens split on "_"), requiring a unique winner with at least one shared
// token. Returns "" when there's no candidate, no overlap, or a tie — those are
// left for the LLM/user rather than guessed.
func bestTokenMatch(ref string, candidates []string) string {
	refTokens := tokenizeVar(ref)

	// First preference: a candidate whose tokens are a SUBSET of the ref's
	// tokens — i.e. the producer name is "contained in" the reference, like
	// producer `notebook` for ref `notebook_id`. This cleanly resolves the
	// common "X_id refers to the X output" case and breaks ties against a
	// sibling like `notebook_params` (which has an extra non-ref token). If
	// exactly one subset candidate has the max overlap, take it.
	subBest, subScore, subTie := "", 0, false
	for _, c := range candidates {
		ct := tokenizeVar(c)
		if !isSubset(ct, refTokens) {
			continue
		}
		score := len(ct)
		if score > subScore {
			subScore, subBest, subTie = score, c, false
		} else if score == subScore && score > 0 {
			subTie = true
		}
	}
	if subScore > 0 && !subTie {
		return subBest
	}

	// Fallback: the candidate sharing the most tokens, if it's a unique winner.
	bestVar, bestScore, tie := "", 0, false
	for _, c := range candidates {
		score := 0
		for t := range tokenizeVar(c) {
			if refTokens[t] {
				score++
			}
		}
		if score > bestScore {
			bestScore, bestVar, tie = score, c, false
		} else if score == bestScore && score > 0 {
			tie = true
		}
	}
	if bestScore == 0 || tie {
		return ""
	}
	return bestVar
}

// isSubset reports whether every token in a is also in b.
func isSubset(a, b map[string]bool) bool {
	if len(a) == 0 {
		return false
	}
	for t := range a {
		if !b[t] {
			return false
		}
	}
	return true
}

func tokenizeVar(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Split(strings.ToLower(s), "_") {
		if t != "" {
			out[t] = true
		}
	}
	return out
}

// rewriteVarRef replaces template references to .from with .to in s — matching
// the dotted identifier so ".date_str" becomes ".date_info" inside any
// {{ ... }} expression, without touching unrelated text.
func rewriteVarRef(s, from, to string) string {
	re := regexp.MustCompile(`\.` + regexp.QuoteMeta(from) + `\b`)
	return re.ReplaceAllString(s, "."+to)
}

// AutoWire deterministically connects an upstream step's output into a later
// step's REQUIRED tool argument when that argument is empty/placeholder and an
// earlier step produces an output variable of the same name (local-first pivot,
// Story #10). This is the automatic repair for the most common generation
// failure — a tool called with notebook_id: <no value> — without asking the
// model to regenerate anything. It is conservative: it only fills a required
// arg that is currently empty/placeholder, and only from a producer that runs
// BEFORE the consumer (an ancestor, or any earlier node when no edges exist).
// Returns the number of args it wired. Pure except for mutating draft.
func AutoWire(draft *Draft, cat Catalog) int {
	if draft == nil || len(draft.Flow.Nodes) == 0 {
		return 0
	}

	// Required args per MCP tool, from the catalog param hints.
	required := map[string][]string{}
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			if name := strings.TrimSpace(t.Name); name != "" {
				required[name] = requiredParams(t.Params)
			}
		}
	}

	// producer: output var -> producing node id.
	producer := map[string]string{}
	for _, n := range draft.Flow.Nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n.ID
		}
	}
	anc := ancestors(draft.Flow.Edges, draft.Flow.Nodes)
	hasEdges := len(draft.Flow.Edges) > 0

	wired := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if n.Kind != "tool" {
			continue
		}
		reqs := required[strings.TrimSpace(n.Tool)]
		if len(reqs) == 0 {
			continue
		}
		for _, arg := range reqs {
			if argFilled(n.Input, arg) {
				continue
			}
			prod, ok := producer[arg] // a step that outputs a var named exactly like the arg
			if !ok || prod == n.ID {
				continue
			}
			// Only wire from a step that runs before this one.
			if hasEdges {
				if a := anc[n.ID]; a == nil || !a[prod] {
					continue
				}
			}
			if setInputArg(n, arg, "{{ ."+arg+" }}") {
				wired++
			}
		}
	}
	return wired
}

// setInputArg sets arg -> value in a node's Input JSON object, preserving any
// existing keys. Returns true on success. If the Input isn't a JSON object it is
// replaced with a minimal one containing just this arg (the node had no usable
// input anyway).
func setInputArg(n *sdkr.FlowNode, arg, value string) bool {
	m := map[string]any{}
	if s := strings.TrimSpace(n.Input); s != "" {
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			m = map[string]any{}
		}
	}
	m[arg] = value
	b, err := json.Marshal(m)
	if err != nil {
		return false
	}
	n.Input = string(b)
	return true
}
