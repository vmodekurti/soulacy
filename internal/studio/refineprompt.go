package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PromptRefinement is the result of the pre-generation refine pass. Before the
// compiler turns an intent into a workflow, RefinePrompt asks the framework LLM
// to act as a requirements analyst: it rewrites the user's plain-language intent
// into a clear, complete, unambiguous specification, states the assumptions it
// had to make, and surfaces clarifying questions for the genuinely ambiguous,
// decision-changing gaps. The UI shows all of this and lets the user confirm or
// edit BEFORE a workflow is generated — so a vague prompt no longer silently
// produces a broken workflow.
type PromptRefinement struct {
	// Original is the raw intent the user typed (echoed back for the UI diff).
	Original string `json:"original"`
	// RefinedIntent is the rewritten, self-contained specification. It is what
	// the compiler should be fed once the user confirms — every piece spelled
	// out: trigger/schedule, data sources, processing steps, output channels,
	// and edge-case handling.
	RefinedIntent string `json:"refined_intent"`
	// Summary is a one- or two-sentence plain-language description of what the
	// resulting automation will do, so the user understands what they are
	// signing up for at a glance.
	Summary string `json:"summary"`
	// Assumptions lists the decisions the analyst made to fill gaps in the
	// original intent (e.g. "Assumed a daily 8am schedule", "Assumed output to
	// Telegram"). The user can correct any of these by editing the refined
	// intent before generating.
	Assumptions []string `json:"assumptions"`
	// Questions are clarifying questions for the genuinely ambiguous gaps that
	// would change the workflow. The UI renders them; answers are woven into the
	// compile that follows. Empty when the intent is already clear enough.
	Questions []Question `json:"questions"`
	// RecommendedMode is the architecture the analyst judges best: "workflow"
	// (fixed pipeline), "react" (reasoning loop — for tasks that loop or depend
	// on intermediate/async results), or "plan_execute". The wizard uses it to
	// decide whether Generate produces a flow or a ReAct agent. ModeReason is a
	// one-line justification.
	RecommendedMode string `json:"recommended_mode"`
	ModeReason      string `json:"mode_reason"`
}

// refinePromptPayload is the exact JSON shape the model is told to return. It is
// kept separate from PromptRefinement so the wire contract is explicit and the
// model never has to know about the server-filled Original field.
type refinePromptPayload struct {
	RefinedIntent   string     `json:"refined_intent"`
	Summary         string     `json:"summary"`
	Assumptions     []string   `json:"assumptions"`
	Questions       []Question `json:"questions"`
	RecommendedMode string     `json:"recommended_mode"`
	ModeReason      string     `json:"mode_reason"`
}

// BuildRefinePromptInstruction builds the instruction for the refine pass. It is
// pure (no I/O) and deterministic so it is unit-testable, and it grounds the
// analyst in the SAME live catalog the compiler will use — so the refined
// intent only references capabilities that actually exist.
func BuildRefinePromptInstruction(intent string, catalog Catalog) string {
	// Trim large grounding lists to the intent-relevant subset (no-op when small).
	catalog = FilterCatalogForIntent(intent, catalog)
	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio requirements analyst. ")
	sb.WriteString("A user has described an automation they want built. Your job is NOT to build it yet — ")
	sb.WriteString("it is to turn their rough, often vague description into a clear, complete, unambiguous specification, ")
	sb.WriteString("and to flag anything still genuinely unclear, BEFORE a workflow is generated.\n\n")

	sb.WriteString("Why this matters: a vague prompt produces a broken or wrong workflow. Every piece of the spec must be explicit.\n\n")

	sb.WriteString("Produce a refined specification that pins down ALL of:\n")
	sb.WriteString("1. TRIGGER — when/how it runs: a schedule (give a concrete cadence, e.g. \"every weekday at 8am\"), an incoming message/channel, a webhook, or manual.\n")
	sb.WriteString("2. INPUTS / DATA SOURCES — exactly what data it works on and where that comes from (a search query, an API, an uploaded file, an MCP server).\n")
	sb.WriteString("3. PROCESSING STEPS — the concrete sequence of work, in order, in plain language.\n")
	sb.WriteString("4. OUTPUT — what is produced and where it goes (which channel: telegram/slack/email, a file, etc.).\n")
	sb.WriteString("5. EDGE CASES — what to do on empty results, errors, or nothing-to-report.\n\n")

	sb.WriteString("Rules:\n")
	sb.WriteString("- Stay faithful to the user's intent. Do NOT invent scope they did not ask for; fill only the gaps needed to make it buildable.\n")
	sb.WriteString("- Where you must make a choice to fill a gap, pick a sensible default AND record it in \"assumptions\" so the user can correct it.\n")
	sb.WriteString("- Only reference capabilities that exist in the catalog below. If the user names something not available, note it as an assumption or a question rather than inventing it.\n")
	sb.WriteString("- Ask a clarifying question ONLY when the answer would genuinely change the workflow (a real fork). Do not ask about things you can reasonably default. Prefer 0–3 high-value questions; an already-clear intent needs none.\n")
	sb.WriteString("- The \"refined_intent\" must be self-contained: a person reading ONLY it should understand the whole automation. Write it as clear prose or a short ordered list, not JSON.\n")
	sb.WriteString("- \"summary\" is one or two plain sentences describing what the automation will do.\n\n")

	sb.WriteString("Also decide the best ARCHITECTURE and return it:\n")
	sb.WriteString("- \"workflow\": a fixed, deterministic pipeline — the same steps in the same order every run, knowable up front (e.g. \"each morning search X, summarize, post to Telegram\").\n")
	sb.WriteString("- \"react\": a reasoning loop — when steps DEPEND on intermediate results, the task loops over items, or it polls an ASYNCHRONOUS job until done (e.g. driving NotebookLM: add each source, then poll audio status until ready). A frozen graph is brittle here.\n")
	sb.WriteString("- \"plan_execute\": a long, multi-phase job worth decomposing first.\n\n")

	sb.WriteString("Respond with ONLY a single JSON object, no prose, no markdown, no code fences, matching exactly:\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"refined_intent\": \"<the complete, unambiguous specification>\",\n")
	sb.WriteString("  \"summary\": \"<one or two sentences: what this automation does>\",\n")
	sb.WriteString("  \"assumptions\": [\"<each gap you filled and the default you chose>\"],\n")
	sb.WriteString("  \"questions\": [ { \"id\": \"<short_id>\", \"text\": \"<question>\", \"options\": [\"<opt>\", \"...\"] } ],\n")
	sb.WriteString("  \"recommended_mode\": \"workflow|react|plan_execute\",\n")
	sb.WriteString("  \"mode_reason\": \"<1 sentence on why this architecture fits>\"\n")
	sb.WriteString("}\n")
	sb.WriteString("(\"options\" is optional — include it only when the answer is a closed choice. \"assumptions\" and \"questions\" may be empty arrays.)\n\n")

	writeCatalogGrounding(&sb, catalog)
	writePatternGrounding(&sb, intent, catalog)

	sb.WriteString("\nUser's original intent:\n")
	sb.WriteString(intent)
	sb.WriteString("\n")
	return sb.String()
}

// writeCatalogGrounding appends the available-capabilities context (skills, MCP
// servers/tools, agents, channels) to sb. It is shared by the refine pass and
// could be reused by other prompt builders; it mirrors the grounding format the
// compiler uses so the analyst and the compiler see the same world.
func writeCatalogGrounding(sb *strings.Builder, catalog Catalog) {
	if len(catalog.Skills) > 0 {
		sb.WriteString("Available skills (data sources / capabilities you may reference):\n")
		for _, sk := range catalog.Skills {
			name := strings.TrimSpace(sk.Name)
			if name == "" {
				continue
			}
			desc := strings.TrimSpace(sk.Description)
			if len(desc) > 200 {
				desc = desc[:200] + "…"
			}
			sb.WriteString("- ")
			sb.WriteString(name)
			if desc != "" {
				sb.WriteString(" — ")
				sb.WriteString(desc)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	if len(catalog.MCP) > 0 {
		sb.WriteString("Available MCP servers and their tools:\n")
		for _, srv := range catalog.MCP {
			name := strings.TrimSpace(srv.Server)
			if name == "" {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(name)
			sb.WriteString("\n")
			for _, t := range srv.Tools {
				tn := strings.TrimSpace(t.Name)
				if tn == "" {
					continue
				}
				desc := strings.TrimSpace(t.Description)
				if len(desc) > 200 {
					desc = desc[:200] + "…"
				}
				sb.WriteString("    • ")
				sb.WriteString(tn)
				if desc != "" {
					sb.WriteString(" — ")
					sb.WriteString(desc)
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}
	if len(catalog.Agents) > 0 {
		sb.WriteString("Available agents: ")
		sb.WriteString(strings.Join(catalog.Agents, ", "))
		sb.WriteString("\n")
	}
	if len(catalog.Tools) > 0 {
		sb.WriteString("Available tools: ")
		sb.WriteString(strings.Join(catalog.Tools, ", "))
		sb.WriteString("\n")
	}
	writeChannelGrounding(sb, catalog)
	writeKBGrounding(sb, catalog)
}

// writeChannelGrounding appends the configured output channels so prompts wire
// delivery to a real channel instead of inventing one. Shared by compile +
// refine.
func writeChannelGrounding(sb *strings.Builder, catalog Catalog) {
	if len(catalog.Channels) == 0 {
		return
	}
	sb.WriteString("Configured output channels (deliver results to one of these EXACT names): ")
	sb.WriteString(strings.Join(catalog.Channels, ", "))
	sb.WriteString("\n")
}

// writeKBGrounding appends the available knowledge bases with their
// descriptions so the compiler can attach a relevant KB to the agent (Story
// #7). Shared by compile + refine.
func writeKBGrounding(sb *strings.Builder, catalog Catalog) {
	if len(catalog.KnowledgeBases) == 0 {
		return
	}
	sb.WriteString("Available knowledge bases — to give the agent access to one, add its EXACT name to a top-level \"knowledge\" array in your JSON. Attach a KB ONLY when its subject clearly matches the intent; never attach unrelated KBs:\n")
	for _, kb := range catalog.KnowledgeBases {
		name := strings.TrimSpace(kb.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(kb.Description)
		if len(desc) > 200 {
			desc = desc[:200] + "…"
		}
		sb.WriteString("- ")
		sb.WriteString(name)
		if desc != "" {
			sb.WriteString(" — ")
			sb.WriteString(desc)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
}

// BuildLightRefineInstruction builds the instruction for a LIGHT (touch-up)
// refine pass. It is used when the intent has ALREADY been through a full
// refine and the user has hand-edited the resulting specification: re-running
// the full analyst rewrite would be slow and would fight the user's edits. The
// light pass instead treats the input as near-final — it only cleans up grammar
// and obvious gaps, preserves the user's wording and structure, and returns the
// same JSON shape so the UI/compiler path is unchanged. Like the full builder it
// is pure and grounds the model in the same catalog.
func BuildLightRefineInstruction(intent string, catalog Catalog) string {
	catalog = FilterCatalogForIntent(intent, catalog)
	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio requirements analyst doing a LIGHT touch-up. ")
	sb.WriteString("The text below is ALREADY a refined specification that the user has reviewed and hand-edited. ")
	sb.WriteString("It is essentially final. Do NOT rewrite it, restructure it, or expand its scope.\n\n")

	sb.WriteString("Your ONLY job is a light cleanup that respects the user's edits:\n")
	sb.WriteString("- Fix grammar, spelling, and obvious clarity issues.\n")
	sb.WriteString("- Preserve the user's wording, ordering, and intent as closely as possible — change as little as you can.\n")
	sb.WriteString("- If the user left an obvious gap in the standard spec (trigger, inputs, processing, output, edge cases), fill ONLY that gap with a sensible default and record it in \"assumptions\".\n")
	sb.WriteString("- Do NOT introduce new features, steps, or scope the user did not write.\n")
	sb.WriteString("- Only reference capabilities that exist in the catalog below.\n")
	sb.WriteString("- Ask a clarifying question ONLY if the user's edit introduced a genuine, workflow-changing contradiction. Prefer 0 questions.\n\n")

	sb.WriteString("Also re-check the best ARCHITECTURE for the edited spec and return it:\n")
	sb.WriteString("- \"workflow\": a fixed, deterministic pipeline.\n")
	sb.WriteString("- \"react\": a reasoning loop (steps depend on intermediate results, per-item loops, or polling an async job).\n")
	sb.WriteString("- \"plan_execute\": a long, multi-phase job worth decomposing first.\n\n")

	sb.WriteString("Respond with ONLY a single JSON object, no prose, no markdown, no code fences, matching exactly:\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"refined_intent\": \"<the lightly cleaned-up specification — keep the user's text>\",\n")
	sb.WriteString("  \"summary\": \"<one or two sentences: what this automation does>\",\n")
	sb.WriteString("  \"assumptions\": [\"<only gaps you had to fill>\"],\n")
	sb.WriteString("  \"questions\": [ { \"id\": \"<short_id>\", \"text\": \"<question>\", \"options\": [\"<opt>\", \"...\"] } ],\n")
	sb.WriteString("  \"recommended_mode\": \"workflow|react|plan_execute\",\n")
	sb.WriteString("  \"mode_reason\": \"<1 sentence on why this architecture fits>\"\n")
	sb.WriteString("}\n")
	sb.WriteString("(\"assumptions\" and \"questions\" may be empty arrays.)\n\n")

	writeCatalogGrounding(&sb, catalog)
	writePatternGrounding(&sb, intent, catalog)

	sb.WriteString("\nAlready-refined specification (the user's edited text):\n")
	sb.WriteString(intent)
	sb.WriteString("\n")
	return sb.String()
}

// RefinePrompt runs the pre-generation refine pass: it asks the LLM to rewrite
// the intent into a clear specification plus assumptions and clarifying
// questions. Like Compile it is tolerant of model output wrapped in fences or
// prose. If the model returns nothing usable, RefinePrompt degrades gracefully:
// it returns a refinement that echoes the original intent (so the UI can still
// proceed) rather than erroring — a refine pass should never block generation.
func RefinePrompt(ctx context.Context, llm LLM, intent string, catalog Catalog) (PromptRefinement, error) {
	return refinePrompt(ctx, llm, intent, catalog, false)
}

// LightRefinePrompt runs a LIGHT touch-up pass for an already-refined,
// user-edited specification: it cleans up the text without the full rewrite, so
// re-generating after an edit is fast and faithful to what the user typed. Same
// output contract as RefinePrompt.
func LightRefinePrompt(ctx context.Context, llm LLM, intent string, catalog Catalog) (PromptRefinement, error) {
	return refinePrompt(ctx, llm, intent, catalog, true)
}

// refinePrompt is the shared implementation behind RefinePrompt (full) and
// LightRefinePrompt (touch-up). The only difference is which instruction the
// model is given; parsing, mode resolution, and graceful degradation are
// identical so both paths return the same PromptRefinement contract.
func refinePrompt(ctx context.Context, llm LLM, intent string, catalog Catalog, light bool) (PromptRefinement, error) {
	if strings.TrimSpace(intent) == "" {
		return PromptRefinement{}, fmt.Errorf("studio: intent is required")
	}
	if llm == nil {
		return PromptRefinement{}, fmt.Errorf("studio: no LLM configured")
	}

	var prompt string
	if light {
		prompt = BuildLightRefineInstruction(intent, catalog)
	} else {
		prompt = BuildRefinePromptInstruction(intent, catalog)
	}
	raw, err := llm.Complete(ctx, prompt)
	if err != nil {
		return PromptRefinement{}, fmt.Errorf("studio: llm complete: %w", err)
	}

	payload, perr := parseRefinement(raw)
	if perr != nil || strings.TrimSpace(payload.RefinedIntent) == "" {
		// Graceful degradation: never block generation on a bad refine. Fall
		// back to the original intent + a deterministic mode guess.
		return PromptRefinement{
			Original:        intent,
			RefinedIntent:   intent,
			RecommendedMode: inferModeFromIntent(intent),
		}, nil
	}

	combined := payload.RefinedIntent + " " + intent
	mode := normalizeMode(payload.RecommendedMode)
	// Override the model when there are STRONG async/loop signals: a fixed flow
	// physically cannot poll an async job or loop per-item, so these are always
	// ReAct regardless of what the model guessed (models often mislabel a
	// scheduled NotebookLM job as a "workflow" because it has a daily cadence).
	if hasStrongReactCues(combined) {
		mode = "react"
	} else if mode == "" {
		mode = inferModeFromIntent(combined)
	}
	return PromptRefinement{
		Original:        intent,
		RefinedIntent:   strings.TrimSpace(payload.RefinedIntent),
		Summary:         strings.TrimSpace(payload.Summary),
		Assumptions:     trimStrings(payload.Assumptions),
		Questions:       payload.Questions,
		RecommendedMode: mode,
		ModeReason:      strings.TrimSpace(payload.ModeReason),
	}, nil
}

// normalizeMode canonicalizes a model-supplied mode to workflow|react|
// plan_execute, or "" if unrecognized.
func normalizeMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "workflow", "flow":
		return "workflow"
	case "react":
		return "react"
	case "plan_execute", "plan-execute", "planexecute":
		return "plan_execute"
	}
	return ""
}

// hasStrongReactCues reports whether the intent has signals that a FIXED flow
// cannot satisfy (asynchronous jobs that must be polled, per-item loops, or
// driving an interactive multi-step external service like NotebookLM). These
// override a model's "workflow" guess — we've seen fixed flows fail every time
// on these. Distinct from inferModeFromIntent's softer cues (used only as a
// no-model fallback).
func hasStrongReactCues(intent string) bool {
	t := strings.ToLower(intent)
	strong := []string{
		"notebooklm", "notebook lm", "audio overview",
		"poll", "until ready", "until it is ready", "until complete", "until completed",
		"until done", "until it finishes", "wait until", "wait for it",
		"each source", "one at a time", "one by one", "for each ", "per article", "per item",
		"check status", "status until", "generation status",
	}
	for _, c := range strong {
		if strings.Contains(t, c) {
			return true
		}
	}
	return false
}

// inferModeFromIntent is a deterministic backstop: phrases implying loops over
// items, polling, or driving an interactive external service lean ReAct; else
// workflow.
func inferModeFromIntent(intent string) string {
	t := strings.ToLower(intent)
	reactCues := []string{
		"poll", "until ready", "until complete", "until done", "wait for",
		"each ", "every item", "one by one", "iterate", "loop over",
		"notebooklm", "notebook lm", "research and then", "figure out", "explore", "manage",
	}
	for _, c := range reactCues {
		if strings.Contains(t, c) {
			return "react"
		}
	}
	return "workflow"
}

// parseRefinement tolerantly extracts the refine JSON from raw model output,
// reusing the same fence-stripping + outermost-object narrowing as ParseDraft.
func parseRefinement(raw string) (refinePromptPayload, error) {
	s := stripFences(strings.TrimSpace(raw))
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return refinePromptPayload{}, fmt.Errorf("studio: no JSON object found in refine output")
	}
	s = s[start : end+1]
	var p refinePromptPayload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return refinePromptPayload{}, fmt.Errorf("studio: parse refine: %w", err)
	}
	return p, nil
}

// trimStrings trims each entry and drops empties, keeping the assumptions list
// clean for the UI.
func trimStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
