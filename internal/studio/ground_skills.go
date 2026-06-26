// ground_skills.go — deterministic capability grounding for ReAct/Plan-Execute
// agents. The compiler prompt already shows the model the LIVE catalog (the
// unified index of installed skills, builtin tools and connected MCP tools, each
// with a description) and asks it to match the intent to real capabilities. This
// pass makes that selection TRUSTWORTHY instead of taking the model at its word:
//
//	1. VERIFY + CORRECT — every skill/tool the model chose is checked against the
//	   index. An exact hit is canonicalised; an obvious near-miss is fuzzy-mapped
//	   to the real installed name; anything with no match is dropped with a note.
//	2. INJECT — installed skills the intent clearly references (by name, or by a
//	   strong overlap with the skill's description) but the model failed to list
//	   are added, so the agent actually gets the capability.
//
// It mutates the draft in place and returns human-readable notes describing every
// change. Pure + deterministic (no LLM, no I/O) so it is fully unit-testable.
package studio

import (
	"encoding/json"
	"strings"
)

// GroundAgentCapabilities grounds an agent draft's Skills and Tools against the
// catalog. Tools are verified/corrected but never bulk-injected (auto-granting
// tools is a scope/security concern); skills — the safe, additive capability —
// are also injected when the intent clearly calls for them.
func GroundAgentCapabilities(draft *Draft, cat Catalog) []string {
	if draft == nil {
		return nil
	}
	var notes []string
	notes = append(notes, groundSkills(draft, cat)...)
	notes = append(notes, groundTools(draft, cat)...)
	return notes
}

// skillEntry is one indexed skill: its canonical name plus a search corpus
// (name + description tokens) used for fuzzy matching and intent injection.
type skillEntry struct {
	name   string
	tokens map[string]bool
}

// MissingSkillSuggestions returns a Suggestion for each model-listed skill that
// has no installed match (exact or fuzzy) — so a referenced-but-uninstalled skill
// is surfaced in the "Needs setup" panel like a missing tool, instead of being
// silently dropped by grounding. Call it on the model's ORIGINAL skill picks,
// before GroundAgentCapabilities mutates the list.
func MissingSkillSuggestions(skills []string, cat Catalog) []Suggestion {
	if len(cat.Skills) == 0 {
		return nil
	}
	index, byLowerName := buildSkillIndex(cat)
	var out []Suggestion
	seen := map[string]bool{}
	for _, s := range skills {
		raw := strings.TrimSpace(s)
		low := strings.ToLower(raw)
		if raw == "" || seen[low] {
			continue
		}
		seen[low] = true
		if _, ok := byLowerName[low]; ok {
			continue // installed exactly
		}
		if bestSkillMatch(raw, index) != "" {
			continue // fuzzy-maps to an installed skill — not missing
		}
		out = append(out, Suggestion{Kind: "skill", Name: raw, Installed: false, Reason: capabilityReason("skill", raw, false)})
	}
	return out
}

// buildSkillIndex indexes the catalog's installed skills for grounding: a
// canonical-name lookup (lowercased → real name) and a per-skill search corpus.
func buildSkillIndex(cat Catalog) (index []skillEntry, byLowerName map[string]string) {
	byLowerName = map[string]string{}
	index = make([]skillEntry, 0, len(cat.Skills))
	for _, sk := range cat.Skills {
		n := strings.TrimSpace(sk.Name)
		if n == "" {
			continue
		}
		byLowerName[strings.ToLower(n)] = n
		index = append(index, skillEntry{name: n, tokens: tokenize(n + " " + sk.Description)})
	}
	return index, byLowerName
}

func groundSkills(draft *Draft, cat Catalog) []string {
	if len(cat.Skills) == 0 {
		return nil // no index to ground against — leave the model's choice untouched
	}
	index, byLowerName := buildSkillIndex(cat)

	var notes []string
	chosen := map[string]bool{}
	var kept []string
	add := func(name string) bool {
		if chosen[name] {
			return false
		}
		chosen[name] = true
		kept = append(kept, name)
		return true
	}

	// (1) Verify + correct everything the model chose.
	for _, s := range draft.Skills {
		raw := strings.TrimSpace(s)
		if raw == "" {
			continue
		}
		if canon, ok := byLowerName[strings.ToLower(raw)]; ok {
			add(canon)
			continue
		}
		if best := bestSkillMatch(raw, index); best != "" {
			if add(best) {
				notes = append(notes, "Mapped skill \""+raw+"\" → installed \""+best+"\".")
			}
			continue
		}
		notes = append(notes, "Dropped skill \""+raw+"\" — no matching installed skill.")
	}

	// (2) Inject installed skills the intent clearly references but the model missed.
	intentLow := strings.ToLower(draft.Intent + " " + draft.SystemPrompt)
	intentTokens := tokenize(draft.Intent + " " + draft.SystemPrompt)
	for _, e := range index {
		if chosen[e.name] {
			continue
		}
		if nameMentioned(intentLow, e.name) || overlap(intentTokens, e.tokens) >= 2 {
			if add(e.name) {
				notes = append(notes, "Added installed skill \""+e.name+"\" — it matches your prompt.")
			}
		}
	}

	draft.Skills = kept
	return notes
}

// GroundFlowSkills grounds a deterministic WORKFLOW's read_skill nodes against
// the live skill index — the flow analogue of groundSkills' verify+correct step.
// For each read_skill node it canonicalises an exact skill_name and fuzzy-maps an
// obvious near-miss ("yahoo finance" → installed "yfinance") in place, so the node
// resolves at run time instead of failing as an unknown skill. It does NOT add or
// remove steps (the graph is the user's), and leaves a genuinely-unknown name
// untouched for "Needs setup"/validation to surface. Returns change notes.
func GroundFlowSkills(draft *Draft, cat Catalog) []string {
	if draft == nil || len(cat.Skills) == 0 {
		return nil
	}
	index, byLowerName := buildSkillIndex(cat)
	var notes []string
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if strings.TrimSpace(n.Tool) != "read_skill" {
			continue
		}
		raw := skillNameFromInput(n.Input)
		if raw == "" {
			continue
		}
		if _, ok := byLowerName[strings.ToLower(raw)]; ok {
			// Canonicalise casing to the installed name so it resolves exactly.
			if canon := byLowerName[strings.ToLower(raw)]; canon != raw {
				n.Input = setSkillNameInInput(n.Input, canon)
			}
			continue
		}
		if best := bestSkillMatch(raw, index); best != "" {
			n.Input = setSkillNameInInput(n.Input, best)
			notes = append(notes, "step \""+n.ID+"\": mapped skill \""+raw+"\" → installed \""+best+"\".")
		}
	}
	return notes
}

// setSkillNameInInput rewrites the skill_name field of a read_skill node's JSON
// input, preserving any other keys. Falls back to a minimal object if the input
// isn't parseable.
func setSkillNameInInput(input, name string) string {
	m := map[string]any{}
	if s := strings.TrimSpace(input); s != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	m["skill_name"] = name
	b, err := json.Marshal(m)
	if err != nil {
		return `{"skill_name":"` + name + `"}`
	}
	return string(b)
}

// bestSkillMatch fuzzy-maps a near-miss skill reference to the closest installed
// skill, comparing the reference's tokens against each skill's name+description
// corpus (so "yahoo_finance" reaches the installed "yfinance" via its "Yahoo
// Finance" description). Returns "" when nothing clears the confidence bar.
func bestSkillMatch(ref string, index []skillEntry) string {
	refTokens := tokenize(ref)
	refLow := strings.ToLower(strings.TrimSpace(ref))
	best := ""
	bestScore := 0
	for _, e := range index {
		ov := overlap(e.tokens, refTokens)
		nameLow := strings.ToLower(e.name)
		substr := refLow != "" && (strings.Contains(nameLow, refLow) || strings.Contains(refLow, nameLow))
		// A match needs REAL evidence: shared meaningful token(s), or a whole-name
		// substring relationship. A stray shared token alone is not enough, and a
		// pure coincidental substring (no token overlap, e.g. "finance" inside an
		// unrelated name) is rejected by the >=2 bar below.
		if ov == 0 && !substr {
			continue
		}
		score := ov
		if substr {
			score++ // a mild boost, not decisive on its own
		}
		// Deterministic: higher score wins; ties are broken by the SHORTER name,
		// then lexicographically — never by catalog iteration order.
		if score > bestScore || (score == bestScore && best != "" && tieBeats(e.name, best)) {
			bestScore, best = score, e.name
		}
	}
	if bestScore >= 2 {
		return best
	}
	return ""
}

// tieBeats reports whether candidate a should win a score tie over current b:
// the shorter (more specific) name, breaking remaining ties lexicographically.
func tieBeats(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// groundTools verifies the chosen tool allowlist against the catalog (builtins +
// mcp__ tools), correcting an obvious mcp near-miss and dropping unknowns. Tools
// are NOT auto-injected: granting an agent a tool it didn't ask for widens its
// blast radius, so that stays an explicit choice.
func groundTools(draft *Draft, cat Catalog) []string {
	if len(cat.Tools) == 0 && len(cat.MCP) == 0 {
		return nil
	}
	builtins := map[string]string{} // lower -> canonical
	for _, t := range cat.Tools {
		if n := strings.TrimSpace(t); n != "" {
			builtins[strings.ToLower(n)] = n
		}
	}
	mcpNames := map[string]string{} // lower full name -> canonical
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			if n := strings.TrimSpace(t.Name); n != "" {
				mcpNames[strings.ToLower(n)] = n
			}
		}
	}

	var notes []string
	seen := map[string]bool{}
	var kept []string
	for _, t := range draft.Tools {
		raw := strings.TrimSpace(t)
		if raw == "" {
			continue
		}
		low := strings.ToLower(raw)
		canon := ""
		if c, ok := builtins[low]; ok {
			canon = c
		} else if c, ok := mcpNames[low]; ok {
			canon = c
		} else if strings.HasPrefix(low, "mcp__") {
			canon = bestMCPMatch(low, mcpNames)
			if canon != "" {
				notes = append(notes, "Mapped tool \""+raw+"\" → \""+canon+"\".")
			}
		}
		if canon == "" {
			notes = append(notes, "Dropped tool \""+raw+"\" — not a builtin or connected MCP tool.")
			continue
		}
		if !seen[canon] {
			seen[canon] = true
			kept = append(kept, canon)
		}
	}
	draft.Tools = kept
	return notes
}

// bestMCPMatch maps an mcp__ near-miss to the connected tool whose final segment
// (the tool name after the server) matches — catching server/tool typos.
func bestMCPMatch(ref string, mcpNames map[string]string) string {
	refTool := ref
	if i := strings.LastIndex(ref, "__"); i >= 0 {
		refTool = ref[i+2:]
	}
	for low, canon := range mcpNames {
		seg := low
		if i := strings.LastIndex(low, "__"); i >= 0 {
			seg = low[i+2:]
		}
		if seg != "" && seg == refTool {
			return canon
		}
	}
	return ""
}
