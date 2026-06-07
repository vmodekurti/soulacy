// Package agent defines the agent definition format (SOUL.yaml) and related types.
// An agent is the atomic unit of intelligence in Soulacy — it binds a system prompt,
// a set of tools, memory access rules, channel bindings, and LLM configuration together
// into a single deployable entity.
package agent

import (
	"time"

	"github.com/soulacy/soulacy/sdk/reasoning"
)

// TriggerKind describes how an agent is activated.
type TriggerKind string

const (
	TriggerChannel  TriggerKind = "channel"  // activated by an inbound channel message
	TriggerCron     TriggerKind = "cron"     // activated on a cron schedule
	TriggerOneShot  TriggerKind = "oneshot"  // activated once at a specific time
	TriggerWebhook  TriggerKind = "webhook"  // activated by an HTTP POST to its endpoint
	TriggerInternal TriggerKind = "internal" // activated programmatically by another agent
)

// MemoryPolicy controls how the agent reads and writes memory.
type MemoryPolicy struct {
	ReadScopes  []string `yaml:"read_scopes"  json:"read_scopes"`
	WriteScopes []string `yaml:"write_scopes" json:"write_scopes"`
	MaxTokens   int      `yaml:"max_tokens"   json:"max_tokens"`
}

// ReasoningConfig configures the multi-step reasoning loop for an agent (CFG-01).
// When Strategy is empty, the agent uses the classic single-call behaviour.
type ReasoningConfig struct {
	// Strategy is "react" or "plan_execute". Empty = loop disabled.
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	// Backend is automatically derived from llm.provider — no need to set this.
	// The reasoning loop uses the same provider as the agent's LLM config so there
	// is exactly one place to configure the model. Supported providers:
	//   ollama    — local Ollama, uses llm.model for Think, qwen2.5:72b for Plan/Reflect
	//   anthropic — Claude API, uses llm.model (default claude-sonnet-4-6)
	//   openai    — OpenAI or any OpenAI-compatible endpoint (Groq, Together, vLLM)
	// Any unsupported provider falls back to Ollama.
	Backend string `yaml:"backend,omitempty" json:"backend,omitempty"`
	// MaxSteps is the hard ceiling for ReAct iterations (default 8).
	MaxSteps int `yaml:"max_steps,omitempty" json:"max_steps,omitempty"`
	// MaxPlanSteps caps plan decomposition depth for plan_execute (default 6).
	MaxPlanSteps int `yaml:"max_plan_steps,omitempty" json:"max_plan_steps,omitempty"`
	// StepTimeout is the per-step context deadline (e.g. "30s").
	StepTimeout string `yaml:"step_timeout,omitempty" json:"step_timeout,omitempty"`
	// TotalTimeout is the whole-task deadline (e.g. "180s").
	TotalTimeout string `yaml:"total_timeout,omitempty" json:"total_timeout,omitempty"`
}

// BrainMemoryConfig controls long-term agent memory behaviour (CFG-02).
type BrainMemoryConfig struct {
	Episodic   EpisodicMemoryConfig   `yaml:"episodic,omitempty"   json:"episodic,omitempty"`
	Semantic   SemanticMemoryConfig   `yaml:"semantic,omitempty"   json:"semantic,omitempty"`
	Procedural ProceduralMemoryConfig `yaml:"procedural,omitempty" json:"procedural,omitempty"`
}

// EpisodicMemoryConfig controls episodic task history injection.
type EpisodicMemoryConfig struct {
	Enabled   bool `yaml:"enabled,omitempty"    json:"enabled,omitempty"`
	MaxInject int  `yaml:"max_inject,omitempty" json:"max_inject,omitempty"`
}

// SemanticMemoryConfig controls semantic knowledge chunk injection.
type SemanticMemoryConfig struct {
	Enabled   bool `yaml:"enabled,omitempty"    json:"enabled,omitempty"`
	MaxInject int  `yaml:"max_inject,omitempty" json:"max_inject,omitempty"`
}

// ProceduralMemoryConfig controls procedural rule injection and auto-update.
type ProceduralMemoryConfig struct {
	Enabled    bool `yaml:"enabled,omitempty"     json:"enabled,omitempty"`
	AutoUpdate bool `yaml:"auto_update,omitempty" json:"auto_update,omitempty"`
}

// LLMConfig specifies which model and parameters to use.
type LLMConfig struct {
	Provider    string  `yaml:"provider"           json:"provider"`
	Model       string  `yaml:"model"              json:"model"`
	Temperature float64 `yaml:"temperature"        json:"temperature"`
	MaxTokens   int     `yaml:"max_tokens"         json:"max_tokens"`
	BaseURL     string  `yaml:"base_url,omitempty" json:"base_url,omitempty"`

	// OutputSchema, when set, constrains the final reply to valid JSON matching
	// the supplied JSON Schema. Translated per provider:
	//   • OpenAI  → response_format json_schema (strict)
	//   • Gemini  → generationConfig.responseSchema + responseMimeType
	//   • Anthropic → forced tool_use with the schema as the tool's input_schema
	//   • Ollama  → format: <schema> (Ollama ≥ 0.5)
	// The engine also re-prompts once on failure if the reply doesn't parse.
	OutputSchema map[string]any `yaml:"output_schema,omitempty" json:"output_schema,omitempty"`

	// ToolChoice, when non-empty, constrains how the model picks tools on the
	// FIRST turn only (subsequent turns always use "auto" so the model can
	// synthesize the final answer once tools have produced their results).
	// Accepted values mirror OpenAI / Ollama tool_choice semantics:
	//
	//   ""               (default) — model decides freely on every turn.
	//   "auto"           — equivalent to default.
	//   "none"           — model MUST NOT call any tool on turn 1.
	//   "required"       — model MUST call at least one tool on turn 1.
	//   "<tool_name>"    — model MUST call this specific tool on turn 1.
	//                       Pass the full tool name, e.g. "agent__researcher".
	//
	// Use this when a workflow REQUIRES delegation — e.g. a writer agent that
	// should always consult a researcher peer before composing. Without this,
	// local models often skip the tool call and answer from training data.
	ToolChoice string `yaml:"tool_choice,omitempty" json:"tool_choice,omitempty"`

	// AllowedProviders is an opt-in guard against the "I clicked the wrong
	// model in the GUI dropdown and burned billed credit" failure mode.
	// When set, the engine refuses to start a run whose `Provider` is not
	// on this list, returning a clear actionable error instead of dialling
	// out to a paid API the agent was never meant to use.
	//
	// Semantics:
	//   nil / absent — current behavior; any configured provider is allowed.
	//   [name, …]    — provider MUST be one of these (case-sensitive,
	//                  matched against LLMConfig.Provider). An empty []
	//                  also means "no providers allowed" — bricks the
	//                  agent intentionally, useful for a disabled-state
	//                  shim agent.
	//
	// Recommended for any cron-triggered agent that calls a self-hosted
	// model (ollama / lm-studio): set `allowed_providers: [ollama]` and
	// the agent can never accidentally hit anthropic / openai / gemini,
	// no matter how the GUI dropdown gets fat-fingered.
	AllowedProviders []string `yaml:"allowed_providers,omitempty" json:"allowed_providers,omitempty"`
}

// SecurityConfig holds access-control settings for an agent.
// All checks are enforced in Go before the LLM is invoked — they cannot be
// overridden by prompt injection or model behavior.
type SecurityConfig struct {
	// Passphrase, when non-empty, requires every new session to present this
	// exact string before the agent will answer any message. The engine tracks
	// verified sessions in memory; once verified the check is not repeated for
	// the remainder of that session. Comparison is case-sensitive.
	Passphrase string `yaml:"passphrase,omitempty" json:"passphrase,omitempty"`

	// PassphrasePrompt is the message shown to unverified users.
	// Defaults to "🔒 Please provide your access passphrase to continue."
	PassphrasePrompt string `yaml:"passphrase_prompt,omitempty" json:"passphrase_prompt,omitempty"`
}

// ToolDef describes a tool the agent can invoke.
type ToolDef struct {
	Name        string         `yaml:"name"                   json:"name"`
	Description string         `yaml:"description"            json:"description"`
	PythonFile  string         `yaml:"python_file,omitempty"  json:"python_file,omitempty"`
	Inline      string         `yaml:"inline,omitempty"       json:"inline,omitempty"`
	Parameters  map[string]any `yaml:"parameters"             json:"parameters,omitempty"`

	// Timeout overrides the engine's global runtime.tool_timeout for this one
	// tool. Use Go duration syntax: "30s", "5m", "30m", "1h". Empty = use the
	// global default. Useful for tools that legitimately block for minutes
	// (e.g. NotebookLM audio generation, large data exports).
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// ContextHook is a lifecycle callback inserted before/after context assembly.
type ContextHook struct {
	Event      string `yaml:"event"       json:"event"`
	PythonFile string `yaml:"python_file" json:"python_file"`
	Function   string `yaml:"function"    json:"function"`
}

// Schedule configures cron/one-shot triggers.
type Schedule struct {
	Cron                string          `yaml:"cron,omitempty"                  json:"cron,omitempty"`
	At                  time.Time       `yaml:"at,omitempty"                    json:"at,omitempty"`
	Timeout             string          `yaml:"timeout,omitempty"               json:"timeout,omitempty"`
	RunMissedOnStartup  bool            `yaml:"run_missed_on_startup,omitempty" json:"run_missed_on_startup,omitempty"`
	MissedStartupWindow string          `yaml:"missed_startup_window,omitempty" json:"missed_startup_window,omitempty"`
	Output              *ScheduleOutput `yaml:"output,omitempty"                json:"output,omitempty"`
}

// ScheduleOutput configures where successful scheduled runs should be sent.
type ScheduleOutput struct {
	Channel  string `yaml:"channel,omitempty"  json:"channel,omitempty"`  // channel adapter ID, e.g. telegram or telegram-financial-agent
	To       string `yaml:"to,omitempty"       json:"to,omitempty"`       // destination thread/chat/channel/user ID for the adapter
	BotName  string `yaml:"bot_name,omitempty" json:"bot_name,omitempty"` // display snapshot from channel bot mapping
	Template string `yaml:"template,omitempty" json:"template,omitempty"` // optional text template; {reply} inserts the agent reply
}

// Definition is the parsed representation of a SOUL.yaml file.
// This is the single source of truth for an agent's behaviour.
type Definition struct {
	// --- Identity ---
	ID          string            `yaml:"id"                json:"id"`
	Name        string            `yaml:"name"              json:"name"`
	Description string            `yaml:"description"       json:"description"`
	Version     string            `yaml:"version"           json:"version"`
	Tags        []string          `yaml:"tags,omitempty"    json:"tags,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"  json:"labels,omitempty"`

	// --- Trigger ---
	Trigger  TriggerKind `yaml:"trigger"             json:"trigger"`
	Channels []string    `yaml:"channels,omitempty"  json:"channels,omitempty"`
	Schedule *Schedule   `yaml:"schedule,omitempty"  json:"schedule,omitempty"`

	// --- Intelligence ---
	SystemPrompt string    `yaml:"system_prompt" json:"system_prompt"`
	LLM          LLMConfig `yaml:"llm"           json:"llm"`

	// --- Tools ---
	Tools []ToolDef `yaml:"tools,omitempty" json:"tools,omitempty"`

	// --- Skills (opt-in) ---
	// Names of Agent Skills this agent may use. Empty = skills disabled (the
	// skill catalog and read_skill tools are NOT injected, so simple agents
	// don't waste turns on spurious skill lookups). Use ["*"] (or ["all"]) to
	// enable all installed skills, or list specific skill names.
	Skills []string `yaml:"skills,omitempty" json:"skills,omitempty"`

	// --- Knowledge bases (opt-in) ---
	// Names of knowledge bases this agent may search via the built-in
	// `kb_search` tool. Empty = no KB catalog is injected and kb_search is
	// NOT offered, so simple agents stay focused. Each entry must match a
	// KB.Name in the knowledge store; entries that don't resolve at load time
	// are logged but tolerated (the KB may be created later).
	Knowledge []string `yaml:"knowledge,omitempty" json:"knowledge,omitempty"`

	// --- Peer agents (opt-in, multi-agent / agent-as-tool) ---
	// IDs of OTHER agents this agent may invoke as callable tools. The engine
	// dynamically registers one tool per peer named `agent__<id>` whose
	// description is pulled from the target agent's `description` field.
	// When invoked, the peer runs as a fresh session (no shared history with
	// the caller) up to its own max_turns and returns its final reply.
	//
	// Use ["*"] (or ["all"]) to expose every other loaded agent as a tool.
	// Self-references (an agent in its own peer list) are silently skipped.
	// Cycles are bounded by a depth limit (5) carried via context.Value;
	// exceeding it returns an error tool result so the parent can recover.
	Agents []string `yaml:"agents,omitempty" json:"agents,omitempty"`

	// --- Built-in tool allowlist (opt-in) ---
	// Controls which Go-native built-ins (web_search, kb_search, read_skill, …)
	// are offered to this agent. Three modes:
	//
	//   nil / field absent → default gating applies: built-ins whose Gate
	//                        condition passes are auto-injected. This is the
	//                        backward-compatible behaviour.
	//   []  (empty list)   → NO built-ins are offered. Useful for orchestrator
	//                        agents that should only call their peers — without
	//                        this, an orchestrator with `agents: [web-researcher]`
	//                        ALSO sees the raw `web_search` built-in and may
	//                        bypass the peer-agent abstraction.
	//   [name, …]          → ONLY the named built-ins are offered (still
	//                        subject to their gate — e.g. listing kb_search
	//                        without declaring any knowledge bases is a no-op).
	//                        Use ["*"] or ["all"] as a synonym for the nil mode.
	//
	// IMPORTANT: this field is encoded with `omitempty,!nil` semantics — i.e.
	// a present empty list means "no built-ins" and SERIALIZES as `builtins: []`
	// in YAML so a GUI round-trip preserves the user's intent.
	Builtins *[]string `yaml:"builtins,omitempty" json:"builtins,omitempty"`

	// --- MCP tool allowlists (opt-in restriction) ---
	// MCPServers limits which connected MCP servers this agent can see and call.
	// Nil / absent preserves legacy behavior: all connected MCP servers are
	// available. A present empty list means no MCP servers are available. Use
	// ["*"] or ["all"] to explicitly allow every connected MCP server.
	MCPServers *[]string `yaml:"mcp_servers,omitempty" json:"mcp_servers,omitempty"`

	// MCPTools limits individual MCP tools by full namespaced tool name, e.g.
	// "mcp__rocketmoney__get_transactions". It may be combined with
	// MCPServers; a tool is allowed when either allowlist admits it. Nil means
	// no per-tool restriction unless MCPServers is also set.
	MCPTools *[]string `yaml:"mcp_tools,omitempty" json:"mcp_tools,omitempty"`

	// SystemTools, when true, opts this agent into the OS-level built-in tool set
	// (shell_exec, run_script, install_library, read_file, write_file, list_dir).
	// ALSO requires runtime.allow_system_tools: true in config.yaml — both must
	// be set. This double opt-in prevents accidental exposure of system access.
	SystemTools bool `yaml:"system_tools,omitempty" json:"system_tools,omitempty"`

	// ConfirmTools is the list of built-in tool names that require explicit user
	// approval before execution. The engine pauses, emits a "tool_confirm" SSE
	// event, and waits for a POST to /api/v1/chat/confirm before proceeding.
	// Use ["*"] to require confirmation for every built-in tool call.
	// Particularly useful for destructive tools: shell_exec, write_file, http_request.
	ConfirmTools []string `yaml:"confirm_tools,omitempty" json:"confirm_tools,omitempty"`

	// --- Memory ---
	Memory MemoryPolicy `yaml:"memory" json:"memory"`

	// --- Reasoning loop (CFG-01) ---
	// Controls the multi-step reasoning strategy. When absent the agent uses
	// the classic single-LLM-call behaviour (no loop). Set strategy to "react"
	// or "plan_execute" to enable the reasoning loop for this agent.
	Reasoning ReasoningConfig `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`

	// --- Long-term agent memory (CFG-02) ---
	// Controls episodic, semantic, and procedural memory injection and
	// persistence for this agent. Requires a CompositeStore to be wired into
	// the engine at startup (MEM-02).
	BrainMemory BrainMemoryConfig `yaml:"brain_memory,omitempty" json:"brain_memory,omitempty"`

	// --- Hooks ---
	Hooks []ContextHook `yaml:"hooks,omitempty" json:"hooks,omitempty"`

	// --- Runtime ---
	MaxTurns    int  `yaml:"max_turns"    json:"max_turns"`
	StreamReply bool `yaml:"stream_reply" json:"stream_reply"`
	Enabled     bool `yaml:"enabled"      json:"enabled"`

	// NotifyOnFailure tells the engine where to post a heads-up when a run
	// errors. Useful for cron-driven agents whose only audience is the
	// scheduler — without this, failures only ever land in the actionlog
	// where nothing alerts a human until the operator notices stale output.
	//
	// Default behavior (field absent): runs triggered by an external
	// channel (telegram/slack/discord/whatsapp) get an automatic error
	// reply on the SAME channel back to the originating user, because the
	// engine has both the channel id and the thread id from the inbound
	// message. Runs triggered by cron or by manual HTTP have no implicit
	// reply target — for those you must set NotifyOnFailure explicitly or
	// the failure is logged silently.
	NotifyOnFailure *NotifyOnFailure `yaml:"notify_on_failure,omitempty" json:"notify_on_failure,omitempty"`

	// Security configures access control for this agent independent of the LLM.
	// When Passphrase is non-empty the engine enforces it in Go before the LLM
	// ever sees the message — no model instruction can bypass this gate.
	Security *SecurityConfig `yaml:"security,omitempty" json:"security,omitempty"`

	// Workflow, when set, declares a multi-step DAG for this agent. The runtime
	// executes steps sequentially, checkpointing state after each step, and can
	// resume on restart. Declared under the `workflow:` key in SOUL.yaml.
	// When Workflow is non-nil, Handle() delegates to WorkflowExecutor instead
	// of the free-form LLM loop.
	Workflow *WorkflowSpec `yaml:"workflow,omitempty" json:"workflow,omitempty"`

	// RunTimeout caps the total wall-clock duration of one full agent run
	// (across all LLM turns and tool calls). Go duration syntax: "5m", "30m",
	// "1h". Empty = use the gateway default (15m). Bump this for agents that
	// call long-running tools — e.g. NotebookLM audio generation can take 10+
	// minutes by itself.
	RunTimeout string `yaml:"run_timeout,omitempty" json:"run_timeout,omitempty"`

	// Populated at load time — excluded from API responses and YAML serialisation.
	SourcePath string    `yaml:"-" json:"-"`
	LoadedAt   time.Time `yaml:"-" json:"-"`
}

// NotifyOnFailure is the SOUL.yaml block that configures where a run's
// failure is reported. All fields are optional except Channel + To.
type NotifyOnFailure struct {
	// Channel is the adapter ID (e.g. "telegram", "slack", "discord",
	// "whatsapp", "http"). Must match a channel that is registered and
	// enabled at runtime, otherwise the notification is dropped with a
	// warn log — the original failure is still recorded in the actionlog.
	Channel string `yaml:"channel" json:"channel"`

	// To is the recipient on that channel. Format is adapter-specific:
	//   telegram → chat_id as a numeric string ("8546291328")
	//   slack    → channel id ("C0123ABCD") or user id
	//   discord  → channel id
	//   whatsapp → phone number (E.164)
	//   http     → user_id of the receiving handle
	To string `yaml:"to" json:"to"`

	// IncludeError, when true, appends the engine's error string to the
	// notification body. Defaults to true via the engine's handling code
	// (we generally want the operator to know WHY a job failed).
	IncludeError bool `yaml:"include_error,omitempty" json:"include_error,omitempty"`

	// Template overrides the default notification body. Recognises these
	// substitutions, applied via simple string replace:
	//   {agent_id}   {agent_name}   {timestamp}   {error}   {stage}
	// Default (when Template is empty):
	//   "🚨 Soulacy agent {agent_id} failed at {timestamp}: {error}"
	Template string `yaml:"template,omitempty" json:"template,omitempty"`
}

// Clone returns a deep copy of the Definition. Slice and map fields are
// copied so a hot-reload (which replaces the in-memory pointer) cannot
// mutate the copy held by an in-flight engine.Handle() call.
//
// For fields whose values are read-only after unmarshal (e.g. nested map
// values inside Parameters/OutputSchema), a shallow clone of the map is
// sufficient — only the map header needs to be independent so range/assign
// on one copy doesn't affect the other.
func (d *Definition) Clone() *Definition {
	if d == nil {
		return nil
	}
	cp := *d // copy scalar fields

	// Slices — each gets its own backing array.
	cp.Tags = cloneStrSlice(d.Tags)
	cp.Channels = cloneStrSlice(d.Channels)
	cp.Skills = cloneStrSlice(d.Skills)
	cp.Knowledge = cloneStrSlice(d.Knowledge)
	cp.Agents = cloneStrSlice(d.Agents)
	cp.ConfirmTools = cloneStrSlice(d.ConfirmTools)

	// Memory slices.
	cp.Memory = MemoryPolicy{
		MaxTokens:   d.Memory.MaxTokens,
		ReadScopes:  cloneStrSlice(d.Memory.ReadScopes),
		WriteScopes: cloneStrSlice(d.Memory.WriteScopes),
	}

	// LLM — clone slice/map sub-fields.
	cp.LLM = d.LLM
	cp.LLM.AllowedProviders = cloneStrSlice(d.LLM.AllowedProviders)
	if d.LLM.OutputSchema != nil {
		cp.LLM.OutputSchema = cloneMapAny(d.LLM.OutputSchema)
	}

	// Labels map.
	if d.Labels != nil {
		cp.Labels = make(map[string]string, len(d.Labels))
		for k, v := range d.Labels {
			cp.Labels[k] = v
		}
	}

	// Builtins — pointer to a slice.
	if d.Builtins != nil {
		cloned := cloneStrSlice(*d.Builtins)
		cp.Builtins = &cloned
	}
	if d.MCPServers != nil {
		cloned := cloneStrSlice(*d.MCPServers)
		cp.MCPServers = &cloned
	}
	if d.MCPTools != nil {
		cloned := cloneStrSlice(*d.MCPTools)
		cp.MCPTools = &cloned
	}

	// Tools — each ToolDef's Parameters map gets its own header copy.
	if len(d.Tools) > 0 {
		cp.Tools = make([]ToolDef, len(d.Tools))
		for i, t := range d.Tools {
			cp.Tools[i] = t
			if t.Parameters != nil {
				cp.Tools[i].Parameters = cloneMapAny(t.Parameters)
			}
		}
	}

	// Hooks slice.
	if len(d.Hooks) > 0 {
		cp.Hooks = make([]ContextHook, len(d.Hooks))
		copy(cp.Hooks, d.Hooks)
	}

	// NotifyOnFailure — shallow pointer copy; struct has no sub-slices.
	if d.NotifyOnFailure != nil {
		nof := *d.NotifyOnFailure
		cp.NotifyOnFailure = &nof
	}

	// Workflow — shallow pointer copy. Steps/Nodes/Edges elements are
	// read-only after unmarshal so element-shallow copies are safe.
	if d.Workflow != nil {
		wf := *d.Workflow
		wf.Steps = append([]StepSpec(nil), d.Workflow.Steps...)
		wf.Nodes = append([]reasoning.FlowNode(nil), d.Workflow.Nodes...)
		wf.Edges = append([]reasoning.FlowEdge(nil), d.Workflow.Edges...)
		cp.Workflow = &wf
	}

	// Schedule.
	if d.Schedule != nil {
		sched := *d.Schedule
		if d.Schedule.Output != nil {
			out := *d.Schedule.Output
			sched.Output = &out
		}
		cp.Schedule = &sched
	}

	return &cp
}

func cloneStrSlice(s []string) []string {
	if s == nil {
		return nil
	}
	c := make([]string, len(s))
	copy(c, s)
	return c
}

// cloneMapAny produces a one-level-deep map copy (keys + value pointers
// are independent; nested map/slice values share storage). This is
// sufficient because the engine only READS nested schema data — it never
// mutates it in place.
func cloneMapAny(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// ResolvedRunTimeout returns the agent's declared RunTimeout (parsed as a Go
// duration), falling back to the supplied default when unset or invalid. Used
// by the gateway and scheduler so per-agent caps apply consistently across
// manual triggers, HTTP chat, and cron-driven runs.
func (d *Definition) ResolvedRunTimeout(fallback time.Duration) time.Duration {
	if d == nil || d.RunTimeout == "" {
		return fallback
	}
	t, err := time.ParseDuration(d.RunTimeout)
	if err != nil || t <= 0 {
		return fallback
	}
	return t
}

// Status is the live operational state of a loaded agent.
type Status struct {
	AgentID        string     `json:"agent_id"`
	Enabled        bool       `json:"enabled"`
	ActiveSessions int        `json:"active_sessions"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	TotalRuns      int64      `json:"total_runs"`
}
