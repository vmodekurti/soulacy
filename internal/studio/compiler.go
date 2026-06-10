// Package studio implements the Studio plugin's backend: the intent
// compiler (Story S1.1). It turns a plain-language intent into a draft
// workflow plus clarifying questions — a hybrid that always returns a
// best-effort draft AND the questions needed to firm it up, never blocking.
//
// The compiler is deliberately split into small, independently testable
// functions (BuildPrompt → LLM.Complete → ParseDraft → validate → derive
// questions → notes) and depends only on a narrow LLM interface so it can
// be unit-tested with a fake model.
package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// LLM is the narrow completion seam the compiler depends on. Production
// wiring adapts the gateway's llm.Router to this; tests supply a fake.
type LLM interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// Catalog is the optional context the caller can supply so the model
// grounds the draft in real agents/tools/providers instead of inventing
// names. All fields are optional.
type Catalog struct {
	Agents    []string `json:"agents,omitempty"`
	Tools     []string `json:"tools,omitempty"`
	Providers []string `json:"providers,omitempty"`
}

// Request is the POST /api/v1/studio/compile body.
type Request struct {
	Intent  string  `json:"intent"`
	Catalog Catalog `json:"catalog,omitempty"`
	// Answers carries the user's responses to clarifying questions from a
	// prior compile (question id -> answer). When present they are woven
	// into the prompt so a re-compile incorporates them, closing the
	// clarify round-trip. Optional.
	Answers map[string]string `json:"answers,omitempty"`
}

// Trigger describes how the workflow starts.
type Trigger struct {
	Type   string         `json:"type"`             // schedule | channel | webhook | manual
	Config map[string]any `json:"config,omitempty"` // e.g. {"cron": "0 8 * * 1-5"}
}

// Flow is the graph form, mirroring the sdk/reasoning JSON shapes so the
// draft round-trips straight into reasoning.CompileFlow.
type Flow struct {
	Nodes []sdkr.FlowNode `json:"nodes"`
	Edges []sdkr.FlowEdge `json:"edges,omitempty"`
	Entry string          `json:"entry,omitempty"`
}

// Draft is the workflow the compiler produces.
type Draft struct {
	Name     string   `json:"name"`
	Trigger  Trigger  `json:"trigger"`
	Channels []string `json:"channels,omitempty"`
	Flow     Flow     `json:"flow"`
}

// Question is one clarifying question. Options, when present, suggest a
// closed set of answers the UI can render as choices.
type Question struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
}

// Suggestion flags a capability a draft REFERENCES but that is not present
// in the provided Catalog, so the UI can offer to install/discover it
// (Stories S4.1/S4.3). Kind is one of tool|agent|skill|mcp; today the
// compiler emits tool and agent suggestions (the only capability kinds a
// flow node can reference). Installed reports whether the capability is in
// the catalog: suggestMissing returns only Installed:false entries (the
// actionable set), but the kind is part of the contract so the UI can route
// "install tool" vs "discover agent" appropriately.
type Suggestion struct {
	Kind      string `json:"kind"`   // tool | agent | skill | mcp
	Name      string `json:"name"`   // the referenced capability's name/id
	Reason    string `json:"reason"` // human-readable why it's suggested
	Installed bool   `json:"installed"`
}

// Result is the compile response: a draft, clarifying questions, transparency
// notes about what was inferred and why, and suggestions for capabilities the
// draft references but that aren't in the caller's catalog.
type Result struct {
	Workflow    Draft        `json:"workflow"`
	Questions   []Question   `json:"questions"`
	Notes       []string     `json:"notes"`
	Suggestions []Suggestion `json:"suggestions"`
}

// canonicalExample is the shape the model is instructed to emit. It is
// embedded verbatim in the prompt so the model has a concrete target. It is
// deliberately a RICH, MULTI-NODE graph (Story M3): multiple tool AND agent
// (peer) nodes, a branch node fanning out conditional edges, and typed ports
// wired with from_port/to_port — so the model treats branching, peer-agent
// handoff, and named ports as the norm, not the exception.
const canonicalExample = `{
  "name": "Weekday HN Digest",
  "trigger": { "type": "schedule", "config": { "cron": "0 8 * * 1-5" } },
  "channels": ["telegram"],
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{\"url\":\"https://hacker-news.firebaseio.com/v0/topstories.json\"}", "output": "stories",
        "outputs": [ { "name": "ok", "type": "json" } ], "x": 0, "y": 0 },
      { "id": "triage", "kind": "branch",
        "inputs":  [ { "name": "in" } ],
        "outputs": [ { "name": "hot" }, { "name": "quiet" } ], "x": 200, "y": 0 },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "Summarize the top 5: {{.stories}}", "output": "summary", "x": 400, "y": -80 },
      { "id": "notify", "kind": "agent", "agent": "notifier", "input": "Nothing notable today.", "output": "note", "x": 400, "y": 80 }
    ],
    "edges": [
      { "from": "fetch", "to": "triage", "from_port": "ok", "to_port": "in" },
      { "from": "triage", "to": "summarize", "from_port": "hot",   "if": "{{ gt (len .stories) 0 }}" },
      { "from": "triage", "to": "notify",    "from_port": "quiet" },
      { "from": "summarize", "to": "end" },
      { "from": "notify", "to": "end" }
    ],
    "entry": "fetch"
  }
}`

// BuildPrompt builds the instruction the model must answer. It pins the
// canonical Draft JSON shape and demands JSON-only output, optionally
// grounding the model in the supplied catalog and weaving in any answers
// the user gave to clarifying questions from a prior compile.
func BuildPrompt(intent string, catalog Catalog, answers map[string]string) string {
	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio intent compiler. ")
	sb.WriteString("Turn the user's plain-language intent into a draft automation workflow.\n\n")
	sb.WriteString("Output RULES:\n")
	sb.WriteString("- Respond with ONLY a single JSON object. No prose, no markdown, no code fences.\n")
	sb.WriteString("- The JSON MUST match this exact schema (field names and nesting):\n\n")
	sb.WriteString(canonicalExample)
	sb.WriteString("\n\n")
	sb.WriteString("Schema notes:\n")
	sb.WriteString("- trigger.type is one of: schedule, channel, webhook, manual.\n")
	sb.WriteString("- For schedule triggers, put a cron expression in trigger.config.cron.\n")
	sb.WriteString("- channels is a list of output channel names (e.g. \"telegram\", \"slack\", \"email\").\n")
	sb.WriteString("- flow.nodes[].kind is one of: tool, agent, branch. tool nodes set \"tool\"; agent nodes set \"agent\"; branch nodes set neither.\n")
	sb.WriteString("- Every flow must have an entry node and edges that terminate at \"end\".\n")
	sb.WriteString("- Prefer at least one tool node (to fetch/act) and one agent node (to reason/summarize).\n")
	sb.WriteString("- Build REAL multi-node graphs. Emit MULTIPLE tool and agent nodes when the intent has multiple steps; multiple agent (kind=agent) nodes are peers that hand off to each other.\n")
	sb.WriteString("- Use a branch node (kind=branch, no tool/agent) whenever the work forks on a CONDITION. A branch node fans out 2+ edges; put a Go-template predicate in edge.if (over flow vars, e.g. \"{{ gt (len .stories) 0 }}\"). Edges from a node are tried IN ORDER; the first whose if is truthy wins, so leave the LAST/fallback edge's if empty.\n")
	sb.WriteString("- When a node fans out to multiple targets, you MAY declare typed ports: set nodes[].outputs / inputs to lists of {\"name\":...,\"type\"?:...} and wire edges with from_port / to_port. A named from_port MUST be one of the From node's declared outputs; a named to_port MUST be one of the To node's declared inputs. Omit ports entirely for simple linear hops.\n\n")

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
	if len(catalog.Providers) > 0 {
		sb.WriteString("Available providers: ")
		sb.WriteString(strings.Join(catalog.Providers, ", "))
		sb.WriteString("\n")
	}
	if len(answers) > 0 {
		sb.WriteString("\nThe user already answered these clarifying questions — honor them in the draft:\n")
		for _, k := range sortedKeys(answers) {
			v := strings.TrimSpace(answers[k])
			if v == "" {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(v)
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\nIntent:\n")
	sb.WriteString(intent)
	sb.WriteString("\n")
	return sb.String()
}

// sortedKeys returns the map keys in deterministic (sorted) order so the
// rendered prompt is stable across runs and unit-testable.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ParseDraft tolerantly extracts a Draft from raw model output: it strips
// ```/```json code fences and any leading/trailing prose around the first
// JSON object, then strictly unmarshals. A malformed payload yields a clear
// error.
func ParseDraft(raw string) (Draft, error) {
	s := stripFences(strings.TrimSpace(raw))
	// Narrow to the outermost JSON object if the model wrapped it in prose.
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return Draft{}, fmt.Errorf("studio: no JSON object found in model output")
	}
	s = s[start : end+1]

	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var d Draft
	if err := dec.Decode(&d); err != nil {
		// Retry without strict field checking — be tolerant of extra keys
		// the model may add, but still fail loudly on structurally bad JSON.
		var d2 Draft
		if err2 := json.Unmarshal([]byte(s), &d2); err2 != nil {
			return Draft{}, fmt.Errorf("studio: parse draft: %w", err2)
		}
		return d2, nil
	}
	return d, nil
}

// stripFences removes a single leading/trailing markdown code fence
// (```json … ``` or ``` … ```) if present.
func stripFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line (``` or ```json).
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	// Drop the closing fence.
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// spec converts a Draft's flow into the sdk/reasoning FlowSpec for
// validation via reasoning.CompileFlow.
func (d Draft) spec() sdkr.FlowSpec {
	return sdkr.FlowSpec{
		Nodes: d.Flow.Nodes,
		Edges: d.Flow.Edges,
		Entry: d.Flow.Entry,
	}
}

// Compile runs the full pipeline: build a prompt, ask the model, parse the
// draft, validate the flow, and derive clarifying questions + notes. Hybrid:
// a structurally valid flow always yields a Result (draft + questions);
// only an unparseable response or a flow that fails CompileFlow is an error.
func Compile(ctx context.Context, llm LLM, intent string, catalog Catalog, answers map[string]string) (Result, error) {
	if strings.TrimSpace(intent) == "" {
		return Result{}, fmt.Errorf("studio: intent is required")
	}
	if llm == nil {
		return Result{}, fmt.Errorf("studio: no LLM configured")
	}

	prompt := BuildPrompt(intent, catalog, answers)
	raw, err := llm.Complete(ctx, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("studio: llm complete: %w", err)
	}

	draft, err := ParseDraft(raw)
	if err != nil {
		return Result{}, err
	}

	// Deterministic post-parse normalization: fill in an obvious trigger the
	// model left blank or mis-typed when the intent's phrasing clearly
	// implies one (S2.2). Modest and rule-based — it never overrides a
	// trigger the model already set correctly.
	normalizeTrigger(&draft, intent)

	// Deterministic graph normalization (Story M3): make the node kinds the
	// model implied explicit, so the returned/persisted draft carries the same
	// tool|agent|branch kind the engine will infer. This never changes graph
	// SHAPE (nodes/edges/entry/ports stay as emitted) — it only fills a blank
	// Kind from the node's Tool/Agent fields, mirroring CompileFlow.
	normalizeFlow(&draft)

	// The flow must compile — this is the hard contract. A model that emits a
	// structurally invalid multi-node/branch/port graph is rejected here and
	// NOT persisted; the caller sees a clear error.
	if _, err := reasoning.CompileFlow(draft.spec()); err != nil {
		return Result{}, fmt.Errorf("studio: compiled flow is invalid: %w", err)
	}

	questions, notes := analyze(draft)
	return Result{
		Workflow:    draft,
		Questions:   questions,
		Notes:       notes,
		Suggestions: suggestMissing(draft, catalog),
	}, nil
}

// suggestMissing inspects the (normalized) draft's flow and flags every tool
// and peer-agent it REFERENCES that is absent from the provided catalog, so
// the UI can offer to install/discover them (Stories S4.1/S4.3). It is a pure
// function — deterministic, side-effect-free, and unit-testable — so Compile
// can simply attach its output to the Result.
//
// Matching is tolerant: catalog entries and node references are compared after
// trimming surrounding whitespace and case-folding (a draft that says
// "HTTP_Fetch" matches a catalog "http_fetch"). The Catalog fields are plain
// string lists today; the comparison stays robust if they later carry
// decorated names.
//
// Design choice — actionable-only output: suggestMissing returns ONLY
// Installed:false entries (capabilities the draft needs but the catalog lacks).
// Present capabilities are intentionally omitted to keep the list a crisp
// to-do list for the UI rather than a full inventory. Callers that DO want the
// installed ones can use referencedCapabilities, which reports every
// referenced capability with its installed flag.
//
// Empty-catalog guard: if the caller supplied NO catalog context (no tools and
// no agents listed), suggestMissing returns nil. With nothing to compare
// against we cannot know what is or isn't installed, and emitting suggestions
// would be pure false positives. A caller wanting suggestions must pass the
// catalog of what they actually have.
func suggestMissing(draft Draft, cat Catalog) []Suggestion {
	all := referencedCapabilities(draft, cat)
	var out []Suggestion
	for _, s := range all {
		if !s.Installed {
			out = append(out, s)
		}
	}
	return out
}

// referencedCapabilities reports EVERY tool and peer-agent the draft's flow
// references, each tagged with whether it is present in the catalog
// (Installed). It is the shared core behind suggestMissing and is exported in
// spirit (lowercase, package-internal) for callers that want the full picture
// including already-installed capabilities. Like suggestMissing it honors the
// empty-catalog guard: with no catalog context it returns nil rather than
// guessing.
//
// References are de-duplicated and returned in deterministic order: all tools
// (in first-seen flow order) followed by all agents.
func referencedCapabilities(draft Draft, cat Catalog) []Suggestion {
	// Empty-catalog guard: no context => no claims about install state.
	if len(cat.Tools) == 0 && len(cat.Agents) == 0 {
		return nil
	}

	toolSet := newNameSet(cat.Tools)
	agentSet := newNameSet(cat.Agents)

	var out []Suggestion
	seenTool := map[string]bool{}
	seenAgent := map[string]bool{}

	// Tools first (first-seen flow order), then agents, for stable output.
	for _, n := range draft.Flow.Nodes {
		t := strings.TrimSpace(n.Tool)
		if t == "" || seenTool[normalizeName(t)] {
			continue
		}
		seenTool[normalizeName(t)] = true
		installed := toolSet[normalizeName(t)]
		out = append(out, Suggestion{
			Kind:      "tool",
			Name:      t,
			Installed: installed,
			Reason:    capabilityReason("tool", t, installed),
		})
	}
	for _, n := range draft.Flow.Nodes {
		a := strings.TrimSpace(n.Agent)
		if a == "" || seenAgent[normalizeName(a)] {
			continue
		}
		seenAgent[normalizeName(a)] = true
		installed := agentSet[normalizeName(a)]
		out = append(out, Suggestion{
			Kind:      "agent",
			Name:      a,
			Installed: installed,
			Reason:    capabilityReason("agent", a, installed),
		})
	}
	return out
}

// capabilityReason renders a helpful, human-readable reason for a suggestion.
func capabilityReason(kind, name string, installed bool) string {
	if installed {
		return fmt.Sprintf("workflow references %s %q which is already installed", kind, name)
	}
	return fmt.Sprintf("workflow references %s %q which isn't installed", kind, name)
}

// newNameSet builds a lookup set of normalized capability names from a catalog
// list. Entries are case-folded and trimmed so matching is tolerant.
func newNameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		set[normalizeName(n)] = true
	}
	return set
}

// normalizeName canonicalizes a capability name for tolerant matching.
func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// normalizeTrigger applies modest, DETERMINISTIC trigger inference (Story
// S2.2): it inspects the plain-language intent and fills or corrects the
// draft's Trigger when the phrasing clearly implies a kind the model left
// blank, mis-typed, or under-specified. It is intentionally conservative:
//
//   - It never changes a trigger the model already set to a recognized type
//     UNLESS that trigger is a schedule missing its cron and the intent
//     supplies an unambiguous cadence (then it fills trigger.config.cron).
//   - Schedule phrasings ("every morning", "every weekday", "daily",
//     "at 8am", "hourly", …) map to type=schedule with a sane cron.
//   - Channel phrasings ("when someone messages", "on telegram", "reply
//     to messages") map to type=channel.
//   - Webhook phrasings ("webhook", "when X posts to", "on POST") map to
//     type=webhook.
//
// Anything ambiguous is left untouched so analyze() can raise a clarifying
// question. This is not NL parsing — it is a small keyword table.
func normalizeTrigger(d *Draft, intent string) {
	if d == nil {
		return
	}
	lc := strings.ToLower(intent)
	typ := strings.ToLower(strings.TrimSpace(d.Trigger.Type))
	known := typ == "schedule" || typ == "channel" || typ == "webhook" || typ == "manual"

	inferred := inferTriggerType(lc)

	// Fill a missing/unrecognized type from the intent when we can.
	if !known && inferred != "" {
		d.Trigger.Type = inferred
		typ = inferred
		known = true
	}

	// For schedule triggers (whether the model set them or we just did),
	// ensure a cron is present: derive one from the intent if the model
	// left config.cron blank.
	if typ == "schedule" {
		cron, _ := d.Trigger.Config["cron"].(string)
		if strings.TrimSpace(cron) == "" {
			if c := inferCron(lc); c != "" {
				if d.Trigger.Config == nil {
					d.Trigger.Config = map[string]any{}
				}
				d.Trigger.Config["cron"] = c
			}
		}
	}

	// A still-empty/unknown type with no schedule/channel/webhook signal is
	// left alone — analyze() asks the user. Avoid guessing "manual" here.
	_ = known
}

// inferTriggerType returns the trigger type implied by the intent, or "" when
// no clear signal is present. Schedule wins over channel/webhook when both
// appear, because a cadence phrase ("every morning … on telegram") describes
// WHEN it runs while the channel is the OUTPUT.
func inferTriggerType(lc string) string {
	scheduleCues := []string{
		"every morning", "every weekday", "every day", "everyday",
		"each morning", "each day", "daily", "weekly", "hourly",
		"every hour", "every monday", "every week", "at 8am", "at 8 am",
		"at noon", "at midnight", "schedule", "cron", "o'clock",
	}
	for _, cue := range scheduleCues {
		if strings.Contains(lc, cue) {
			return "schedule"
		}
	}
	// Generic "every <time>" / "at <n>(am|pm)" cadence.
	if hasAtClock(lc) {
		return "schedule"
	}

	webhookCues := []string{"webhook", "on post", "posts to", "http callback", "incoming request"}
	for _, cue := range webhookCues {
		if strings.Contains(lc, cue) {
			return "webhook"
		}
	}

	channelCues := []string{
		"when someone messages", "when someone sends", "on telegram",
		"on slack", "on discord", "on whatsapp", "reply to messages",
		"when a message", "when i message", "respond to messages",
		"someone dms", "incoming message",
	}
	for _, cue := range channelCues {
		if strings.Contains(lc, cue) {
			return "channel"
		}
	}
	return ""
}

// inferCron maps common cadence phrasings onto a cron expression. Returns ""
// when the cadence is present-but-unspecified (e.g. bare "every day" with no
// time) so the caller leaves cron blank and analyze() asks for the time.
func inferCron(lc string) string {
	hour := inferHour(lc)

	switch {
	case strings.Contains(lc, "every weekday") || strings.Contains(lc, "weekdays"):
		if hour < 0 {
			hour = 8
		}
		return cronExpr(hour, "1-5")
	case strings.Contains(lc, "every morning") || strings.Contains(lc, "each morning"):
		if hour < 0 {
			hour = 8
		}
		return cronExpr(hour, "*")
	case strings.Contains(lc, "hourly") || strings.Contains(lc, "every hour"):
		return "0 * * * *"
	case strings.Contains(lc, "daily") || strings.Contains(lc, "every day") ||
		strings.Contains(lc, "everyday") || strings.Contains(lc, "each day"):
		if hour < 0 {
			return "" // cadence is clear, time is not — let analyze() ask.
		}
		return cronExpr(hour, "*")
	case strings.Contains(lc, "at midnight"):
		return cronExpr(0, "*")
	case strings.Contains(lc, "at noon"):
		return cronExpr(12, "*")
	}

	// Bare "at 8am" with no cadence word implies a daily schedule.
	if hour >= 0 {
		return cronExpr(hour, "*")
	}
	return ""
}

// cronExpr builds a "minute hour * * dow" cron at minute 0.
func cronExpr(hour int, dow string) string {
	return fmt.Sprintf("0 %d * * %s", hour, dow)
}

// hasAtClock reports whether the intent contains an "at <n>(am|pm)" or
// "<n> o'clock" time signal.
func hasAtClock(lc string) bool {
	return inferHour(lc) >= 0
}

// inferHour extracts a 0-23 hour from phrasings like "at 8am", "at 8 am",
// "8pm", "at noon", "at midnight". Returns -1 when no hour is found.
func inferHour(lc string) int {
	if strings.Contains(lc, "midnight") {
		return 0
	}
	if strings.Contains(lc, "noon") {
		return 12
	}
	// Scan for "<digits> am/pm" or "at <digits>".
	for i := 0; i < len(lc); i++ {
		if lc[i] < '0' || lc[i] > '9' {
			continue
		}
		j := i
		for j < len(lc) && lc[j] >= '0' && lc[j] <= '9' {
			j++
		}
		n := 0
		for k := i; k < j; k++ {
			n = n*10 + int(lc[k]-'0')
		}
		rest := strings.TrimLeft(lc[j:], " ")
		switch {
		case strings.HasPrefix(rest, "am"):
			if n >= 1 && n <= 12 {
				if n == 12 {
					n = 0
				}
				return n
			}
		case strings.HasPrefix(rest, "pm"):
			if n >= 1 && n <= 12 {
				if n != 12 {
					n += 12
				}
				return n
			}
		case strings.HasPrefix(rest, "o'clock") || strings.HasPrefix(rest, "oclock"):
			if n >= 0 && n <= 23 {
				return n
			}
		}
		// Plain 24h hour right after "at " (e.g. "at 14:00").
		if i >= 3 && lc[i-3:i] == "at " && n >= 0 && n <= 23 {
			// Only treat as hour if followed by ":" or end/space, to avoid
			// matching quantities like "at 5 stories".
			if j >= len(lc) || lc[j] == ':' {
				return n
			}
		}
		i = j
	}
	return -1
}

// normalizeFlow makes the implied node kinds explicit (Story M3). For each
// node whose Kind is blank it derives tool|agent|branch from the node's
// Tool/Agent fields — the exact same inference reasoning.CompileFlow performs
// — so the draft the compiler returns (and the one Studio later persists)
// carries the kind the engine will execute. It is purely additive: it never
// rewrites a Kind the model already set, never touches edges/ports/entry, and
// never invents nodes. Branch and multi-agent graphs round-trip unchanged.
func normalizeFlow(d *Draft) {
	if d == nil {
		return
	}
	for i := range d.Flow.Nodes {
		n := &d.Flow.Nodes[i]
		if strings.TrimSpace(n.Kind) != "" {
			continue
		}
		switch {
		case strings.TrimSpace(n.Tool) != "":
			n.Kind = sdkr.FlowNodeTool
		case strings.TrimSpace(n.Agent) != "":
			n.Kind = sdkr.FlowNodeAgent
		default:
			n.Kind = sdkr.FlowNodeBranch
		}
	}
}

// analyze derives clarifying questions for missing essentials and notes
// explaining what the compiler inferred. It never blocks: a draft with gaps
// still produces a Result, with questions describing the gaps.
func analyze(d Draft) ([]Question, []string) {
	var questions []Question
	var notes []string

	notes = append(notes, fmt.Sprintf("Inferred trigger type %q from the intent.", d.Trigger.Type))

	switch d.Trigger.Type {
	case "schedule":
		cron, _ := d.Trigger.Config["cron"].(string)
		if strings.TrimSpace(cron) == "" {
			questions = append(questions, Question{
				ID:   "schedule_time",
				Text: "When should this run? Provide a time or cron schedule.",
			})
			notes = append(notes, "No schedule time was specified; asking for one.")
		} else {
			notes = append(notes, fmt.Sprintf("Scheduled with cron %q.", cron))
		}
	case "channel", "webhook", "manual":
		notes = append(notes, fmt.Sprintf("Trigger %q needs no schedule.", d.Trigger.Type))
	default:
		questions = append(questions, Question{
			ID:      "trigger_type",
			Text:    "How should this workflow be triggered?",
			Options: []string{"schedule", "channel", "webhook", "manual"},
		})
		notes = append(notes, "Trigger type was missing or unrecognized; asking how to start the workflow.")
	}

	if len(d.Channels) == 0 {
		questions = append(questions, Question{
			ID:      "output_channel",
			Text:    "Where should the results be delivered?",
			Options: []string{"telegram", "slack", "discord", "email"},
		})
		notes = append(notes, "No output channel was specified; asking where results go.")
	} else {
		notes = append(notes, fmt.Sprintf("Delivering to channels: %s.", strings.Join(d.Channels, ", ")))
	}

	notes = append(notes, fmt.Sprintf("Flow has %d node(s) entering at %q.", len(d.Flow.Nodes), d.Flow.Entry))

	return questions, notes
}
