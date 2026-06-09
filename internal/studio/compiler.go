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

// Result is the compile response: a draft, clarifying questions, and
// transparency notes about what was inferred and why.
type Result struct {
	Workflow  Draft      `json:"workflow"`
	Questions []Question `json:"questions"`
	Notes     []string   `json:"notes"`
}

// canonicalExample is the shape the model is instructed to emit. It is
// embedded verbatim in the prompt so the model has a concrete target.
const canonicalExample = `{
  "name": "Weekday HN Digest",
  "trigger": { "type": "schedule", "config": { "cron": "0 8 * * 1-5" } },
  "channels": ["telegram"],
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{\"url\":\"https://hacker-news.firebaseio.com/v0/topstories.json\"}", "output": "stories", "x": 0, "y": 0 },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "Summarize the top 5: {{.stories}}", "output": "summary", "x": 200, "y": 0 }
    ],
    "edges": [
      { "from": "fetch", "to": "summarize" },
      { "from": "summarize", "to": "end" }
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
	sb.WriteString("- flow.nodes[].kind is one of: tool, agent, branch. tool nodes set \"tool\"; agent nodes set \"agent\".\n")
	sb.WriteString("- Every flow must have an entry node and edges that terminate at \"end\".\n")
	sb.WriteString("- Prefer at least one tool node (to fetch/act) and one agent node (to reason/summarize).\n\n")

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

	// The flow must compile — this is the hard contract.
	if _, err := reasoning.CompileFlow(draft.spec()); err != nil {
		return Result{}, fmt.Errorf("studio: compiled flow is invalid: %w", err)
	}

	questions, notes := analyze(draft)
	return Result{
		Workflow:  draft,
		Questions: questions,
		Notes:     notes,
	}, nil
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
