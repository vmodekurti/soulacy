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
	"github.com/soulacy/soulacy/internal/studio/codeclass"
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
	Agents    []string           `json:"agents,omitempty"`
	Tools     []string           `json:"tools,omitempty"`
	Providers []string           `json:"providers,omitempty"`
	Skills    []CatalogSkill     `json:"skills,omitempty"`
	MCP       []CatalogMCPServer `json:"mcp,omitempty"`
}

// CatalogMCPServer is one connected MCP server and the tools it exposes, so the
// model can wire MCP tools when the intent names a server or a capability it
// provides ("use the github mcp", "create a notebook").
type CatalogMCPServer struct {
	Server string           `json:"server"`
	Tools  []CatalogMCPTool `json:"tools,omitempty"`
}

// CatalogMCPTool is one callable MCP tool. Name is the EXACT value to put in a
// tool node's "tool" field; Description lets the model map a loose reference to
// the right tool.
type CatalogMCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CatalogSkill is an installed Agent Skill the model may reference via a
// read_skill node. Name is the EXACT skill name to put in skill_name; the
// Description lets the model map a loose reference in the intent ("yahoo
// finance", "stock data") to the right installed skill ("yfinance").
type CatalogSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
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
	Nodes  []sdkr.FlowNode `json:"nodes"`
	Edges  []sdkr.FlowEdge `json:"edges,omitempty"`
	Entry  string          `json:"entry,omitempty"`
	Output string          `json:"output,omitempty"` // node id whose result is the flow's output (default: last node)
}

// NewAgent defines a full profile for an agent the model invented.
type NewAgent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	SystemPrompt string `json:"system_prompt"`
}

// Recommendation is the compiler's assessment of which execution model best
// fits the intent. It's advisory: the compiler still emits a workflow graph so
// the editor has something to show, but the recommendation tells the user when
// a reasoning strategy (react/plan_execute) would serve the task better than a
// frozen graph — e.g. when steps depend on values only known at run time.
type Recommendation struct {
	Mode      string `json:"mode"`      // workflow | react | plan_execute
	Rationale string `json:"rationale"` // 1–2 sentences explaining the pick
}

// Draft is the workflow the compiler produces.
type Draft struct {
	Name           string          `json:"name"`
	SystemPrompt   string          `json:"system_prompt,omitempty"`
	Intent         string          `json:"intent,omitempty"` // the prompt that generated this workflow (Studio editor)
	Trigger        Trigger         `json:"trigger"`
	Channels       []string        `json:"channels,omitempty"`
	Flow           Flow            `json:"flow"`
	NewAgents      []NewAgent      `json:"new_agents,omitempty"`
	Recommendation *Recommendation `json:"recommendation,omitempty"`
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
  "name": "Weekday AI Digest",
  "system_prompt": "You are a specialized AI news curator. You wake up every weekday morning to compile a brief, high-signal digest of the latest artificial intelligence research and product news. You execute your workflow methodically: searching the web, filtering out noise, and delegating the final synthesis to your summarizer agent. Your tone is professional and concise. When encountering empty search results or errors, you gracefully emit a fallback message rather than failing. Stick strictly to the defined workflow steps.",
  "trigger": { "type": "schedule", "config": { "cron": "0 8 * * 1-5" } },
  "channels": ["telegram"],
  "flow": {
    "nodes": [
      { "id": "search_ai_news", "kind": "tool", "tool": "web_search",
        "description": "Search the web for today's top AI news",
        "input": "{\"query\": \"latest artificial intelligence research and product news\", \"num_results\": 10}",
        "output": "search", "x": 0, "y": 0 },
      { "id": "pick_top_articles", "kind": "python",
        "description": "Parse the search results and keep the top 5 as {title,url}",
        "code": "def run(inputs):\n    try:\n        res = inputs.get('search', {})\n        if not isinstance(res, dict):\n            res = {}\n        items = res.get('results') or res.get('items') or []\n        return [{'title': it.get('title', ''), 'url': it.get('url') or it.get('link', '')} for it in items[:5] if isinstance(it, dict)]\n    except Exception as e:\n        return []",
        "output": "articles", "x": 220, "y": 0 },
      { "id": "have_articles", "kind": "branch",
        "description": "Continue only if at least one article was found",
        "inputs":  [ { "name": "in" } ],
        "outputs": [ { "name": "hot" }, { "name": "quiet" } ], "x": 440, "y": 0 },
      { "id": "write_digest", "kind": "agent", "agent": "summarizer",
        "description": "Summarize the articles into a 5-bullet digest",
        "input": "You are an expert AI news curator. Your task is to write a concise, 5-bullet AI digest based on the provided articles. For each article, write a single clear sentence summarizing the takeaway, followed immediately by the URL. Do not include any introductory text or fluff. Output strictly as a markdown bulleted list.\n\nSource Articles:\n{{ toJson .articles }}",
        "output": "digest", "x": 660, "y": -80 },
      { "id": "say_quiet", "kind": "agent", "agent": "notifier",
        "description": "Send a short 'nothing notable today' note",
        "input": "Reply exactly with this fallback message: No notable AI news today. Enjoy the quiet!",
        "output": "note", "x": 660, "y": 80 }
    ],
    "edges": [
      { "from": "search_ai_news", "to": "pick_top_articles" },
      { "from": "pick_top_articles", "to": "have_articles" },
      { "from": "have_articles", "to": "write_digest", "from_port": "hot", "if": "{{ gt (len .articles) 0 }}" },
      { "from": "have_articles", "to": "say_quiet", "from_port": "quiet" },
      { "from": "write_digest", "to": "end" },
      { "from": "say_quiet", "to": "end" }
    ],
    "entry": "search_ai_news"
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
	sb.WriteString("- flow.nodes[].kind is one of: tool, agent, branch, python. tool nodes set \"tool\"; agent nodes set \"agent\"; python nodes set \"code\" (inline Python); branch nodes set none of these.\n")
	sb.WriteString("- Every flow must have an entry node and edges that terminate at \"end\".\n")
	sb.WriteString("- Prefer at least one tool node (to fetch/act) and one agent node (to reason/summarize).\n")
	sb.WriteString("- PRODUCTION MINDSET: Treat every intent as a production workload. Handle empty states, edge cases, and failure modes explicitly (e.g., using a branch node to check if an API returned data before proceeding to processing it).\n")
	sb.WriteString("- Build REAL multi-node graphs. Emit MULTIPLE tool and agent nodes when the intent has multiple steps; multiple agent (kind=agent) nodes are peers that hand off to each other.\n")
	sb.WriteString("- Use a branch node (kind=branch, no tool/agent) whenever the work forks on a CONDITION. A branch node fans out 2+ edges; put a Go-template predicate in edge.if (over flow vars, e.g. \"{{ gt (len .stories) 0 }}\"). Edges from a node are tried IN ORDER; the first whose if is truthy wins, so leave the LAST/fallback edge's if empty.\n")
	sb.WriteString("- When a node fans out to multiple targets, you MAY declare typed ports: set nodes[].outputs / inputs to lists of {\"name\":...,\"type\"?:...} and wire edges with from_port / to_port. A named from_port MUST be one of the From node's declared outputs; a named to_port MUST be one of the To node's declared inputs. Omit ports entirely for simple linear hops.\n\n")
	sb.WriteString("- Use a python node (kind=python) ONLY for glue the available tools can't do: shelling out to a local CLI the user says is installed, parsing a tool's raw output, reshaping data, or calling an external command. Put the script in nodes[].code as a function `def run(inputs):` where `inputs` is a dict of the node's rendered input (JSON) and the value you RETURN becomes the node's output. Always prefer an existing tool or agent when one fits; reach for python only for the gaps, and keep the code minimal. Do NOT invent tool names for capabilities that don't exist — write a python node instead.\n\n")
	sb.WriteString("- system_prompt: Write a rich, conversational system prompt for the overarching agent (2-4 sentences). Give it a clear persona, define its goal based on the intent, and instruct it to faithfully execute the steps in the graph. Do NOT write a mechanical list of steps (the framework will attach those automatically); write the persona and domain instructions.\n")
	sb.WriteString("- CRITICAL — every node MUST carry its REAL instruction derived from the intent, never a placeholder or empty field:\n")
	sb.WriteString("    * tool nodes: set \"input\" to the tool's arguments as a JSON object built from the intent. A search/web tool node MUST contain the actual query, e.g. {\"query\":\"latest AI research articles\",\"num_results\":10}. Never leave a tool node's input empty when it takes arguments.\n")
	sb.WriteString("    * agent nodes: set \"input\" to the full, concrete, highly specific task prompt for that agent (what to do, with which data, what persona to adopt, edge case rules, explicit output format) — not a one-word label or 1-sentence stub. Inject an upstream output with {{ toJson .var }} for a JSON value, or {{ .var }} for a plain string.\n")
	sb.WriteString("    * python nodes: write COMPLETE, robust, runnable code in \"code\". NEVER a stub, `pass`, `...`, TODO, or `return inputs`. The code automatically receives ALL prior node outputs as the `inputs` dict (keyed by each node's output var), so read e.g. inputs.get(\"articles\"). Code MUST include try/except blocks, defensive dictionary lookups (.get()), type checking, and fallback values. Never blindly index into dicts/lists. Always define def run(inputs).\n")
	sb.WriteString("- TOOL OUTPUT CONTRACT — the web_search tool returns a JSON OBJECT: {\"query\":\"...\",\"result_count\":N,\"results\":[{\"title\":\"...\",\"url\":\"...\",\"content\":\"...\"}]}. A python node consuming it MUST read the dict, e.g. `data = inputs.get(\"<search_var>\", {})` then `for it in data.get(\"results\", []): it[\"title\"], it[\"url\"], it[\"content\"]`. Defensive code only: treat a non-dict value as empty (e.g. `if not isinstance(data, dict): data = {}`) so a missing or error result never crashes the node.\n")
	sb.WriteString("    * Pull concrete values straight from the user's words: if they ask for \"top 10 AI articles\", the query is about AI articles and the count is 10 — bake that in, do not leave it generic.\n")
	sb.WriteString("- Give every node a meaningful \"output\" var name so downstream nodes can reference it (e.g. \"articles\", \"notebook_id\", \"audio_url\").\n")
	sb.WriteString("- Give every node a one-line \"description\" stating concretely what THAT node does (e.g. \"Search the web for today's AI news\", \"Keep the top 5 articles as {title,url}\") — not a vague label. The node ids should also read as verbs (search_ai_news, pick_top_articles), not generic names like node1.\n")
	sb.WriteString("- If the intent needs reasoning and no suitable agent exists in the Available agents list, you MAY invent a new peer agent. If you do, you MUST provide its full definition in a `new_agents` array at the top level of your output JSON (alongside `flow`), including its `id`, `name`, `description`, and `system_prompt`.\n")
	sb.WriteString("- Do NOT invent tool names. Do the work in a python node or an existing tool if no tool exists.\n")
	sb.WriteString("- SKILLS: when the intent references a capability/data source by a loose name (e.g. \"yahoo finance\", \"stock data\", \"web research\"), DO NOT invent a skill name. Look in the \"Available skills\" list below and MATCH the reference to the closest installed skill by its name and description, then emit a tool node with \"tool\":\"read_skill\" and \"input\":{\"skill_name\":\"<EXACT installed skill name>\"} (e.g. a request for \"yahoo finance\" → {\"skill_name\":\"yfinance\"}). Only reference skills that appear in the Available skills list; if none matches, do the work with an existing tool or a python node instead of inventing a read_skill.\n")
	sb.WriteString("- MCP SERVERS: connected MCP servers and the tools they expose are listed under \"Available MCP servers\" below. When the intent names a server or a capability one provides (e.g. \"use the github mcp\", \"create a notebook in notebooklm\", \"open a Linear issue\"), emit a tool node whose \"tool\" is the EXACT MCP tool name from that list, with \"input\" set to that tool's arguments built from the intent. Match loose references to the closest listed MCP tool by name + description. Do NOT invent MCP tool names; if no listed MCP tool fits, use another existing tool or a python node.\n")
	sb.WriteString("- EXECUTION MODE: judge which execution model best fits the intent and include a top-level \"recommendation\": {\"mode\":\"workflow|react|plan_execute\", \"rationale\":\"<1-2 sentences>\"}. Pick \"workflow\" for a fixed, deterministic pipeline — the same steps in the same order every run, knowable up front (e.g. \"each morning search X, summarize, post to Telegram\"); the graph you emit IS the artifact. Pick \"react\" when steps DEPEND on intermediate results or the task is exploratory/decision-heavy — an id from one tool feeds the next, the number or choice of tool calls varies per run, or the user says \"manage\", \"research and then\", \"figure out\" (most multi-step MCP jobs like driving NotebookLM are react); a frozen graph is brittle, so the agent should loop think→act→observe. Pick \"plan_execute\" for long, multi-phase jobs worth decomposing first. ALWAYS still emit a best-effort flow, but set the recommendation honestly even when it is not \"workflow\".\n\n")

	if len(catalog.Skills) > 0 {
		sb.WriteString("Available skills (use the EXACT name in read_skill's skill_name):\n")
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
		sb.WriteString("Available MCP servers and their tools (use the EXACT tool name in a tool node):\n")
		for _, srv := range catalog.MCP {
			name := strings.TrimSpace(srv.Server)
			if name == "" {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(name)
			if len(srv.Tools) == 0 {
				sb.WriteString(" (connected; no tools exposed)")
			}
			sb.WriteString("\n")
			for _, t := range srv.Tools {
				tn := strings.TrimSpace(t.Name)
				if tn == "" {
					continue
				}
				desc := strings.TrimSpace(t.Description)
				// MCP descriptions carry the usage/workflow hints the model needs to
				// pick the right tool + sequence (e.g. "add_source action", "poll
				// status until completed"), so keep them generous, not 1-line.
				if len(desc) > 400 {
					desc = desc[:400] + "…"
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

	// Auto-declare any edge-referenced port the model forgot to list on a node.
	// reasoning.CompileFlow is strict (a named from_port/to_port MUST appear in
	// the node's declared Outputs/Inputs), and models occasionally name a port
	// they didn't declare — which would otherwise throw away an entire
	// otherwise-valid draft over one cosmetic wiring slip.
	reconcilePorts(&draft)

	// Classify each Custom Python node's required capabilities from its code so
	// the canvas can show them and save/consent can gate on them.
	classifyFlowNodes(&draft.Flow)

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
		case strings.TrimSpace(n.Code) != "":
			n.Kind = sdkr.FlowNodePython
		default:
			n.Kind = sdkr.FlowNodeBranch
		}
	}
}

// reconcilePorts makes every edge's port references self-consistent so the
// strict reasoning.CompileFlow port check passes. For each edge with a named
// from_port/to_port that the referenced node did not declare, we ADD that port
// to the node's Outputs/Inputs (preserving the model's intended named handle)
// rather than failing the compile. Edges to the terminal ("end"/"") have no
// target node and are skipped naturally by the id lookup. Idempotent.
func reconcilePorts(d *Draft) {
	if d == nil {
		return
	}
	idx := make(map[string]*sdkr.FlowNode, len(d.Flow.Nodes))
	for i := range d.Flow.Nodes {
		idx[d.Flow.Nodes[i].ID] = &d.Flow.Nodes[i]
	}
	ensure := func(ports *[]sdkr.FlowPort, name string) {
		if name == "" {
			return
		}
		for _, port := range *ports {
			if port.Name == name {
				return
			}
		}
		*ports = append(*ports, sdkr.FlowPort{Name: name})
	}
	for _, e := range d.Flow.Edges {
		if n, ok := idx[e.From]; ok {
			ensure(&n.Outputs, e.FromPort)
		}
		if n, ok := idx[e.To]; ok {
			ensure(&n.Inputs, e.ToPort)
		}
	}
}

// classifyFlowNodes sets Requires on every Custom Python node from its inline
// Code (internal/studio/codeclass). Deterministic and idempotent; a node with
// no code, or pure-data code, ends up with no Requires (ReadOnly). Nodes that
// reference a deployed tool (no inline Code) are left untouched.
func classifyFlowNodes(f *Flow) {
	if f == nil {
		return
	}
	for i := range f.Nodes {
		n := &f.Nodes[i]
		if n.Kind != sdkr.FlowNodePython || strings.TrimSpace(n.Code) == "" {
			continue
		}
		n.Requires = codeclass.Classify(n.Code).Requires
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
