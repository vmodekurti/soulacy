package studio

import (
	"sort"
	"strings"
)

// Grounding caps. Below these sizes the catalog is passed through UNCHANGED, so
// small/typical setups behave exactly as before and existing prompt tests hold.
// Above them, we keep the items most relevant to the intent (plus anything the
// intent names explicitly) to avoid flooding the prompt — which both wastes
// tokens and degrades weaker builder models.
const (
	maxGroundedSkills   = 24
	maxGroundedKBs      = 12
	maxGroundedMCPTools = 40
)

// FilterCatalogForIntent returns a copy of the catalog trimmed to the items most
// relevant to the intent, but ONLY when a list exceeds its cap. Agents, tools,
// providers, and channels are left untouched (small, cheap, and the user may
// reference any). Skills, KBs, and MCP tools — the token-heavy, often-large
// lists — are ranked by term overlap with the intent and capped; any item the
// intent mentions by name is always kept. Pure + deterministic.
func FilterCatalogForIntent(intent string, cat Catalog) Catalog {
	terms := tokenize(intent)
	li := strings.ToLower(intent)

	out := cat // shallow copy; we replace the slices we trim

	// Skills.
	if len(cat.Skills) > maxGroundedSkills {
		type sc struct {
			s     CatalogSkill
			score int
		}
		ranked := make([]sc, 0, len(cat.Skills))
		for _, s := range cat.Skills {
			score := overlap(terms, tokenize(s.Name+" "+s.Description))
			if nameMentioned(li, s.Name) {
				score += 100 // never drop an explicitly named skill
			}
			ranked = append(ranked, sc{s, score})
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
		trimmed := make([]CatalogSkill, 0, maxGroundedSkills)
		for i := 0; i < len(ranked) && i < maxGroundedSkills; i++ {
			trimmed = append(trimmed, ranked[i].s)
		}
		out.Skills = trimmed
	}

	// Knowledge bases.
	if len(cat.KnowledgeBases) > maxGroundedKBs {
		type kc struct {
			k     CatalogKB
			score int
		}
		ranked := make([]kc, 0, len(cat.KnowledgeBases))
		for _, k := range cat.KnowledgeBases {
			score := overlap(terms, tokenize(k.Name+" "+k.Description))
			if nameMentioned(li, k.Name) {
				score += 100
			}
			ranked = append(ranked, kc{k, score})
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
		trimmed := make([]CatalogKB, 0, maxGroundedKBs)
		for i := 0; i < len(ranked) && i < maxGroundedKBs; i++ {
			trimmed = append(trimmed, ranked[i].k)
		}
		out.KnowledgeBases = trimmed
	}

	// MCP tools (across all servers). Keep whole servers, but cap total tools by
	// relevance. A server the intent names keeps all its tools.
	if total := countMCPTools(cat.MCP); total > maxGroundedMCPTools {
		out.MCP = trimMCPTools(cat.MCP, terms, li)
	}

	return out
}

// trimMCPTools keeps the most relevant MCP tools up to the cap, always keeping
// every tool of a server the intent names, and never dropping a server entirely
// if it still has at least one kept tool. Preserves server + tool order.
func trimMCPTools(servers []CatalogMCPServer, terms map[string]bool, li string) []CatalogMCPServer {
	type ref struct {
		srv, tool int
		score     int
	}
	var all []ref
	for si, srv := range servers {
		serverNamed := nameMentioned(li, srv.Server)
		for ti, t := range srv.Tools {
			score := overlap(terms, tokenize(t.Name+" "+t.Description))
			if serverNamed {
				score += 100
			}
			all = append(all, ref{si, ti, score})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })

	keep := make(map[[2]int]bool)
	for i := 0; i < len(all) && i < maxGroundedMCPTools; i++ {
		keep[[2]int{all[i].srv, all[i].tool}] = true
	}

	out := make([]CatalogMCPServer, 0, len(servers))
	for si, srv := range servers {
		var tools []CatalogMCPTool
		for ti, t := range srv.Tools {
			if keep[[2]int{si, ti}] {
				tools = append(tools, t)
			}
		}
		if len(tools) > 0 {
			out = append(out, CatalogMCPServer{Server: srv.Server, Tools: tools})
		}
	}
	return out
}

func countMCPTools(servers []CatalogMCPServer) int {
	n := 0
	for _, s := range servers {
		n += len(s.Tools)
	}
	return n
}

// tokenize lowercases, splits on non-alphanumerics, and drops very short words
// and a small stopword set, returning a set of meaningful terms.
func tokenize(s string) map[string]bool {
	out := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		w := cur.String()
		cur.Reset()
		if len(w) < 3 || stopwords[w] {
			return
		}
		out[w] = true
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// overlap counts how many of b's terms appear in a.
func overlap(a, b map[string]bool) int {
	n := 0
	for t := range b {
		if a[t] {
			n++
		}
	}
	return n
}

// nameMentioned reports whether the (lowercased) intent contains the item name.
func nameMentioned(li, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name != "" && strings.Contains(li, name)
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"from": true, "into": true, "your": true, "you": true, "are": true, "all": true,
	"use": true, "using": true, "get": true, "let": true, "via": true, "per": true,
	"can": true, "will": true, "every": true, "each": true, "when": true, "then": true,
}
