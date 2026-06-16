// engine.go — the agent execution loop.
// The Engine is the heart of Soulacy. It receives a message, assembles the
// full context (system prompt + memory + history + tools), fires the LLM, and
// if the LLM requests tool calls, executes them in a sandboxed Python subprocess
// before re-entering the loop. This continues until the LLM produces a plain
// text response or the max_turns limit is hit.
package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/audit"
	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/skill"
)

// EventSink receives structured events as they happen during agent execution.
// The gateway WebSocket handler implements this to stream events to the GUI.
type EventSink interface {
	Emit(event message.Event)
}

// noopSink discards all events (used when no GUI is connected).
type noopSink struct{}

func (noopSink) Emit(_ message.Event) {}

// SkillLoader is satisfied by *skills.Loader. Defined as an interface here to
// avoid an import cycle (skills → runtime would be circular).
type SkillLoader interface {
	BuildCatalog() string
	Get(name string) *skill.Skill
	All() []*skill.Skill
}

// BuiltinTool is a Go-native tool that runs inside the engine process rather
// than delegating to a Python subprocess. Built-ins are added alongside the
// agent's Python tool definitions when building the LLM tool schema.
type BuiltinTool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     func(ctx context.Context, args map[string]any) (string, error)
	// Gate controls when the tool is offered to the LLM:
	//   "skills" — only when the agent has opted into skills (def.Skills)
	//   "ollama" — only when the agent's LLM provider is Ollama
	//   ""       — always
	Gate string
}

// Engine orchestrates agent execution.
type Engine struct {
	loader      *Loader
	llmRouter   *llm.Router
	memory      memory.Store
	archive     storage.MemoryBackend
	pythonBin   string
	toolTimeout time.Duration
	log         *zap.Logger
	sink        EventSink
	sessions    sync.Map // sessionID → *Session

	// Skills support
	skillLoader SkillLoader
	builtins    []BuiltinTool

	// ollamaAPIKey is used by the built-in web_search tool (Ollama Web Search API).
	// Falls back to the OLLAMA_API_KEY env var at call time.
	ollamaAPIKeyMu sync.RWMutex
	ollamaAPIKey   string

	// Web search provider and API key
	searchProviderMu sync.RWMutex
	searchProvider   string
	searchAPIKey     string


	// mcpClient routes MCP tool calls to configured external MCP servers.
	// All tools from connected servers are offered to every agent, namespaced
	// as mcp__<server>__<tool>. May be nil if no servers are configured.
	mcpClient *mcp.Client

	// knowledge is the RAG facade — used by the kb_search built-in tool and
	// by buildContext to inject the per-agent KB catalog. nil = RAG disabled.
	knowledge *knowledge.Service

	// Conversational agent builder sessions (sessionID → *builderSession)
	builderSessions sync.Map

	// PRODUCTION_AUDIT → F1 (2026-05-27): when both fields are set,
	// executePythonTool wraps every command in a re-exec of the
	// soulacy binary (selfPath) under __exec-sandbox, applying the
	// rlimits in sandboxLimits before execve'ing python. Zero values
	// disable wrapping (engine falls through to the legacy direct exec).
	selfPath      string
	sandboxLimits sandbox.Limits

	// FailureNotifier is wired from main.go to route run failures to a
	// configured channel (see agent.NotifyOnFailure) and/or back to the
	// originating channel. nil = silent (legacy behavior) — failures only
	// land in the actionlog. Set via SetFailureNotifier.
	failureNotifier FailureNotifier

	// allowSystemAgents is the list of agent IDs allowed to access the OS-level
	// built-in tools (shell_exec, run_script, install_library, write_file,
	// download_file). Set from config.Runtime.AllowSystemAgents.
	// An agent must be listed here AND declare the "system" capability.
	allowSystemAgents []string

	// vectorStore, when non-nil, backs the semantic_memory_search built-in.
	// Powered by sqlite-vec in the same archive DB as the long-term memory.
	vectorStore *memory.VectorStore

	// brainStore, when non-nil, enables three-layer long-term memory
	// (episodic / semantic / procedural) for agents that declare brain_memory
	// in their SOUL.yaml. Set via SetBrainMemory after construction.
	brainStore *agentmemory.CompositeStore

	// pluginProvider, when non-nil, provides plugin-contributed tools.
	// Satisfied by *plugins.Loader via an adapter in main.go.
	pluginProvider PluginToolProvider

	// broker handles pending tool-confirmation requests from the UI.
	// Allocated once in NewEngine; the gateway calls Broker() to resolve decisions.
	broker *ConfirmBroker

	// auditLog records every built-in tool call to an append-only JSONL file.
	// nil = audit logging disabled.
	auditLog *audit.Logger

	// ssrfProtection mirrors config.Runtime.SSRFProtection.
	ssrfProtection bool
	// allowPrivateHosts mirrors config.Runtime.AllowPrivateHosts.
	allowPrivateHosts []string
	// allowedToolDirs mirrors config.Runtime.AllowedToolDirs. When non-empty,
	// any python_file path that does not resolve under one of these prefixes is
	// rejected before the subprocess is forked. Empty = all paths permitted.
	allowedToolDirs []string

	// pyExecutor is the optional pre-forked Python worker pool. When nil,
	// the engine falls back to the original exec-per-call subprocess path.
	// Set via SetExecutor after construction.
	pyExecutor executor.Backend

	// resources is the optional session resource store used for typed media
	// attachments (E1 — Typed Media Attachments).  nil by default; set via
	// SetResourceStore before traffic starts.
	resources *session.ResourceStore

	// checkpoints persists workflow step state across restarts (E5 — Structured
	// Workflow Scaffolding). nil = checkpoint persistence disabled (workflow
	// steps still run but cannot resume after a crash). Set via
	// SetCheckpointStore before traffic starts.
	checkpoints *CheckpointStore

	// tracer is the optional telemetry tracer (Task #32). When nil, no spans
	// are emitted. Set via SetTracer after construction.
	tracer telemetryTracer

	// costStore is the optional per-agent token-cost store (Task #32). When
	// nil, usage is not persisted. Set via SetCostStore after construction.
	costStore agentCostStore

	// historyStore persists every user+assistant turn to a durable conversation
	// log (session history). nil = no persistence (legacy behaviour).
	// Set via SetHistoryStore after construction.
	historyStore session.HistoryStore

	// dlqStore pushes failed Handle() calls to a dead-letter queue so operators
	// can inspect, retry, or purge them via the admin API. nil = no DLQ.
	// Set via SetDLQStore after construction.
	dlqStore deadLetterStore

	// reasoningKeys carries cloud-provider API keys for reasoning loop
	// backends (Story 16). Set via SetReasoningKeys at boot.
	reasoningKeys reasoning.ProviderKeys
	// reasoningBackendFactory, when non-nil, overrides how the reasoning
	// LLM backend is built for an agent (tests / embedders). nil = derive
	// from def.LLM.Provider via reasoning.DefaultBackendFor.
	reasoningBackendFactory func(*agent.Definition) reasoning.LLMBackend

	// ── PERF-1: session eviction ────────────────────────────────────────────
	// sessionTTL bounds how long an idle session is retained before the sweep
	// reclaims it. maxSessions caps the live session count. Both are set via
	// SetSessionEviction; zero values fall back to the documented defaults
	// (24h TTL, 10000 sessions). evictStop stops the sweeper goroutine.
	sessionTTL  time.Duration
	maxSessions int
	evictStop   chan struct{}
	evictOnce   sync.Once

	// ── PERF-2: history windowing ───────────────────────────────────────────
	// maxHistoryTurns caps the number of NON-system messages retained in a
	// session's in-memory History. Older turns are trimmed oldest-first; a
	// leading system message (index 0) is always preserved. Set via
	// SetMaxHistoryTurns; <=0 falls back to defaultMaxHistoryTurns.
	maxHistoryTurns int
}

const (
	defaultSessionTTL      = 24 * time.Hour
	defaultMaxSessions     = 10000
	defaultMaxHistoryTurns = 100
)

// PluginToolProvider is satisfied by *plugins.Loader. Defined locally to
// avoid an import cycle (plugins → engine would be circular).
type PluginToolProvider interface {
	AllTools() []PluginTool
}

// PluginTool is a callable tool contributed by a Soulacy plugin.
type PluginTool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     string // "python:<path>::<function>"
}

// streamCallbackKey is the context key for the per-request token callback.
type streamCallbackKey struct{}

// sessionIDKey carries the current session ID so nested helpers (confirm,
// audit) can tag their records without threading it through every signature.
type inboundMsgKey struct{}

// WithStreamCallback returns a context that delivers streaming tokens to cb.
// The engine's Handle() checks for this callback and sets Stream: true on the
// CompletionRequest when it's present (and the agent has stream_reply: true).
func WithStreamCallback(ctx context.Context, cb func(string)) context.Context {
	return context.WithValue(ctx, streamCallbackKey{}, cb)
}

// streamCallback extracts the token callback from ctx, or returns nil.
func streamCallback(ctx context.Context) func(string) {
	if cb, ok := ctx.Value(streamCallbackKey{}).(func(string)); ok {
		return cb
	}
	return nil
}

// FailureNotifier is invoked by the engine after a run errors. The engine
// passes the agent definition (so the notifier can read def.NotifyOnFailure
// + def.Name etc.), the original inbound message (so the notifier can
// reply on the same channel when no explicit NotifyOnFailure is set), and
// the rendered error string the LLM/operator should see.
//
// Notifiers must not block — the engine calls this synchronously from the
// run's deferred outcome handler. Implementations should use chanReg.Send
// (which already non-blocks) plus a short timeout for any network work.
type FailureNotifier interface {
	NotifyFailure(ctx context.Context, def *agent.Definition, inbound message.Message, errMsg string)
}

// SetFailureNotifier wires the run-failure callback. Safe to call zero or
// one time before traffic starts.
func (e *Engine) SetFailureNotifier(fn FailureNotifier) {
	e.failureNotifier = fn
}

// SetSandbox installs Python-tool sandboxing. selfPath should be the
// absolute path to the running soulacy binary (os.Executable()). Limits
// should come from cfg.Runtime.Sandbox via sandbox.Limits{Enabled:…, …}.
// Safe to call zero or one time; a second call replaces the previous
// settings. Goroutine-safe to call before tools start running.
func (e *Engine) SetSandbox(selfPath string, limits sandbox.Limits) {
	e.selfPath = selfPath
	e.sandboxLimits = limits
}

// Session tracks per-conversation state.
type Session struct {
	ID        string
	AgentID   string
	History   []llm.ChatMessage
	CreatedAt time.Time
	mu        sync.Mutex

	// cachedPrefix is the rendered system_prompt+catalogs for this session.
	// Primed by Handle() once per inbound user message and reused across
	// every turn of the agent loop. Cleared between Handle() calls so a
	// hot-reloaded def picks up its new catalogs on the next inbound
	// message (vs. the next process restart).
	cachedPrefix string

	// PassphraseVerified tracks whether the user has provided the correct
	// passphrase for agents that have security.passphrase set. Enforced in
	// Go before the LLM is invoked — cannot be bypassed by prompt injection.
	PassphraseVerified bool

	// lastAccess is the wall-clock time of the most recent inbound message
	// for this session. The eviction sweep (PERF-1) compares this against
	// the configured TTL to decide whether an idle session can be reclaimed.
	// Guarded by mu.
	lastAccess time.Time

	// inUse counts the number of in-flight Handle() calls touching this
	// session. The eviction sweep NEVER drops a session with inUse > 0 — a
	// session that is mid-conversation is always retained. Guarded by mu.
	inUse int
}

// NewEngine creates a new agent execution engine.
// skillLoader, knowledgeSvc, vectorStore, and pluginProvider may be nil — if
// so, those capabilities are silently disabled.
func NewEngine(
	loader *Loader,
	router *llm.Router,
	mem memory.Store,
	archive storage.MemoryBackend,
	pythonBin string,
	toolTimeout time.Duration,
	log *zap.Logger,
	sink EventSink,
	skillLoader SkillLoader,
	ollamaAPIKey string,
	mcpClient *mcp.Client,
	knowledgeSvc *knowledge.Service,
	allowSystemAgents []string,
	vectorStore *memory.VectorStore,
	pluginProvider PluginToolProvider,
) *Engine {
	if sink == nil {
		sink = noopSink{}
	}
	e := &Engine{
		loader:            loader,
		llmRouter:         router,
		memory:            mem,
		archive:           archive,
		pythonBin:         pythonBin,
		toolTimeout:       toolTimeout,
		log:               log,
		sink:              sink,
		skillLoader:       skillLoader,
		ollamaAPIKey:      ollamaAPIKey,
		mcpClient:         mcpClient,
		knowledge:         knowledgeSvc,
		allowSystemAgents: allowSystemAgents,
		vectorStore:       vectorStore,
		pluginProvider:    pluginProvider,
	}
	e.broker = newConfirmBroker()
	e.builtins = e.buildBuiltins()
	return e
}

// Broker returns the ConfirmBroker so the gateway can resolve pending
// tool-confirmation requests when the user approves or denies via the API.
func (e *Engine) Broker() *ConfirmBroker { return e.broker }

// SetAuditLog installs an audit logger. Safe to call before traffic starts.
func (e *Engine) SetAuditLog(l *audit.Logger) { e.auditLog = l }

// SetOllamaAPIKey refreshes the hosted Ollama API key used by web_search.
func (e *Engine) SetOllamaAPIKey(key string) {
	e.ollamaAPIKeyMu.Lock()
	defer e.ollamaAPIKeyMu.Unlock()
	e.ollamaAPIKey = strings.TrimSpace(key)
}

func (e *Engine) getOllamaAPIKey() string {
	e.ollamaAPIKeyMu.RLock()
	defer e.ollamaAPIKeyMu.RUnlock()
	return e.ollamaAPIKey
}

// SetSearchConfig configures the search provider and key for the built-in web_search tool.
func (e *Engine) SetSearchConfig(provider, apiKey string) {
	e.searchProviderMu.Lock()
	defer e.searchProviderMu.Unlock()
	e.searchProvider = strings.TrimSpace(provider)
	e.searchAPIKey = strings.TrimSpace(apiKey)
}

func (e *Engine) getSearchConfig() (string, string) {
	e.searchProviderMu.RLock()
	defer e.searchProviderMu.RUnlock()
	return e.searchProvider, e.searchAPIKey
}


// SetSSRF configures SSRF protection for the HTTP-fetching built-in tools.
func (e *Engine) SetSSRF(enabled bool, allowedHosts []string) {
	e.ssrfProtection = enabled
	e.allowPrivateHosts = allowedHosts
}

// SetAllowedToolDirs installs the python_file path allowlist. When dirs is
// non-empty, executePythonTool rejects any python_file that does not resolve
// under one of the listed directory prefixes.
func (e *Engine) SetAllowedToolDirs(dirs []string) {
	e.allowedToolDirs = dirs
}

// SetBrainMemory wires the three-layer agent memory store (MEM-03).
// When non-nil, agents with brain_memory.episodic.enabled=true will have their
// task history injected before each run (RL-10) and persisted after (RL-09).
func (e *Engine) SetBrainMemory(store *agentmemory.CompositeStore) {
	e.brainStore = store
}

// BrainStore returns the CompositeStore, or nil if not configured.
func (e *Engine) BrainStore() *agentmemory.CompositeStore { return e.brainStore }

// SetExecutor installs an executor.Backend for Python tool dispatch.
// When set to a pool backend, Python tool cold-start latency is eliminated by
// reusing pre-forked interpreter processes. When nil (or never called), the
// engine uses the original per-call subprocess path.
// Safe to call once at startup before any traffic.
func (e *Engine) SetExecutor(ex executor.Backend) { e.pyExecutor = ex }

// progressPublisher is satisfied by *gateway.EventHub (and any test double).
// Defined locally to avoid importing gateway from runtime.
type progressPublisher interface {
	PublishProgress(ev message.ProgressEvent)
}

// progressExecutor is the subset of executor.Backend that supports an
// OnProgress callback. Implemented by both process.Executor and pool.Pool.
type progressExecutor interface {
	SetOnProgress(fn func(message.ProgressEvent))
}

// SetProgressHub wires progress events from the active executor through to hub.
// When called, if the configured executor supports SetOnProgress, each parsed
// progress event from tool stdout will be forwarded to hub.PublishProgress.
// Safe to call after SetExecutor, before traffic starts.
func (e *Engine) SetProgressHub(hub progressPublisher) {
	pe, ok := e.pyExecutor.(progressExecutor)
	if !ok || hub == nil {
		return
	}
	pe.SetOnProgress(func(ev message.ProgressEvent) {
		hub.PublishProgress(ev)
	})
}

// SetResourceStore wires the session resource store used for typed media
// attachments (E1 — Typed Media Attachments).  Safe to call once at startup
// before any traffic.
func (e *Engine) SetResourceStore(s session.ResourceStore) { e.resources = &s }

// SetCheckpointStore wires the workflow checkpoint store (E5 — Structured
// Workflow Scaffolding). Safe to call once at startup before any traffic.
func (e *Engine) SetCheckpointStore(s *CheckpointStore) { e.checkpoints = s }

// ---------------------------------------------------------------------------
// Task #32 — telemetry + cost tracking interfaces and setters
// ---------------------------------------------------------------------------

// telemetryTracer is a minimal tracing interface satisfied by any tracer
// whose Start method takes a context, a span name, and optional string
// key/value pairs (as flat ...string variadics: key, value, key, value …).
// Defined locally so the runtime package does not import the telemetry
// package and create a cycle.
type telemetryTracer interface {
	// Start begins a span named spanName. kv is an optional flat list of
	// string key/value attribute pairs (key0, val0, key1, val1, …).
	// Returns the child context and a span whose End() must be deferred.
	Start(ctx context.Context, spanName string, kv ...string) (context.Context, interface{ End() })
}

// agentCostStore is a minimal interface satisfied by *costs.Store via
// engineCostStoreAdapter (defined in main.go). Defined locally so the runtime
// package does not import the costs package.
type agentCostStore interface {
	Record(ctx context.Context, agentID, sessionID, provider, model string,
		promptTokens, compTokens, totalTokens int, costUSD float64) error
}

// deadLetterStore is a minimal interface satisfied by *dlq.SQLiteStore via
// engineDLQAdapter (defined in main.go). Defined locally to avoid importing
// the dlq package and creating a cycle.
type deadLetterStore interface {
	// PushFailed records a failed message delivery in the dead-letter queue.
	PushFailed(ctx context.Context, queue string, payload []byte, errMsg string) error
}

// SetTracer installs a telemetry tracer. Safe to call once at startup.
func (e *Engine) SetTracer(t telemetryTracer) { e.tracer = t }

// SetCostStore installs a cost store. Safe to call once at startup.
func (e *Engine) SetCostStore(s agentCostStore) { e.costStore = s }

// SetHistoryStore installs a conversation history store. Safe to call once at startup.
func (e *Engine) SetHistoryStore(s session.HistoryStore) { e.historyStore = s }

// SeedSessionHistory initialises the in-memory history of (agentID,
// sessionID) from persisted conversation entries — used when forking a chat
// session (Story 8) so the branch's copied turns become real LLM context on
// the next Handle. A no-op when the in-memory session already has history:
// live conversations are never clobbered.
func (e *Engine) SeedSessionHistory(agentID, sessionID string, entries []session.ConversationEntry) {
	if len(entries) == 0 {
		return
	}
	sess := e.getOrCreateSession(sessionID, agentID)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.History) > 0 {
		return
	}
	history := make([]llm.ChatMessage, 0, len(entries))
	for _, en := range entries {
		role := en.Role
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		history = append(history, llm.ChatMessage{Role: role, Content: en.Content})
	}
	sess.History = history
}

// SetDLQStore installs a dead-letter queue store. Safe to call once at startup.
func (e *Engine) SetDLQStore(s deadLetterStore) { e.dlqStore = s }

// recordUsage writes a token-usage record to the cost store, if one is wired.
// Silently no-ops when costStore is nil or any argument is zero.
func (e *Engine) recordUsage(ctx context.Context, agentID, sessionID, provider, model string, promptTokens, compTokens int) {
	if e.costStore == nil || (promptTokens == 0 && compTokens == 0) {
		return
	}
	total := promptTokens + compTokens
	if err := e.costStore.Record(ctx, agentID, sessionID, provider, model,
		promptTokens, compTokens, total, 0); err != nil {
		e.log.Warn("cost store record failed", zap.Error(err))
	}
}

// RunTool invokes a named tool with raw JSON arguments and returns raw JSON output.
// Used by WorkflowExecutor to call individual tools without a full LLM round-trip.
func (e *Engine) RunTool(ctx context.Context, toolName string, argsJSON string) (json.RawMessage, error) {
	// Look up the tool by name in the engine's built-in registry.
	for _, bt := range e.builtins {
		if bt.Name == toolName {
			// Decode argsJSON into map[string]any for the handler.
			args := map[string]any{}
			if argsJSON != "" {
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return nil, fmt.Errorf("RunTool: decode args for %q: %w", toolName, err)
				}
			}
			result, err := bt.Handler(ctx, args)
			if err != nil {
				return nil, err
			}
			// Wrap plain string results as a JSON string so callers always
			// receive valid json.RawMessage.
			raw, merr := json.Marshal(result)
			if merr != nil {
				return nil, fmt.Errorf("RunTool: marshal result for %q: %w", toolName, merr)
			}
			return json.RawMessage(raw), nil
		}
	}
	return nil, fmt.Errorf("tool %q not found", toolName)
}

// RunInlinePython executes a Studio "Custom Python" node's inline code in the
// sandboxed Python executor (process-per-call; env filtered to the allowlist).
// argsJSON is delivered on stdin and passed to the node's run(inputs) function;
// the printed value is captured and returned as JSON (a non-JSON string is
// wrapped as a JSON string so downstream flow vars stay well-typed). Returns an
// error when no Python executor is configured.
//
// SECURITY: this runs LLM/user-authored code. The flow runner must only reach
// here for a python node whose per-case consent has been granted (see the
// consent model in internal/studio/plan.go and docs/STUDIO_PYTHON_TOOLS.md §13);
// RunInlinePython itself is the mechanism, not the gate.
func (e *Engine) RunInlinePython(ctx context.Context, code string, argsJSON []byte) (json.RawMessage, error) {
	if e.pyExecutor == nil {
		return nil, fmt.Errorf("RunInlinePython: no python executor configured")
	}
	if len(argsJSON) == 0 {
		argsJSON = []byte("{}")
	}
	out, err := e.pyExecutor.Run(ctx, "", "run", code, argsJSON)
	if err != nil {
		return nil, fmt.Errorf("RunInlinePython: %w", err)
	}
	trimmed := []byte(strings.TrimSpace(out))
	if json.Valid(trimmed) {
		return json.RawMessage(trimmed), nil
	}
	wrapped, merr := json.Marshal(out)
	if merr != nil {
		return nil, fmt.Errorf("RunInlinePython: encode output: %w", merr)
	}
	return json.RawMessage(wrapped), nil
}

// maybeConfirm checks whether call.Name is in def.ConfirmTools and, if so,
// emits a tool_confirm SSE event and blocks until the user approves or denies.
// Returns nil if confirmation is not required or if the tool is approved.
// Returns an error (wrapping "denied") if the user rejected the call.
func (e *Engine) maybeConfirm(ctx context.Context, def *agent.Definition, call message.ToolCall) error {
	if len(def.ConfirmTools) == 0 {
		return nil
	}
	required := false
	for _, t := range def.ConfirmTools {
		if t == "*" || t == "all" || t == call.Name {
			required = true
			break
		}
	}
	if !required {
		return nil
	}

	sender, ok := confirmSenderFrom(ctx)
	if !ok {
		// No confirm channel in this context (e.g. non-streaming call).
		// Proceed without confirmation rather than silently blocking forever.
		e.log.Warn("tool confirmation required but no confirm channel in context — proceeding",
			zap.String("tool", call.Name))
		return nil
	}

	callID := uuid.New().String()
	resultCh := sender(ConfirmRequest{
		CallID: callID,
		Tool:   call.Name,
		Args:   call.Arguments,
	})

	select {
	case approved := <-resultCh:
		if !approved {
			e.logAudit(ctx, def, call, "", time.Now(), true, nil)
			return fmt.Errorf("tool %q was denied by the user", call.Name)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) dynamicConfirm(ctx context.Context, def *agent.Definition, call message.ToolCall, reason string) error {
	sender, ok := confirmSenderFrom(ctx)
	if !ok {
		// No confirm channel in this context. Since the guardrail demanded confirmation but we can't get it, we must DENY for safety.
		e.log.Warn("guardrail required confirmation but no confirm channel in context — denying",
			zap.String("tool", call.Name))
		return fmt.Errorf("guardrail required confirmation, but no GUI available to confirm: %s", reason)
	}

	callID := uuid.New().String()
	resultCh := sender(ConfirmRequest{
		CallID: callID,
		Tool:   call.Name,
		Args:   call.Arguments,
		Reason: reason,
	})

	select {
	case approved := <-resultCh:
		if !approved {
			e.logAudit(ctx, def, call, "", time.Now(), true, nil)
			return fmt.Errorf("tool %q was denied by the user (guardrail flag: %s)", call.Name, reason)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// logAudit records a tool call to the audit logger (no-op when auditLog is nil).
func (e *Engine) logAudit(ctx context.Context, def *agent.Definition, call message.ToolCall, result string, start time.Time, denied bool, err error) {
	if e.auditLog == nil {
		return
	}
	sessionID := ""
	if msg, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok {
		sessionID = msg.SessionID
	}
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	e.auditLog.Log(audit.Entry{
		Timestamp:  start.UTC(),
		SessionID:  sessionID,
		AgentID:    def.ID,
		Tool:       call.Name,
		Args:       call.Arguments,
		ResultLen:  len(result),
		DurationMS: time.Since(start).Milliseconds(),
		Denied:     denied,
		Error:      errStr,
	})
}

// Knowledge returns the engine's RAG service (may be nil).
func (e *Engine) Knowledge() *knowledge.Service { return e.knowledge }

// Builtins returns a copy of the Go-native built-in tools (web_search,
// read_skill, …) plus the system tools (shell_exec, run_script, …).
// Used by the gateway's /tool-catalog endpoint to advertise what's available
// to the Builder and the Agents Edit UI.
// System tools are listed in the catalog regardless of channel; they are only
// actually offered at runtime when the three-way guard in allToolSchemas passes.
func (e *Engine) Builtins() []BuiltinTool {
	out := make([]BuiltinTool, len(e.builtins))
	copy(out, e.builtins)
	// SAFE (read-only) OS-level built-ins are always advertised so the GUI
	// Builder can display them — they are available to every http-channel
	// agent regardless of capability (SEC-3).
	out = append(out, e.safeSystemTools()...)
	// The privileged SYSTEM partition (shell_exec, run_script, …) is only
	// advertised when the server permits system tools at all. Whether a GIVEN
	// agent may actually call them additionally requires the "system"
	// capability — enforced per-agent in systemToolsFor at dispatch time.
	if len(e.allowSystemAgents) > 0 {
		all := e.buildSystemTools()
		for _, b := range all {
			if isPrivilegedSystemTool(b.Name) {
				out = append(out, b)
			}
		}
	}
	return out
}

// buildBuiltins constructs the set of Go-native built-in tools available to
// every agent. Currently includes:
//   - read_skill: load the full instructions for an Agent Skill by name
//   - read_skill_file: read a resource file (script/reference/asset) from a skill
func (e *Engine) buildBuiltins() []BuiltinTool {
	var tools []BuiltinTool

	// web_search — Ollama Web Search API. Available to ALL agents regardless of
	// which LLM provider they use: the search endpoint at ollama.com/api/web_search
	// only needs OLLAMA_API_KEY (env var or llm.providers.ollama.api_key). A
	// claude / openai / gemini agent can call this exactly like an Ollama agent
	// can — the model running inference and the search service are independent.
	// The handler returns a clear error if the key isn't configured, and agents
	// can opt out of all built-ins via `builtins: []` in SOUL.yaml.
	tools = append(tools, BuiltinTool{
		Name:        "web_search",
		Description: "Search the web for current, up-to-date information. Use for facts, news, prices, or anything beyond the model's training data. Returns a JSON object {\"query\":..., \"result_count\":N, \"results\":[{\"title\",\"url\",\"content\"},...]}. Supports Ollama, Tavily, and Serper backends.",
		Gate:        "",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results (default 5)",
				},
			},
			"required": []string{"query"},
		},
		Handler: e.webSearch,
	})

	// Skill built-ins are only added when a skill loader is configured;
	// other built-ins (kb_search, …) are appended below regardless.
	if e.skillLoader != nil {
		tools = e.appendSkillBuiltins(tools)
	}

	// kb_search — see buildKBSearchBuiltin below. Gated by `def.Knowledge`.
	if e.knowledge != nil {
		tools = append(tools, e.buildKBSearchBuiltin())
	}

	// semantic_memory_search — embedding-based long-term memory retrieval.
	// Only added when a VectorStore is configured.
	if e.vectorStore != nil {
		tools = append(tools, e.buildSemanticMemoryBuiltin())
	}

	// System tools are NOT pre-built into e.builtins — they are injected
	// per-request in allToolSchemas, gated by both def.SystemTools and the
	// inbound channel. See allToolSchemas for the enforcement logic.

	return tools
}

// appendSkillBuiltins adds the skills tier-2/tier-3 built-ins (read_skill,
// read_skill_file). Split out so buildBuiltins can layer other capabilities
// (knowledge, …) on top without an early return blocking them.
func (e *Engine) appendSkillBuiltins(tools []BuiltinTool) []BuiltinTool {
	// read_skill — tier 2 activation: load full SKILL.md body for a named skill
	tools = append(tools, BuiltinTool{
		Gate:        "skills",
		Name:        "read_skill",
		Description: "Load the full instructions for an Agent Skill by name. Call this when a task matches a skill description from the available_skills catalog.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill_name": map[string]any{
					"type":        "string",
					"description": "The skill name as listed in the available_skills catalog",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Legacy alias for skill_name",
				},
			},
			"required": []string{"skill_name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := argString(args, "skill_name")
			if name == "" {
				name = argString(args, "name")
			}
			if name == "" {
				return "", fmt.Errorf("read_skill: skill_name is required")
			}
			s := e.skillLoader.Get(name)
			if s == nil {
				return "", fmt.Errorf("read_skill: skill %q not found. Available skills: %s", name, e.skillNamesCSV())
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("<skill_content name=%q>\n", s.Name))
			sb.WriteString(s.Body)
			sb.WriteString(fmt.Sprintf("\n\nSkill directory: %s\n", s.Dir))
			// List bundled resources if any
			resources := s.ResourceFiles()
			if len(resources) > 0 {
				sb.WriteString("\n<skill_resources>\n")
				for _, r := range resources {
					sb.WriteString(fmt.Sprintf("  <file>%s</file>\n", r))
				}
				sb.WriteString("</skill_resources>\n")
			}
			sb.WriteString("</skill_content>")
			return sb.String(), nil
		},
	})

	// read_skill_file — tier 3 resource loading: read a specific file from a skill directory
	tools = append(tools, BuiltinTool{
		Gate:        "skills",
		Name:        "read_skill_file",
		Description: "Read a specific resource file (script, reference, or asset) from a skill directory. Use when skill instructions reference a file like scripts/extract.py or references/guide.md.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill_name": map[string]any{
					"type":        "string",
					"description": "The name of the skill that owns the file",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path from the skill directory (e.g. scripts/run.py)",
				},
			},
			"required": []string{"skill_name", "path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			skillName := argString(args, "skill_name")
			relPath := argString(args, "path")
			if skillName == "" || relPath == "" {
				return "", fmt.Errorf("read_skill_file: skill_name and path are required")
			}

			s := e.skillLoader.Get(skillName)
			if s == nil {
				return "", fmt.Errorf("read_skill_file: skill %q not found", skillName)
			}

			// Safety: prevent path traversal outside the skill directory
			absPath := filepath.Join(s.Dir, relPath)
			if !strings.HasPrefix(absPath, s.Dir+string(filepath.Separator)) {
				return "", fmt.Errorf("read_skill_file: path traversal not allowed")
			}

			data, err := os.ReadFile(absPath)
			if err != nil {
				return "", fmt.Errorf("read_skill_file: %w", err)
			}
			return string(data), nil
		},
	})

	return tools
}

// buildKBSearchBuiltin returns the RAG retrieval tool. Gate "knowledge": only
// offered when the agent has declared at least one KB in its SOUL.yaml
// `knowledge:` list. Caller must ensure e.knowledge is non-nil.
func (e *Engine) buildKBSearchBuiltin() BuiltinTool {
	return BuiltinTool{
		Gate:        "knowledge",
		Name:        "kb_search",
		Description: "Search a knowledge base for passages relevant to a query. Returns the top-K most semantically similar chunks with their source document and similarity score. Use this whenever the user's question might be answered by indexed reference material.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kb": map[string]any{
					"type":        "string",
					"description": "The knowledge base name to search (must be listed in this agent's available knowledge bases).",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "The natural-language search query. Be specific — use the user's actual terms when possible.",
				},
				"top_k": map[string]any{
					"type":        "integer",
					"description": "How many passages to return (default 10, max 20). Use 10+ for broad questions or when the corpus has multiple related documents that might compete for the top spots.",
				},
			},
			"required": []string{"kb", "query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			kbName := argString(args, "kb")
			query := argString(args, "query")
			topK := argInt(args, "top_k", 10)
			if topK > 20 {
				topK = 20
			}
			if kbName == "" {
				return "", fmt.Errorf("kb_search: kb is required")
			}
			return e.knowledge.Search(ctx, kbName, query, topK)
		},
	}
}

// buildSemanticMemoryBuiltin returns the semantic_memory_search tool backed by
// sqlite-vec. Only added when e.vectorStore is non-nil.
func (e *Engine) buildSemanticMemoryBuiltin() BuiltinTool {
	return BuiltinTool{
		Name:        "semantic_memory_search",
		Gate:        "",
		Description: "Search long-term semantic memory for past conversations, facts, and context that match a natural-language query. Use when the current conversation lacks background that the agent may have seen in previous sessions.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural language query to search past memory for",
				},
				"top_k": map[string]any{
					"type":        "integer",
					"description": "Number of memories to return (default 5, max 20)",
				},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			query := argString(args, "query")
			topK := argInt(args, "top_k", 5)
			if topK <= 0 {
				topK = 5
			}
			results, err := e.vectorStore.Search(ctx, query, topK)
			if err != nil {
				return "", fmt.Errorf("semantic_memory_search: %w", err)
			}
			if len(results) == 0 {
				return "No relevant memories found.", nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Semantic memory results for %q:\n\n", query))
			for i, r := range results {
				sb.WriteString(fmt.Sprintf("%d. [%s | %.3f similarity]\n%s\n\n",
					i+1, r.Entry.CreatedAt.Format("2006-01-02 15:04"), 1-r.Distance, r.Entry.Content))
			}
			return sb.String(), nil
		},
	}
}

// privilegedSystemTools is the set of OS-level built-ins that can mutate the
// host or execute arbitrary code (SEC-3 "SYSTEM" partition). These are offered
// ONLY when the server permits (runtime.allow_system_tools) AND the agent
// declares the "system" capability. Everything else returned by
// buildSystemTools is treated as a read-only "SAFE" tool, always available.
//
// SYSTEM (privileged):
//
//	shell_exec      — arbitrary /bin/sh -c
//	run_script      — execute a script file with an interpreter
//	install_library — pip/npm/brew/apt package installs
//	write_file      — create/overwrite/append host files
//	download_file   — write arbitrary bytes from a URL to disk
//
// SAFE (read-only, always on): read_file, list_dir, find_files, fetch_url,
//
//	http_request, env_get, sys_info. (http_request can POST, but it cannot
//	touch the local filesystem or spawn processes; it is governed instead by
//	SSRF protection + per-agent confirm_tools, so it stays in the SAFE set.)
var privilegedSystemTools = map[string]bool{
	"shell_exec":      true,
	"run_script":      true,
	"install_library": true,
	"write_file":      true,
	"download_file":   true,
}

// isPrivilegedSystemTool reports whether name is in the SEC-3 SYSTEM partition.
func isPrivilegedSystemTool(name string) bool { return privilegedSystemTools[name] }

// safeSystemTools returns only the read-only OS-level built-ins (the SAFE
// partition). Always available regardless of allow_system_tools / capabilities.
func (e *Engine) safeSystemTools() []BuiltinTool {
	all := e.buildSystemTools()
	out := all[:0:0]
	for _, b := range all {
		if !isPrivilegedSystemTool(b.Name) {
			out = append(out, b)
		}
	}
	return out
}

// IsSystemAgentAllowed checks if the given agent is explicitly allowed by the server
// to access destructive OS-level tools. It checks the global allowSystemAgents list.
func (e *Engine) IsSystemAgentAllowed(def *agent.Definition) bool {
	if def == nil || e.allowSystemAgents == nil {
		return false
	}
	for _, id := range e.allowSystemAgents {
		if id == "*" || id == "all" || id == def.ID {
			return true
		}
	}
	return false
}

// systemToolsFor returns the OS-level built-ins this agent may use. The SAFE
// partition is always included; the privileged SYSTEM partition is added only
// when the server permits system tools for this agent AND the agent has the "system"
// capability (SEC-3 double opt-in). When privileged tools are excluded the
// caller still won't dispatch them — gating is centralised here.
func (e *Engine) systemToolsFor(def *agent.Definition) []BuiltinTool {
	all := e.buildSystemTools()
	allowPrivileged := e.IsSystemAgentAllowed(def) && def.HasCapability("system")
	out := make([]BuiltinTool, 0, len(all))
	for _, b := range all {
		if isPrivilegedSystemTool(b.Name) && !allowPrivileged {
			continue
		}
		out = append(out, b)
	}
	return out
}

// buildSystemTools returns the FULL set of OS-level built-in tools (both the
// SAFE and SYSTEM partitions). This is the canonical catalog; callers that
// need gating use safeSystemTools / systemToolsFor instead of this directly.
//
// WARNING: the SYSTEM-partition tools (see privilegedSystemTools) execute
// arbitrary shell commands and write files on the host. They are only offered
// to agents that pass the SEC-3 double opt-in.
func (e *Engine) buildSystemTools() []BuiltinTool {
	// ARCH-2: the tool definitions now live in per-domain files
	// (engine_tools_shell.go, engine_tools_files.go, engine_tools_http.go,
	// engine_tools_misc.go). This concatenates them into the canonical
	// full catalog. The SEC-3 SAFE/SYSTEM partition is applied by callers
	// (safeSystemTools / systemToolsFor) via privilegedSystemTools, not here.
	out := make([]BuiltinTool, 0, 12)
	out = append(out, e.buildShellTools()...)
	out = append(out, e.buildFileTools()...)
	out = append(out, e.buildHTTPTools()...)
	out = append(out, e.buildMiscTools()...)
	return out
}

// webSearch routes the query to the configured search provider.
// searchResultItem is one normalized web_search hit.
type searchResultItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// marshalSearchResults renders web_search output as a JSON object string —
// {"query":..., "result_count":N, "results":[{title,url,content},...]} — rather
// than prose. A structured shape is the contract downstream Studio flow/python
// nodes rely on (they do inputs["var"]["results"]); LLM agents read the JSON
// equally well. An empty result set still returns a valid object with results:[]
// so callers can branch on result_count without special-casing a "no results"
// sentence. Content is truncated to keep payloads bounded.
func marshalSearchResults(query string, items []searchResultItem) (string, error) {
	if items == nil {
		items = []searchResultItem{}
	}
	for i := range items {
		items[i].Content = strings.TrimSpace(items[i].Content)
		if len(items[i].Content) > 600 {
			items[i].Content = items[i].Content[:600] + "…"
		}
	}
	b, err := json.Marshal(map[string]any{
		"query":        query,
		"result_count": len(items),
		"results":      items,
	})
	if err != nil {
		return "", fmt.Errorf("web_search: encode results: %w", err)
	}
	return string(b), nil
}

func (e *Engine) webSearch(ctx context.Context, args map[string]any) (string, error) {
	provider, _ := e.getSearchConfig()
	provider = strings.ToLower(provider)
	if provider == "" {
		provider = "ollama"
	}
	switch provider {
	case "tavily":
		return e.tavilyWebSearch(ctx, args)
	case "serper":
		return e.serperWebSearch(ctx, args)
	default:
		return e.ollamaWebSearch(ctx, args)
	}
}

// tavilyWebSearch implements the web_search tool via Tavily API.
func (e *Engine) tavilyWebSearch(ctx context.Context, args map[string]any) (string, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return "", fmt.Errorf("web_search: query is required")
	}
	maxResults := argInt(args, "max_results", 5)
	if maxResults <= 0 {
		maxResults = 5
	}

	_, key := e.getSearchConfig()
	if key == "" {
		key = os.Getenv("TAVILY_API_KEY")
	}
	if key == "" {
		return "", fmt.Errorf("web_search: no Tavily API key. Set TAVILY_API_KEY environment variable or search.api_key in config.yaml")
	}

	payload, err := json.Marshal(map[string]any{
		"api_key":     key,
		"query":       query,
		"max_results": maxResults,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search (tavily): request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_search (tavily): API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("web_search (tavily): decode response: %w", err)
	}
	items := make([]searchResultItem, 0, len(out.Results))
	for _, r := range out.Results {
		items = append(items, searchResultItem{Title: r.Title, URL: r.URL, Content: r.Content})
	}
	return marshalSearchResults(query, items)
}

// serperWebSearch implements the web_search tool via Serper API.
func (e *Engine) serperWebSearch(ctx context.Context, args map[string]any) (string, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return "", fmt.Errorf("web_search: query is required")
	}
	maxResults := argInt(args, "max_results", 5)
	if maxResults <= 0 {
		maxResults = 5
	}

	_, key := e.getSearchConfig()
	if key == "" {
		key = os.Getenv("SERPER_API_KEY")
	}
	if key == "" {
		return "", fmt.Errorf("web_search: no Serper API key. Set SERPER_API_KEY environment variable or search.api_key in config.yaml")
	}

	payload, err := json.Marshal(map[string]any{
		"q":   query,
		"num": maxResults,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://google.serper.dev/search", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-API-KEY", key)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search (serper): request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_search (serper): API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("web_search (serper): decode response: %w", err)
	}
	items := make([]searchResultItem, 0, len(out.Organic))
	for _, r := range out.Organic {
		items = append(items, searchResultItem{Title: r.Title, URL: r.Link, Content: r.Snippet})
	}
	return marshalSearchResults(query, items)
}

// ollamaWebSearch implements the built-in web_search tool via the Ollama Web
// Search API (https://ollama.com/api/web_search). Requires an Ollama API key.
func (e *Engine) ollamaWebSearch(ctx context.Context, args map[string]any) (string, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return "", fmt.Errorf("web_search: query is required")
	}
	maxResults := argInt(args, "max_results", 5)
	if maxResults <= 0 {
		maxResults = 5
	}

	key := os.Getenv("OLLAMA_API_KEY")
	if key == "" {
		key = e.getOllamaAPIKey()
	}
	if key == "" {
		return "", fmt.Errorf("web_search: no Ollama API key. Create one at https://ollama.com/settings/keys, then set the OLLAMA_API_KEY environment variable or llm.providers.ollama.api_key in config.yaml")
	}

	payload, _ := json.Marshal(map[string]any{"query": query, "max_results": maxResults})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://ollama.com/api/web_search", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_search: Ollama API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("web_search: decode response: %w", err)
	}
	items := make([]searchResultItem, 0, len(out.Results))
	for _, r := range out.Results {
		items = append(items, searchResultItem{Title: r.Title, URL: r.URL, Content: r.Content})
	}
	return marshalSearchResults(query, items)
}

// Handle processes an inbound message and returns the agent's reply.
// It is safe to call concurrently from multiple goroutines.
//
// Named returns are used here so the failure-notification defer can see
// the final err value without every internal return-path having to
// manually shadow a local. Successful runs leave err == nil → defer's
// notify branch is a no-op.
func (e *Engine) Handle(ctx context.Context, msg message.Message) (reply message.Message, err error) {
	// Per-run timing + outcome counter. Outcome resolved at deferred-call
	// time so panics still record as "error". (PRODUCTION_AUDIT → MED/Observability)
	//
	// Failure notification (added 2026-05-28): when the run errors and the
	// engine has a FailureNotifier wired (typically from main.go via
	// chanReg.Send), we fire it here from the same defer. Single source of
	// truth so a hosted cron failure produces the same notification as a
	// channel-triggered failure with no per-call-site duplication. The
	// notifier itself decides what to do — by default it (a) honours the
	// agent's notify_on_failure block, and (b) for inbound channel
	// messages with no explicit target, replies on the same channel so
	// the original user sees the error.
	runStart := time.Now()
	runOutcome := "error"
	defer func() {
		metrics.AgentRunDuration.WithLabelValues(msg.AgentID).Observe(time.Since(runStart).Seconds())
		metrics.AgentRunsTotal.WithLabelValues(msg.AgentID, runOutcome).Inc()
		if err != nil {
			// Push failed run to dead-letter queue, if one is wired.
			if e.dlqStore != nil {
				payload, _ := json.Marshal(msg)
				dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if dlqErr := e.dlqStore.PushFailed(dctx, msg.AgentID, payload, err.Error()); dlqErr != nil {
					e.log.Warn("dlq push failed", zap.Error(dlqErr))
				}
				dcancel()
			}
			if e.failureNotifier != nil {
				d := e.loader.Get(msg.AgentID)
				if d == nil {
					// Synthesize a placeholder so the notifier can still
					// route the message via the inbound-channel fallback
					// when the agent ID itself was unknown.
					d = &agent.Definition{ID: msg.AgentID}
				}
				notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				e.failureNotifier.NotifyFailure(notifyCtx, d, msg, err.Error())
			}
		}
	}()

	// Stamp session ID onto the context so nested helpers (audit, confirm) can
	// retrieve it without threading msg through every call.
	ctx = context.WithValue(ctx, inboundMsgKey{}, msg)

	// Task #32 — OTEL span for this agent run.
	if e.tracer != nil {
		var span interface{ End() }
		ctx, span = e.tracer.Start(ctx, "engine.Handle",
			"agent.id", msg.AgentID,
			"session.id", msg.SessionID)
		defer span.End()
	}

	// Resolve agent definition
	def := e.loader.Get(msg.AgentID)
	if def == nil {
		return message.Message{}, fmt.Errorf("engine: unknown agent %q", msg.AgentID)
	}
	def = def.Clone()
	applyPlaygroundOverrides(def, msg.Metadata)
	if !def.Enabled {
		return message.Message{}, fmt.Errorf("engine: agent %q is disabled", msg.AgentID)
	}
	if def.ID == SystemAgentID && msg.Channel != "http" {
		return message.Message{}, fmt.Errorf("engine: system agent is only available on http channel")
	}

	// Router short-circuit. Kind=="router" agents have no LLM loop — they
	// match the inbound text against def.Routes and forward to the first
	// matching peer via the existing agent__<id> peer-call path. See
	// docs/CHANNEL_DESIGN.md Q2. The peer's reply is returned verbatim.
	// Defined as a method so router-specific logic (rule matching, trace
	// events) lives alongside the dispatcher without bloating Handle.
	if def.Kind == "router" {
		return e.dispatchRouter(ctx, def, msg)
	}

	// Provider allowlist guard. Closes the "GUI dropdown fat-finger →
	// paid-API hit" class of failure: an agent that declares
	// `allowed_providers: [ollama]` can never dial out to Anthropic /
	// OpenAI / Gemini even if someone saves the wrong provider into the
	// agent's LLM config. Emit an error event so the trace shows the
	// actionable message ("provider 'anthropic' not in allowed_providers
	// [ollama]") instead of a downstream "401 credit balance" or "404
	// model not found" failure that looks like the model's fault.
	if !providerAllowed(def.LLM.AllowedProviders, def.LLM.Provider) {
		errMsg := fmt.Sprintf(
			"engine: llm provider %q not in allowed_providers %v for agent %q "+
				"(set llm.allowed_providers in SOUL.yaml to widen, or change "+
				"llm.provider to one already on the list)",
			def.LLM.Provider, def.LLM.AllowedProviders, msg.AgentID,
		)
		e.sink.Emit(message.Event{
			Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{
				"message":           errMsg,
				"reason":            "provider_not_allowed",
				"provider":          def.LLM.Provider,
				"allowed_providers": def.LLM.AllowedProviders,
			},
			Timestamp: time.Now().UTC(),
		})
		return message.Message{}, fmt.Errorf("%s", errMsg)
	}

	e.sink.Emit(message.Event{
		Type: "message.in", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: msg, Timestamp: time.Now().UTC(),
	})

	// E5 — Structured Workflow Scaffolding: if the agent declares a workflow,
	// delegate entirely to WorkflowExecutor instead of the free-form LLM loop.
	// The executor checkpoints each step and can resume after a crash.
	if def.Workflow != nil {
		we := NewWorkflowExecutor(*def.Workflow, e, e.checkpoints, e.log)
		wfResult, wfErr := we.Run(ctx, msg, "")
		if wfErr != nil {
			e.sink.Emit(message.Event{
				Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "workflow", "error": wfErr.Error()},
				Timestamp: time.Now().UTC(),
			})
			return message.Message{}, fmt.Errorf("engine: workflow: %w", wfErr)
		}
		var replyText string
		if wfResult != nil {
			if err := json.Unmarshal(wfResult, &replyText); err != nil {
				// Not a plain JSON string — use the raw JSON as the reply text.
				replyText = string(wfResult)
			}
		}
		if replyText == "" {
			replyText = "(workflow completed)"
		}
		reply = message.Message{
			ID:        msg.ID,
			SessionID: msg.SessionID,
			AgentID:   msg.AgentID,
			Channel:   msg.Channel,
			ThreadID:  msg.ThreadID,
			Role:      message.RoleAssistant,
			Parts:     message.Text(replyText),
			CreatedAt: time.Now().UTC(),
		}
		e.sink.Emit(message.Event{
			Type: "message.out", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: reply, Timestamp: time.Now().UTC(),
		})
		runOutcome = "success"
		return reply, nil
	}

	// Retrieve or create session
	sess := e.getOrCreateSession(msg.SessionID, msg.AgentID)

	// PERF-1: mark the session as actively in use for the duration of this
	// Handle call so the eviction sweep never reclaims a mid-conversation
	// session. lastAccess was already bumped by getOrCreateSession.
	sess.mu.Lock()
	sess.inUse++
	sess.mu.Unlock()
	defer func() {
		sess.mu.Lock()
		sess.inUse--
		sess.lastAccess = time.Now().UTC()
		sess.mu.Unlock()
	}()

	// ── Passphrase gate ───────────────────────────────────────────────────────
	// Enforced in Go before the LLM is ever invoked. The model cannot bypass
	// this check regardless of prompt injection or instruction following.
	if sec := def.Security; sec != nil && sec.Passphrase != "" {
		sess.mu.Lock()
		verified := sess.PassphraseVerified
		sess.mu.Unlock()

		userText := flattenParts(msg.Parts)
		if !verified {
			if userText == sec.Passphrase {
				// Correct passphrase — mark session as verified and acknowledge.
				sess.mu.Lock()
				sess.PassphraseVerified = true
				sess.mu.Unlock()
				reply = message.Message{
					ID:        msg.ID + "-auth",
					SessionID: msg.SessionID,
					AgentID:   msg.AgentID,
					Channel:   msg.Channel,
					ThreadID:  msg.ThreadID,
					Role:      message.RoleAssistant,
					Parts:     message.Text("✅ Access granted. How can I help you?"),
					CreatedAt: time.Now().UTC(),
				}
				return reply, nil
			}
			// Wrong or missing passphrase — challenge without invoking the LLM.
			prompt := sec.PassphrasePrompt
			if prompt == "" {
				prompt = "🔒 Please provide your access passphrase to continue."
			}
			reply = message.Message{
				ID:        msg.ID + "-auth",
				SessionID: msg.SessionID,
				AgentID:   msg.AgentID,
				Channel:   msg.Channel,
				ThreadID:  msg.ThreadID,
				Role:      message.RoleAssistant,
				Parts:     message.Text(prompt),
				CreatedAt: time.Now().UTC(),
			}
			return reply, nil
		}
	}

	// Persist inbound message to memory
	if err := e.memory.Write(memory.Entry{
		AgentID: msg.AgentID, SessionID: msg.SessionID,
		Scope:   memory.ScopeSession,
		Content: fmt.Sprintf("[%s] %s", msg.Username, flattenParts(msg.Parts)),
	}); err != nil {
		e.log.Warn("memory write failed (inbound)", zap.String("agent", msg.AgentID), zap.Error(err))
	}

	// Prime the prefix cache for this Handle. Cleared on exit so a
	// hot-reload between user messages picks up the new def's catalogs.
	// (PRODUCTION_AUDIT → MED/Engine.)
	sysPrefix := e.buildSystemPrefix(def)
	sess.mu.Lock()
	e.appendHistoryLocked(sess, llm.ChatMessage{
		Role: "user", Content: flattenParts(msg.Parts),
	})
	sess.cachedPrefix = sysPrefix
	sess.mu.Unlock()
	defer func() {
		sess.mu.Lock()
		sess.cachedPrefix = ""
		sess.mu.Unlock()
	}()

	// Story 16 — pluggable reasoning loops: agents that declare a reasoning:
	// block with a strategy run through the E15 reasoning Loop instead of the
	// classic tool loop below. Agents without one are untouched — the ok=false
	// branch falls straight through to the existing path.
	if loopCfg, ok := reasoning.LoopConfigFromDefinition(def, sysPrefix); ok {
		reply, rerr := e.handleWithReasoning(ctx, def, sess, msg, loopCfg)
		if rerr == nil {
			runOutcome = "success"
		}
		return reply, rerr
	}

	// Build context messages
	chatMsgs := e.buildContext(def, sess, msg)

	// Build tool schemas for this agent (Python tools + opt-in Go built-ins).
	// Pass the inbound channel so system tools are gated to HTTP-only.
	tools := e.allToolSchemas(def, msg.Channel)

	// Auto-delegate: when SOUL.yaml sets `llm.tool_choice: agent__<id>` and
	// `<id>` is one of the declared peers, do the peer call HERE before the
	// LLM ever runs. Reason: local models (qwen2.5:72b in particular) routinely
	// ignore Ollama's tool_choice constraint and answer from training data
	// instead of delegating. We bypass model cooperation by running the peer
	// ourselves, then inject a synthetic assistant→tool round-trip into
	// chatMsgs so the model's first real turn sees the result in context and
	// just has to synthesise. The model genuinely believes it called the tool.
	autoDelegated := false
	if tc := strings.TrimSpace(def.LLM.ToolChoice); strings.HasPrefix(tc, AgentToolPrefix) {
		peerID := strings.TrimPrefix(tc, AgentToolPrefix)
		isPeer := false
		for _, p := range e.resolveAgentRefs(def.Agents, def.ID) {
			if p.ID == peerID {
				isPeer = true
				break
			}
		}
		if isPeer {
			userText := flattenParts(msg.Parts)
			peerArgs := map[string]any{"message": userText}
			e.sink.Emit(message.Event{
				Type: "tool.call", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload: message.ToolCall{
					ID: "auto-" + uuidShort(), Name: tc, Arguments: peerArgs,
				},
				Timestamp: time.Now().UTC(),
			})
			peerResp, perr := e.runAgentCall(ctx, def, tc, peerArgs)
			peerCallID := "auto-" + uuidShort()
			if perr != nil {
				e.log.Warn("auto-delegate failed; falling through to normal LLM loop",
					zap.String("agent", def.ID), zap.String("peer", peerID), zap.Error(perr))
			} else {
				autoDelegated = true
				// Synthetic assistant message recording the (forced) tool call
				// + the tool-role message carrying the peer's reply. The LLM
				// will see this as if it had decided to delegate on its own.
				chatMsgs = append(chatMsgs,
					llm.ChatMessage{
						Role: "assistant", Content: "",
						ToolCalls: []message.ToolCall{{ID: peerCallID, Name: tc, Arguments: peerArgs}},
					},
					llm.ChatMessage{
						Role: "tool", Content: peerResp, ToolCallID: peerCallID, Name: tc,
					},
				)
				e.sink.Emit(message.Event{
					Type: "tool.result", AgentID: msg.AgentID, SessionID: msg.SessionID,
					Payload:   message.ToolResult{CallID: peerCallID, Name: tc, Content: peerResp},
					Timestamp: time.Now().UTC(),
				})
			}
		}
	}
	// Agentic loop: LLM → tool calls → LLM → … → final reply
	maxTurns := def.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}

	model := def.LLM.Model
	if model == "" {
		model = "(provider default)"
	}

	// Anti-loop guard: remember tool calls already executed in this run (keyed by
	// name + arguments). If the model re-issues an identical call, we return the
	// cached result with a nudge to move on instead of re-running it — this stops
	// weaker models from burning every turn re-calling the same tool.
	seen := make(map[string]string)
	var seenMu sync.Mutex

	var finalContent string
	for turn := 0; turn < maxTurns; turn++ {
		// Enable streaming on the final-turn request when the agent opted in
		// AND the caller attached a token callback. We only stream when there
		// are no tools (streaming + tool calls requires careful merging that
		// providers handle differently; the non-streaming path is authoritative
		// for tool-call turns). Providers set resp.Stream = nil when they fall
		// through to the non-streaming code path, so the drain below is a no-op.
		streamCB := streamCallback(ctx)
		wantStream := def.StreamReply && streamCB != nil && len(tools) == 0
		req := llm.CompletionRequest{
			Model:       def.LLM.Model,
			Messages:    chatMsgs,
			Tools:       tools,
			Temperature: def.LLM.Temperature,
			MaxTokens:   def.LLM.MaxTokens,
			Stream:      wantStream,
		}
		// Tool-choice constraint applies ONLY on turn 1 AND only if we didn't
		// already auto-delegate above (otherwise we'd force the same tool a
		// second time after its result is already in context). After turn 1 the
		// model must be free to synthesise the final answer once tool results
		// have come back — leaving it forced would trap us in a tool-call loop.
		if turn == 0 && !autoDelegated && def.LLM.ToolChoice != "" && len(tools) > 0 {
			req.ToolChoice = def.LLM.ToolChoice
		}

		e.sink.Emit(message.Event{
			Type: "llm.call", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload:   map[string]any{"provider": def.LLM.Provider, "model": model, "turn": turn + 1},
			Timestamp: time.Now().UTC(),
		})
		llmStart := time.Now()

		resp, err := e.llmRouter.Complete(ctx, def.LLM.Provider, req)
		// Prometheus: per-call duration + outcome counter + token counts.
		// (PRODUCTION_AUDIT → MED/Observability)
		llmProviderLabel := def.LLM.Provider
		if llmProviderLabel == "" {
			llmProviderLabel = "(default)"
		}
		metrics.LLMCallDuration.WithLabelValues(llmProviderLabel, model).Observe(time.Since(llmStart).Seconds())
		if err != nil {
			metrics.LLMCallsTotal.WithLabelValues(llmProviderLabel, model, "error").Inc()
			e.sink.Emit(message.Event{
				Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "llm", "error": err.Error()},
				Timestamp: time.Now().UTC(),
			})
			return message.Message{}, fmt.Errorf("engine: llm call: %w", err)
		}
		metrics.LLMCallsTotal.WithLabelValues(llmProviderLabel, model, "success").Inc()
		if resp.InputTokens > 0 {
			metrics.LLMInputTokens.WithLabelValues(llmProviderLabel, model).Add(float64(resp.InputTokens))
		}
		if resp.OutputTokens > 0 {
			metrics.LLMOutputTokens.WithLabelValues(llmProviderLabel, model).Add(float64(resp.OutputTokens))
		}
		// Task #32 — record per-call token usage in the cost store.
		e.recordUsage(ctx, msg.AgentID, msg.SessionID, llmProviderLabel, model,
			resp.InputTokens, resp.OutputTokens)

		// Drain the streaming channel, delivering tokens to the caller's
		// callback and accumulating them into resp.Content. If the provider
		// didn't open a stream (non-streaming path or tool-call turn),
		// resp.Stream is nil and this block is a no-op.
		if resp.Stream != nil {
			var sb strings.Builder
			for token := range resp.Stream {
				if streamCB != nil {
					streamCB(token)
				}
				sb.WriteString(token)
			}
			resp.Content = sb.String()
		}

		e.sink.Emit(message.Event{
			Type: "llm.result", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{
				"model":         model,
				"input_tokens":  resp.InputTokens,
				"output_tokens": resp.OutputTokens,
				"duration_ms":   time.Since(llmStart).Milliseconds(),
				"tool_calls":    len(resp.ToolCalls),
			},
			Timestamp: time.Now().UTC(),
		})

		// No tool calls → we have a final answer
		if len(resp.ToolCalls) == 0 {
			finalContent = resp.Content
			break
		}

		// Loop-breaker: if EVERY tool call this turn was already executed with the
		// same arguments, the model is stuck repeating itself (some models emit a
		// spurious tool call alongside their real answer every turn). Stop here and
		// use the model's text as the final answer instead of burning all turns.
		allDup := len(resp.ToolCalls) > 0
		for _, tc := range resp.ToolCalls {
			aj, _ := json.Marshal(tc.Arguments)
			name := normalizeToolCallName(tc.Name)

			// Stateful tools should not trigger the loop breaker since their
			// execution environment or underlying files may have changed.
			if name == "shell_exec" || name == "run_script" || name == "http_request" {
				allDup = false
				break
			}

			seenMu.Lock()
			_, ok := seen[name+"|"+string(aj)]
			seenMu.Unlock()
			if !ok {
				allDup = false
				break
			}
		}
		if allDup {
			// Model is repeating tools instead of answering. Stop the tool loop;
			// the post-loop synthesis step will force a plain-text final answer.
			finalContent = resp.Content // usually empty for these models
			break
		}

		// Execute each tool call
		toolResults := e.executeToolCalls(ctx, def, msg.SessionID, resp.ToolCalls, seen, &seenMu)

		// Append assistant + tool result turns for next loop iteration.
		// NOTE: release sess.mu BEFORE calling buildContext — buildContext locks
		// sess.mu itself, and Go mutexes are not reentrant, so holding it here
		// would deadlock the agent on its second turn (any tool-using agent).
		sess.mu.Lock()
		turns := []llm.ChatMessage{
			{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls},
		}
		for _, tr := range toolResults {
			turns = append(turns, llm.ChatMessage{
				Role: "tool", Content: tr.Content, ToolCallID: tr.CallID, Name: tr.Name,
			})
		}
		e.appendHistoryLocked(sess, turns...)
		sess.mu.Unlock()

		chatMsgs = e.buildContext(def, sess, msg) // rebuild with tool results
	}

	if strings.TrimSpace(finalContent) == "" {
		// The model kept calling tools and never produced a plain-text reply.
		// Force a tool-free synthesis from everything already gathered.
		finalContent = e.finalSynthesis(ctx, def, msg.AgentID, msg.SessionID, chatMsgs)
	}

	// Structured output enforcement: if the agent has an output_schema, validate
	// the final reply parses as JSON. On failure, do ONE corrective retry that
	// asks the model to fix its output (with response_format=json_schema). If
	// that still fails, we surface whatever we have — the caller can inspect.
	if def.LLM.OutputSchema != nil && strings.TrimSpace(finalContent) != "" {
		if _, perr := parseJSONLoose(finalContent); perr != nil {
			e.sink.Emit(message.Event{
				Type: "warn", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "output-schema", "error": perr.Error(), "retry": true},
				Timestamp: time.Now().UTC(),
			})
			corrected := e.finalSynthesisStructured(ctx, def, msg.AgentID, msg.SessionID, chatMsgs, finalContent, perr)
			if corrected != "" {
				finalContent = corrected
			}
		}
	}

	if strings.TrimSpace(finalContent) == "" {
		finalContent = "(no final response produced)"
		e.sink.Emit(message.Event{
			Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload:   map[string]any{"stage": "loop", "error": "no final response produced after synthesis"},
			Timestamp: time.Now().UTC(),
		})
	}

	reply = e.finalizeReply(ctx, def, sess, msg, finalContent)

	runOutcome = "success" // flips the deferred AgentRunsTotal counter from "error"
	return reply, nil
}

// finalizeReply is the shared tail of a successful run — classic loop and
// reasoning loop (Story 16) both end here so the persistence contract stays
// identical: append the assistant turn to in-memory session history, persist
// to session memory, write the episodic brain record, build the reply,
// emit message.out, and append both turns to the durable history store.
func (e *Engine) finalizeReply(ctx context.Context, def *agent.Definition, sess *Session, msg message.Message, finalContent string) message.Message {
	// Append final assistant response to the in-memory session history
	sess.mu.Lock()
	e.appendHistoryLocked(sess, llm.ChatMessage{
		Role: "assistant", Content: finalContent,
	})
	sess.mu.Unlock()

	// Persist reply to session memory
	if err := e.memory.Write(memory.Entry{
		AgentID: msg.AgentID, SessionID: msg.SessionID,
		Scope:   memory.ScopeSession,
		Content: fmt.Sprintf("[%s] %s", def.Name, finalContent),
	}); err != nil {
		e.log.Warn("memory write failed (reply)", zap.String("agent", msg.AgentID), zap.Error(err))
	}

	// RL-09: persist task + reply as an episodic brain memory record.
	// Fires when brainStore is wired and either (a) episodic is explicitly enabled,
	// or (b) reasoning is enabled and brain_memory wasn't configured (auto-default).
	episodicOn := def.BrainMemory.Episodic.Enabled ||
		(def.Reasoning.Strategy != "" && !def.BrainMemory.Episodic.Enabled &&
			!def.BrainMemory.Semantic.Enabled && !def.BrainMemory.Procedural.Enabled)
	if e.brainStore != nil && episodicOn {
		taskInput := flattenParts(msg.Parts)
		rec := agentmemory.ResultToEpisodicRecord(msg.AgentID, taskInput, finalContent, nil)
		if err := e.brainStore.Write(rec); err != nil {
			e.log.Warn("brain memory write failed", zap.String("agent", msg.AgentID), zap.Error(err))
		}
	}

	reply := message.Message{
		ID:        msg.ID, // correlate reply to request
		SessionID: msg.SessionID,
		AgentID:   msg.AgentID,
		Channel:   msg.Channel,
		ThreadID:  msg.ThreadID,
		Role:      message.RoleAssistant,
		Parts:     message.Text(finalContent),
		CreatedAt: time.Now().UTC(),
	}

	e.sink.Emit(message.Event{
		Type: "message.out", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: reply, Timestamp: time.Now().UTC(),
	})

	// Persist user + assistant turns to the conversation history store.
	if e.historyStore != nil {
		userContent := flattenParts(msg.Parts)
		if err := e.historyStore.Append(ctx, session.ConversationEntry{
			SessionID: msg.SessionID, AgentID: msg.AgentID,
			Role: "user", Content: userContent,
		}); err != nil {
			e.log.Warn("history store: append user turn failed", zap.Error(err))
		}
		if err := e.historyStore.Append(ctx, session.ConversationEntry{
			SessionID: msg.SessionID, AgentID: msg.AgentID,
			Role: "assistant", Content: finalContent,
		}); err != nil {
			e.log.Warn("history store: append assistant turn failed", zap.Error(err))
		}
	}

	return reply
}

// finalSynthesis makes one LLM call with NO tools, forcing the model to produce
// a plain-text answer from the context it already gathered. Used when a model
// won't stop emitting tool calls on its own (common with local/Ollama models).
func (e *Engine) finalSynthesis(ctx context.Context, def *agent.Definition, agentID, sessionID string, chatMsgs []llm.ChatMessage) string {
	model := def.LLM.Model
	if model == "" {
		model = "(provider default)"
	}
	// Pre-size to exact capacity so the trailing append doesn't trigger a
	// second re-allocation/copy on long conversations. (PRODUCTION_AUDIT
	// → MED/Engine — minor but free.)
	msgs := make([]llm.ChatMessage, 0, len(chatMsgs)+1)
	msgs = append(msgs, chatMsgs...)
	msgs = append(msgs, llm.ChatMessage{
		Role:    "system",
		Content: "Now write your final response for the user using the information already gathered above. Do NOT call any tools — reply with plain text only.",
	})

	e.sink.Emit(message.Event{
		Type: "llm.call", AgentID: agentID, SessionID: sessionID,
		Payload:   map[string]any{"provider": def.LLM.Provider, "model": model, "turn": "final-synthesis"},
		Timestamp: time.Now().UTC(),
	})
	start := time.Now()
	resp, err := e.llmRouter.Complete(ctx, def.LLM.Provider, llm.CompletionRequest{
		Model:       def.LLM.Model,
		Messages:    msgs,
		Temperature: def.LLM.Temperature,
		MaxTokens:   def.LLM.MaxTokens,
		// Tools intentionally omitted so the model must answer in text.
	})
	if err != nil {
		e.sink.Emit(message.Event{
			Type: "error", AgentID: agentID, SessionID: sessionID,
			Payload:   map[string]any{"stage": "final-synthesis", "error": err.Error()},
			Timestamp: time.Now().UTC(),
		})
		return ""
	}
	e.sink.Emit(message.Event{
		Type: "llm.result", AgentID: agentID, SessionID: sessionID,
		Payload: map[string]any{
			"model": model, "input_tokens": resp.InputTokens, "output_tokens": resp.OutputTokens,
			"duration_ms": time.Since(start).Milliseconds(), "turn": "final-synthesis",
		},
		Timestamp: time.Now().UTC(),
	})
	return resp.Content
}

// finalSynthesisStructured re-runs the synthesis with the agent's output schema
// enforced via the provider's native JSON-mode. Called once when the first
// pass produced text that didn't parse as JSON.
func (e *Engine) finalSynthesisStructured(ctx context.Context, def *agent.Definition, agentID, sessionID string, chatMsgs []llm.ChatMessage, previous string, perr error) string {
	model := def.LLM.Model
	if model == "" {
		model = "(provider default)"
	}
	corrective := fmt.Sprintf(
		"Your previous response was not valid JSON matching the required schema "+
			"(error: %s). Re-emit your final answer as a single JSON value that "+
			"validates against the schema. Do NOT include any prose, code fences, "+
			"or explanation — return ONLY the JSON value.\n\nPrevious output:\n%s",
		perr.Error(), previous,
	)
	msgs := make([]llm.ChatMessage, 0, len(chatMsgs)+1)
	msgs = append(msgs, chatMsgs...)
	msgs = append(msgs, llm.ChatMessage{Role: "system", Content: corrective})

	e.sink.Emit(message.Event{
		Type: "llm.call", AgentID: agentID, SessionID: sessionID,
		Payload:   map[string]any{"provider": def.LLM.Provider, "model": model, "turn": "structured-retry"},
		Timestamp: time.Now().UTC(),
	})
	resp, err := e.llmRouter.Complete(ctx, def.LLM.Provider, llm.CompletionRequest{
		Model:          def.LLM.Model,
		Messages:       msgs,
		Temperature:    def.LLM.Temperature,
		MaxTokens:      def.LLM.MaxTokens,
		ResponseFormat: "json_schema",
		JSONSchema:     def.LLM.OutputSchema,
	})
	if err != nil {
		e.sink.Emit(message.Event{
			Type: "error", AgentID: agentID, SessionID: sessionID,
			Payload:   map[string]any{"stage": "structured-retry", "error": err.Error()},
			Timestamp: time.Now().UTC(),
		})
		return ""
	}
	return strings.TrimSpace(resp.Content)
}

// parseJSONLoose accepts either a bare JSON value or one wrapped in ```json ... ```
// code fences (a common LLM habit). Returns the parsed value (or an error if
// neither shape parses).
func parseJSONLoose(s string) (any, error) {
	s = strings.TrimSpace(s)
	// Strip surrounding code fences if present.
	if strings.HasPrefix(s, "```") {
		// Remove the leading ``` (and optional "json" tag) and the trailing ```.
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	// Try to find the first { or [ — handles prefatory chatter the model leaks.
	first := -1
	for i, r := range s {
		if r == '{' || r == '[' {
			first = i
			break
		}
	}
	if first > 0 {
		s = s[first:]
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return v, nil
}

func (e *Engine) getOrCreateSession(sessionID, agentID string) *Session {
	// Key sessions by (agentID, sessionID) — NOT sessionID alone — because the
	// HTTP chat handler uses a fixed session id per browser user (`http-<userId>`),
	// so multiple agents in the Chat Tester would otherwise share the same in-
	// memory Session struct and bleed their History into each other. Result was
	// the Writer agent reproducing real filenames from prior RAG Demo runs even
	// though no tool was called. The (agent, session) tuple isolates per-agent
	// conversation state cleanly.
	now := time.Now().UTC()
	key := agentID + "|" + sessionID
	val, _ := e.sessions.LoadOrStore(key, &Session{
		ID: sessionID, AgentID: agentID, CreatedAt: now, lastAccess: now,
	})
	sess := val.(*Session)
	// Refresh the idle timer on every access so the eviction sweep (PERF-1)
	// never reclaims a session that is being touched.
	sess.mu.Lock()
	sess.lastAccess = now
	sess.mu.Unlock()
	return sess
}

// ── PERF-1: session eviction ────────────────────────────────────────────────

// SetSessionEviction configures the TTL + max-count eviction policy. ttl<=0
// falls back to defaultSessionTTL (24h); maxSessions<=0 falls back to
// defaultMaxSessions. Safe to call once at startup before StartSessionEviction.
func (e *Engine) SetSessionEviction(ttl time.Duration, maxSessions int) {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	if maxSessions <= 0 {
		maxSessions = defaultMaxSessions
	}
	e.sessionTTL = ttl
	e.maxSessions = maxSessions
}

// StartSessionEviction launches the background sweep goroutine that reclaims
// idle/excess sessions. The sweep runs every interval (clamped so a tiny TTL
// doesn't busy-loop). Idempotent: only the first call starts a sweeper. Call
// StopSessionEviction (or cancel via the returned stop) at shutdown.
func (e *Engine) StartSessionEviction(interval time.Duration) {
	e.evictOnce.Do(func() {
		if e.sessionTTL <= 0 {
			e.sessionTTL = defaultSessionTTL
		}
		if e.maxSessions <= 0 {
			e.maxSessions = defaultMaxSessions
		}
		if interval <= 0 {
			interval = e.sessionTTL / 12 // ~every 2h for the 24h default
		}
		if interval < time.Second {
			interval = time.Second
		}
		e.evictStop = make(chan struct{})
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-e.evictStop:
					return
				case <-ticker.C:
					e.sweepSessions(time.Now().UTC())
				}
			}
		}()
	})
}

// StopSessionEviction halts the background sweep goroutine, if running.
func (e *Engine) StopSessionEviction() {
	if e.evictStop != nil {
		select {
		case <-e.evictStop:
			// already closed
		default:
			close(e.evictStop)
		}
	}
}

// sweepSessions performs one eviction pass at wall-clock time `now`:
//   - any session idle longer than sessionTTL is evicted (unless in use), and
//   - if the live count still exceeds maxSessions, the oldest-idle sessions
//     are evicted until the count is back under the cap.
//
// A session with inUse > 0 is NEVER evicted — mid-conversation sessions are
// always retained. Returns the number of sessions evicted (used by tests).
func (e *Engine) sweepSessions(now time.Time) int {
	ttl := e.sessionTTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	maxSessions := e.maxSessions
	if maxSessions <= 0 {
		maxSessions = defaultMaxSessions
	}

	type liveSess struct {
		key  string
		sess *Session
		idle time.Time
	}
	var live []liveSess
	evicted := 0

	// Pass 1: TTL-based eviction; collect survivors for the count cap.
	e.sessions.Range(func(k, v any) bool {
		key := k.(string)
		sess := v.(*Session)
		sess.mu.Lock()
		inUse := sess.inUse
		last := sess.lastAccess
		sess.mu.Unlock()

		if inUse > 0 {
			// Mid-conversation — never evict.
			return true
		}
		if now.Sub(last) >= ttl {
			e.evictSession(key, sess)
			evicted++
			return true
		}
		live = append(live, liveSess{key: key, sess: sess, idle: last})
		return true
	})

	// Pass 2: count-cap eviction — drop oldest-idle survivors until under cap.
	if len(live) > maxSessions {
		sort.Slice(live, func(i, j int) bool { return live[i].idle.Before(live[j].idle) })
		overflow := len(live) - maxSessions
		for i := 0; i < len(live) && overflow > 0; i++ {
			ls := live[i]
			// Re-check in-use under lock — a session may have become active
			// between the two passes.
			ls.sess.mu.Lock()
			inUse := ls.sess.inUse
			ls.sess.mu.Unlock()
			if inUse > 0 {
				continue
			}
			e.evictSession(ls.key, ls.sess)
			evicted++
			overflow--
		}
	}

	if evicted > 0 && e.log != nil {
		e.log.Info("session eviction sweep", zap.Int("evicted", evicted))
	}
	return evicted
}

// evictSession persists the session's history (if a memory backend is present)
// and then removes it from the in-memory map. Persist-then-evict ensures no
// conversation context is lost when a session is reclaimed.
func (e *Engine) evictSession(key string, sess *Session) {
	if e.archive != nil {
		sess.mu.Lock()
		history := make([]llm.ChatMessage, len(sess.History))
		copy(history, sess.History)
		agentID := sess.AgentID
		sessionID := sess.ID
		sess.mu.Unlock()
		for _, m := range history {
			if m.Role == "system" {
				continue
			}
			_ = e.archive.Archive(memory.Entry{
				AgentID:   agentID,
				SessionID: sessionID,
				Scope:     memory.ScopeSession,
				Content:   m.Role + ": " + m.Content,
				CreatedAt: time.Now().UTC(),
			})
		}
	}
	e.sessions.Delete(key)
}

// ── PERF-2: history windowing ───────────────────────────────────────────────

// SetMaxHistoryTurns configures the per-session history cap. n<=0 falls back to
// defaultMaxHistoryTurns (100). Safe to call once at startup.
func (e *Engine) SetMaxHistoryTurns(n int) {
	if n <= 0 {
		n = defaultMaxHistoryTurns
	}
	e.maxHistoryTurns = n
}

// historyCap returns the effective per-session history cap.
func (e *Engine) historyCap() int {
	if e.maxHistoryTurns <= 0 {
		return defaultMaxHistoryTurns
	}
	return e.maxHistoryTurns
}

// appendHistoryLocked appends msgs to sess.History and then trims the history
// back to the configured window. The CALLER MUST hold sess.mu — this is the
// single funnel every history-append site goes through, so the window cap can
// never be missed. A leading system message (index 0) is always preserved.
func (e *Engine) appendHistoryLocked(sess *Session, msgs ...llm.ChatMessage) {
	sess.History = append(sess.History, msgs...)
	sess.History = trimHistory(sess.History, e.historyCap())
}

// trimHistory caps history to at most `cap` NON-system messages, dropping the
// OLDEST non-system messages first. If history[0] is a system message, it is
// always retained (it is not counted against the cap and never trimmed). cap<=0
// disables trimming. The returned slice reuses the backing array where possible.
func trimHistory(history []llm.ChatMessage, cap int) []llm.ChatMessage {
	if cap <= 0 || len(history) == 0 {
		return history
	}

	// Preserve a leading system message, if present.
	var head []llm.ChatMessage
	body := history
	if history[0].Role == "system" {
		head = history[:1]
		body = history[1:]
	}

	if len(body) <= cap {
		return history
	}

	// Keep the newest `cap` body messages.
	trimmedBody := body[len(body)-cap:]
	if len(head) == 0 {
		// Compact in place to avoid retaining the dropped prefix.
		out := make([]llm.ChatMessage, len(trimmedBody))
		copy(out, trimmedBody)
		return out
	}
	out := make([]llm.ChatMessage, 0, len(head)+len(trimmedBody))
	out = append(out, head...)
	out = append(out, trimmedBody...)
	return out
}

// buildSystemPrefix renders the system prompt plus skill/knowledge/agent
// catalogs into a single string. The output is deterministic for a given
// `def` (modulo any catalog data that mutates between calls — which is the
// caller's invalidation problem). buildContext calls this on the first turn
// only and reuses the result via the prefix cache below.
//
// PRODUCTION_AUDIT → MED/Engine: previously this whole block ran inside
// buildContext on every turn of every agent loop. For agents with large
// system prompts or many skills/KBs/peers, that was tens of KB of string
// concatenation per turn × turns × agents.
func (e *Engine) buildSystemPrefix(def *agent.Definition) string {
	// Phase 1 of the persona-blocks feature (docs/AGENT_DESIGN.md):
	// identity / personality / non_negotiables get rendered BEFORE the
	// operator's free-form system_prompt, with consistent framing across
	// every agent. Skip cleanly when the fields are absent — a SOUL.yaml
	// without these blocks behaves bit-for-bit like before.
	systemPrompt := renderPersonaPrefix(def) + def.SystemPrompt

	// RL-10: inject brain memory context before any other prompt additions.
	// When brainStore is wired, memory is ON BY DEFAULT for any agent that
	// has a reasoning strategy configured — no explicit brain_memory: block
	// needed in SOUL.yaml. Defaults: episodic max_inject=5, semantic max_inject=8.
	// Explicit brain_memory: settings always take precedence when present.
	if e.brainStore != nil {
		bm := def.BrainMemory
		reasoningEnabled := def.Reasoning.Strategy != ""
		// Apply defaults when reasoning is on but brain_memory wasn't configured.
		anyExplicit := bm.Episodic.Enabled || bm.Semantic.Enabled || bm.Procedural.Enabled
		if reasoningEnabled && !anyExplicit {
			bm.Episodic.Enabled = true
			bm.Episodic.MaxInject = 5
			bm.Procedural.Enabled = true
		}
		if bm.Episodic.Enabled || bm.Semantic.Enabled || bm.Procedural.Enabled {
			maxEp, maxSem := bm.Episodic.MaxInject, bm.Semantic.MaxInject
			if maxEp <= 0 {
				maxEp = 5
			}
			if maxSem <= 0 {
				maxSem = 8
			}
			result, err := e.brainStore.Retrieve(agentmemory.RetrieveQuery{
				AgentID:     def.ID,
				MaxEpisodic: maxEp,
				MaxSemantic: maxSem,
			})
			if err == nil {
				if block := agentmemory.BuildContextBlock(result); block != "" {
					systemPrompt += "\n\n" + block
				}
			}
		}
	}
	if e.skillLoader != nil && len(def.Skills) > 0 {
		if catalog := e.skillCatalogFor(def.Skills); catalog != "" {
			systemPrompt += "\n\n" +
				"## Available Skills\n" +
				"The following skills provide specialized instructions for specific tasks.\n" +
				"When a task matches a skill's description, call `read_skill` with the skill name\n" +
				"to load its full instructions before proceeding.\n\n" +
				catalog
		}
	}
	if e.knowledge != nil && len(def.Knowledge) > 0 {
		if catalog := e.knowledgeCatalogFor(def.Knowledge); catalog != "" {
			systemPrompt += "\n\n" +
				"## Available Knowledge Bases\n" +
				"These knowledge bases hold indexed reference material you can search with the\n" +
				"`kb_search` tool. Use kb_search when the user's question might be answered by\n" +
				"the indexed material; cite the source document in your final answer.\n\n" +
				catalog
		}
	}
	if len(def.Agents) > 0 {
		if catalog := e.agentCatalogFor(def); catalog != "" {
			systemPrompt += "\n\n" +
				"## Available Agents\n" +
				"These peer agents can be invoked as tools to delegate sub-tasks. Call\n" +
				"`agent__<id>` with a self-contained instruction in the `message` field\n" +
				"(the peer has no shared context with you). Use them when a sub-task fits\n" +
				"a peer's specialty rather than re-doing the work yourself.\n\n" +
				catalog
		}
	}
	return systemPrompt
}

// buildContext assembles the message slice handed to the LLM provider for
// one turn. Conservative correctness model:
//
//   - The system prefix (prompt + skill/knowledge/agent catalogs) is cached
//     on the Session struct for the lifetime of one Handle. Catalogs are
//     resolved from `def` which we treat as immutable for the duration of
//     a single Handle (Loader.Get already returns a shallow copy, so a
//     hot-reload mid-run can't mutate it under us).
//   - Memory entries and session history ARE re-read every turn — they
//     change between turns. Memory now tail-reads via readTailBytes so this
//     stays cheap even for long-lived sessions.
//
// Trade-off: agents that mutate their KB / skills mid-run won't see the new
// catalog until the next Handle call. That's the right trade — agents
// orchestrate their own tools; they don't reconfigure themselves mid-turn.
func (e *Engine) buildContext(def *agent.Definition, sess *Session, incoming message.Message) []llm.ChatMessage {
	// Resolve prefix from the session cache (set up at Handle entry below).
	// Falls back to a fresh computation for direct callers that haven't
	// primed the cache — keeps the function safe to call independently in
	// tests.
	sess.mu.Lock()
	prefix := sess.cachedPrefix
	sess.mu.Unlock()
	if prefix == "" {
		prefix = e.buildSystemPrefix(def)
	}
	msgs := []llm.ChatMessage{{Role: "system", Content: prefix}}

	// Inject recent memory
	entries, _ := e.memory.Read(def.ID, sess.ID, memory.ScopeSession, def.Memory.MaxTokens)
	if len(entries) > 0 {
		var sb strings.Builder
		sb.WriteString("## Memory\n")
		for i := len(entries) - 1; i >= 0; i-- {
			sb.WriteString(entries[i].Content)
			sb.WriteString("\n")
		}
		msgs = append(msgs, llm.ChatMessage{Role: "system", Content: sb.String()})
	}

	// Session history
	sess.mu.Lock()
	msgs = append(msgs, sess.History...)
	sess.mu.Unlock()

	return msgs
}

// executeToolCalls runs each tool in a sandboxed Python subprocess.
// Each tool definition points to a Python file; we call the named function
// with the tool's arguments as keyword arguments, capture stdout as the result.
func (e *Engine) executeToolCalls(ctx context.Context, def *agent.Definition, sessionID string, calls []message.ToolCall, seen map[string]string, seenMu *sync.Mutex) []message.ToolResult {
	results := make([]message.ToolResult, len(calls))

	for i, tc := range calls {
		tc = normalizeToolCall(tc)
		e.sink.Emit(message.Event{
			Type: "tool.call", AgentID: def.ID, SessionID: sessionID,
			Payload: tc, Timestamp: time.Now().UTC(),
		})

		// Anti-loop guard: if this exact tool+args was already run this turn,
		// don't run it again — hand back the prior result and tell the model
		// to move on.
		argsJSON, _ := json.Marshal(tc.Arguments)
		key := tc.Name + "|" + string(argsJSON)
		seenMu.Lock()
		prev, dup := seen[key]
		seenMu.Unlock()

		var result string
		isErr := false
		if dup {
			result = fmt.Sprintf(
				"NOTE: you already called %s with these arguments this run; do not call it again. "+
					"Use the result below and proceed to the next step.\n\n%s", tc.Name, prev)
		} else {
			// Per-tool timing + outcome counter. (PRODUCTION_AUDIT →
			// MED/Observability)
			toolStart := time.Now()
			var err error
			result, err = e.runTool(ctx, def, sessionID, tc)
			metrics.ToolCallDuration.WithLabelValues(tc.Name).Observe(time.Since(toolStart).Seconds())
			if err != nil {
				result = fmt.Sprintf("error: %v", err)
				isErr = true
				metrics.ToolCallsTotal.WithLabelValues(tc.Name, "error").Inc()
			} else {
				metrics.ToolCallsTotal.WithLabelValues(tc.Name, "success").Inc()
			}
			seenMu.Lock()
			seen[key] = result
			seenMu.Unlock()
		}

		results[i] = message.ToolResult{CallID: tc.ID, Name: tc.Name, Content: result, IsError: isErr}

		e.sink.Emit(message.Event{
			Type: "tool.result", AgentID: def.ID, SessionID: sessionID,
			Payload: results[i], Timestamp: time.Now().UTC(),
		})
	}
	return results
}

func normalizeToolCall(call message.ToolCall) message.ToolCall {
	call.Name = normalizeToolCallName(call.Name)
	if call.Name == "web_search" {
		call.Arguments = normalizeWebSearchArgs(call.Arguments)
	}
	return call
}

func normalizeToolCallName(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"agent:", "tool:", "function:", "functions."} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimSpace(strings.TrimPrefix(name, prefix))
			break
		}
	}
	switch strings.ToLower(name) {
	case "google:search", "google_search", "browser.search", "browser_search", "search", "web.search", "web-search":
		return "web_search"
	}
	return name
}

func normalizeWebSearchArgs(args map[string]any) map[string]any {
	if args == nil {
		return args
	}
	if _, ok := args["query"]; ok {
		return args
	}
	if q, ok := args["q"]; ok {
		args["query"] = q
		return args
	}
	if query, ok := args["queries"]; ok {
		switch v := query.(type) {
		case string:
			args["query"] = v
		case []string:
			args["query"] = strings.Join(v, " ")
		case []any:
			parts := make([]string, 0, len(v))
			for _, item := range v {
				if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
					parts = append(parts, s)
				}
			}
			args["query"] = strings.Join(parts, " ")
		default:
			args["query"] = fmt.Sprint(v)
		}
	}
	return args
}

func (e *Engine) runTool(ctx context.Context, def *agent.Definition, sessionID string, call message.ToolCall) (string, error) {
	// MCP tools — namespaced as mcp__<server>__<tool>. Route to the MCP client.
	if e.mcpClient != nil && strings.HasPrefix(call.Name, mcp.FullNamePrefix) {
		if !mcpToolAllowed(def, call.Name) {
			return "", fmt.Errorf("MCP tool %q is not allowed for agent %q", call.Name, def.ID)
		}
		tctx, cancel := context.WithTimeout(ctx, e.toolTimeout)
		defer cancel()
		return e.mcpClient.Call(tctx, call.Name, call.Arguments)
	}

	// Plugin tools — namespaced as plugin__<pluginID>__<tool>. Execute as a
	// Python subprocess using the handler path from the plugin manifest.
	if strings.HasPrefix(call.Name, "plugin__") && e.pluginProvider != nil {
		for _, pt := range e.pluginProvider.AllTools() {
			if pt.Name != call.Name {
				continue
			}
			if !strings.HasPrefix(pt.Handler, "python:") {
				return "", fmt.Errorf("plugin tool %q: unsupported handler scheme %q", call.Name, pt.Handler)
			}
			rest := strings.TrimPrefix(pt.Handler, "python:")
			parts := strings.SplitN(rest, "::", 2)
			if len(parts) != 2 {
				return "", fmt.Errorf("plugin tool %q: malformed handler %q", call.Name, pt.Handler)
			}
			pyFile, funcName := parts[0], parts[1]
			argsJSON, _ := json.Marshal(call.Arguments)
			script := fmt.Sprintf(`
import sys as _sys, json, importlib.util
# Redirect stdout → stderr so any print() inside the tool code does not
# corrupt the JSON result we write at the very end.
_orig_stdout = _sys.stdout
_sys.stdout = _sys.stderr
args = json.loads(_sys.stdin.read())
spec = importlib.util.spec_from_file_location("tool", %q)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
result = getattr(mod, %q)(**args)
_sys.stdout = _orig_stdout
print(result if isinstance(result, str) else json.dumps(result))
`, pyFile, funcName)
			// SEC-5: scrub env to base allowlist + agent-declared names.
			limits := e.sandboxLimits
			limits.EnvAllow = def.Env
			argv := sandbox.Wrap(e.selfPath, limits, []string{e.pythonBin, "-c", script})
			tctx, cancel := context.WithTimeout(ctx, e.toolTimeout)
			defer cancel()
			cmd := exec.CommandContext(tctx, argv[0], argv[1:]...)
			cmd.Stdin = bytes.NewReader(argsJSON)
			cmd.Env = sandbox.FilteredEnv(os.Environ(), def.Env)
			out, err := cmd.Output()
			if err != nil {
				return "", fmt.Errorf("plugin tool %q: %w", call.Name, err)
			}
			return strings.TrimSpace(string(out)), nil
		}
		return "", fmt.Errorf("plugin tool %q not found in any loaded plugin", call.Name)
	}

	// Peer-agent tools — namespaced as agent__<peer-id>. Route through Handle
	// on a fresh sub-session. NOTE: no e.toolTimeout wrap here — the sub-agent's
	// own RunTimeout (via its caller chain) bounds it. The parent's context
	// deadline also still applies.
	if strings.HasPrefix(call.Name, AgentToolPrefix) {
		return e.runAgentCall(ctx, def, call.Name, call.Arguments)
	}

	// Check built-in Go tools first (read_skill, read_skill_file, etc.)
	for _, b := range e.builtins {
		if b.Name != call.Name {
			continue
		}

		// Confirmation gate: pause and ask the user before executing tools
		// that are listed in def.ConfirmTools (or "*" for all built-ins).
		if err := e.maybeConfirm(ctx, def, call); err != nil {
			return "", err
		}

		tstart := time.Now()
		tctx, cancel := context.WithTimeout(ctx, e.toolTimeout)
		defer cancel()
		result, err := b.Handler(tctx, call.Arguments)

		// Audit log every built-in call.
		e.logAudit(ctx, def, call, result, tstart, false, err)

		return result, err
	}

	// Check system tools (SEC-3 partition). systemToolsFor returns the SAFE
	// (read-only) built-ins unconditionally, and the privileged SYSTEM
	// built-ins (shell_exec, run_script, install_library, write_file,
	// download_file) only when the server permits system tools AND the agent
	// holds the "system" capability. A privileged tool call from an agent
	// without the capability therefore falls through to "tool not defined".
	for _, b := range e.systemToolsFor(def) {
		if b.Name != call.Name {
			continue
		}

		// Deterministic path-based guardrail for privileged system tools
		if isPrivilegedSystemTool(b.Name) {
			action, reason, err := e.deterministicGuardrail(ctx, def, sessionID, call)
			if err != nil {
				return "", err
			}
			if action == GuardrailActionDeny {
				e.log.Warn("guardrail denied tool execution", zap.String("tool", call.Name), zap.String("reason", reason))
				return "", fmt.Errorf("guardrail denied execution: %s", reason)
			} else if action == GuardrailActionConfirm {
				if err := e.dynamicConfirm(ctx, def, call, reason); err != nil {
					return "", err
				}
			}
		}

		// Confirmation gate: pause and ask the user before executing tools
		// that are listed in def.ConfirmTools (or "*" for all built-ins).
		if err := e.maybeConfirm(ctx, def, call); err != nil {
			return "", err
		}

		tstart := time.Now()
		tctx, cancel := context.WithTimeout(ctx, e.toolTimeout)
		defer cancel()
		result, err := b.Handler(tctx, call.Arguments)

		// Audit log every built-in call.
		e.logAudit(ctx, def, call, result, tstart, false, err)

		return result, err
	}

	// Find the agent's Python tool definition
	var toolDef *agent.ToolDef
	for i := range def.Tools {
		if def.Tools[i].Name == call.Name {
			toolDef = &def.Tools[i]
			break
		}
	}
	if toolDef == nil {
		if isPrivilegedSystemTool(call.Name) {
			return "", fmt.Errorf("tool %q requires the 'system' capability in the agent's SOUL.yaml and server-level authorization (allow_system_agents)", call.Name)
		}
		return "", fmt.Errorf("tool %q not defined in agent %q", call.Name, def.ID)
	}

	// Serialize arguments to pass as JSON via stdin
	argsJSON, _ := json.Marshal(call.Arguments)

	// Build a tiny Python bootstrap that imports the tool file and calls the function
	var script string
	if toolDef.Inline != "" {
		script = toolDef.Inline
	} else if toolDef.PythonFile != "" {
		// Expand a leading ~ to the home directory — Python's importlib does NOT
		// do this, so an unexpanded "~/..." path would fail to load.
		pyFile := toolDef.PythonFile
		if strings.HasPrefix(pyFile, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				pyFile = filepath.Join(home, pyFile[2:])
			}
		}
		// Privilege boundary: reject paths outside the configured allowlist.
		// This prevents a crafted SOUL.yaml from executing arbitrary host files.
		// The check is skipped when AllowedToolDirs is empty (default single-user
		// mode where all SOUL.yaml authors are already trusted operators).
		if len(e.allowedToolDirs) > 0 {
			clean := filepath.Clean(pyFile)
			allowed := false
			for _, dir := range e.allowedToolDirs {
				prefix := filepath.Clean(dir) + string(filepath.Separator)
				if strings.HasPrefix(clean, prefix) || clean == filepath.Clean(dir) {
					allowed = true
					break
				}
			}
			if !allowed {
				return "", fmt.Errorf(
					"tool %q: python_file %q is outside the configured allowed_tool_dirs — "+
						"update runtime.allowed_tool_dirs in config.yaml to permit this path",
					call.Name, pyFile,
				)
			}
		}
		script = fmt.Sprintf(`
import sys as _sys, json, importlib.util
# Redirect stdout → stderr so any print() inside the tool code does not
# corrupt the JSON result we write at the very end.
_orig_stdout = _sys.stdout
_sys.stdout = _sys.stderr
args = json.loads(_sys.stdin.read())
spec = importlib.util.spec_from_file_location("tool", %q)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
result = getattr(mod, %q)(**args)
_sys.stdout = _orig_stdout
print(result if isinstance(result, str) else json.dumps(result))
`, pyFile, call.Name)
	} else {
		return "", fmt.Errorf("tool %q has neither python_file nor inline", call.Name)
	}

	// Per-tool timeout (toolDef.Timeout, e.g. "30m") overrides the global
	// runtime.tool_timeout. Useful for slow-by-design tools (notebooklm audio
	// generation, large data exports) so we don't have to weaken the global
	// safety net for every tool.
	timeout := e.toolTimeout
	if toolDef.Timeout != "" {
		if d, perr := time.ParseDuration(toolDef.Timeout); perr == nil && d > 0 {
			timeout = d
		} else {
			e.log.Warn("tool: invalid timeout, using global default",
				zap.String("tool", call.Name),
				zap.String("timeout", toolDef.Timeout),
			)
		}
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// PRODUCTION_AUDIT → F1 (2026-05-27): wrap the python invocation in
	// the soulacy __exec-sandbox subcommand to apply CPU/memory/FD/file
	// caps before execve. When sandboxing is disabled OR we couldn't
	// resolve our own binary path at boot, sandbox.Wrap returns the
	// original argv unchanged — the engine doesn't have to branch.
	//
	// SEC-5: carry the agent's declared env allowlist into the sandbox wrapper
	// (--env= flags) AND set cmd.Env directly so the non-sandboxed path is also
	// scrubbed. Either way the tool sees only BaseEnvAllowlist + def.Env, never
	// the gateway's full environment.
	limits := e.sandboxLimits
	limits.EnvAllow = def.Env
	argv := sandbox.Wrap(e.selfPath, limits, []string{e.pythonBin, "-c", script})
	cmd := exec.CommandContext(tctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(argsJSON)
	cmd.Env = sandbox.FilteredEnv(os.Environ(), def.Env)

	// Stream stderr line-by-line into the actionlog as `tool.log` events so
	// the GUI/CLI can see long-running tools make progress instead of
	// silence-until-completion. Python tools just need to write progress to
	// sys.stderr (with flush) — every line surfaces as one log row in the
	// trace. Stdout remains buffered (it's the tool's return value).
	// (Observed 2026-05-28: ai_daily_pipeline took 10+ min producing zero
	// log rows mid-run; you could watch the agent work in NotebookLM but
	// nothing reached the actionlog. Fixed by piping stderr through here.)
	//
	// On error path, we also keep the last few stderr lines for the LLM-
	// visible error message — same UX as before, just rebuilt from the
	// streamed lines instead of a buffer.
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("tool execution: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("tool execution: start: %w", err)
	}

	// Reader goroutine. Each non-empty stderr line becomes one tool.log
	// event keyed by the tool name + call ID, so the action-log timeline
	// shows the right ordering. Bounded buffer for the LLM-visible error
	// summary — we keep the last 32 lines, plenty for a stacktrace.
	const tailKeepLines = 32
	var tailMu sync.Mutex
	var tailLines []string
	pipeDone := make(chan struct{})
	go func() {
		defer close(pipeDone)
		sc := bufio.NewScanner(stderrPipe)
		// Long lines (full tracebacks) still get one event each. Cap at 64
		// KiB per line so a runaway tool can't memory-bomb us.
		sc.Buffer(make([]byte, 4096), 64*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			tailMu.Lock()
			tailLines = append(tailLines, line)
			if len(tailLines) > tailKeepLines {
				tailLines = tailLines[len(tailLines)-tailKeepLines:]
			}
			tailMu.Unlock()
			e.sink.Emit(message.Event{
				Type: "tool.log", AgentID: def.ID, SessionID: sessionID,
				Payload: map[string]any{
					"call_id": call.ID,
					"name":    call.Name,
					"line":    line,
				},
				Timestamp: time.Now().UTC(),
			})
		}
	}()

	// CRITICAL: drain the stderr pipe BEFORE calling cmd.Wait. Go's os/exec
	// closes the pipe inside Wait, which interrupts any in-progress reads.
	// We wait for the reader goroutine to see natural EOF (which happens
	// when the child process exits and the kernel closes the write end of
	// the pipe), then reap the process. This pattern is documented in the
	// os/exec docs explicitly: "it is incorrect to call Wait before all
	// reads from the pipe have completed." A run-tool first hand-wired
	// this in the wrong order on 2026-05-28 — symptom: zero tool.log
	// events emitted despite the python script flushing stderr correctly.
	<-pipeDone
	runErr := cmd.Wait()

	if runErr != nil {
		tailMu.Lock()
		errMsg := strings.TrimSpace(strings.Join(tailLines, "\n"))
		tailMu.Unlock()
		if errMsg == "" {
			errMsg = runErr.Error()
		}
		if len(errMsg) > 4000 {
			errMsg = errMsg[len(errMsg)-4000:]
		}
		return "", fmt.Errorf("tool execution failed (%v): %s", runErr, errMsg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// allToolSchemas combines the agent's Python tools with the engine's built-in
// Go tools. The skill built-ins (read_skill, read_skill_file) are only offered
// when the agent has opted into skills (def.Skills non-empty), so agents that
// don't use skills aren't tempted to call them.
//
// channel is the inbound message's Channel field ("http", "telegram", etc.).
// System tools (shell_exec, run_script, …) are only offered when ALL three
// conditions hold:
//  1. runtime.allow_system_tools = true  (server-level permit)
//  2. def.SystemTools = true             (per-agent opt-in)
//  3. channel == "http"                  (local web GUI only — never on bot channels)
func (e *Engine) allToolSchemas(def *agent.Definition, channel string) []llm.ToolSchema {
	schemas := make([]llm.ToolSchema, 0, len(def.Tools)+len(e.builtins))

	// Python tools defined in the agent's SOUL.yaml
	for _, t := range def.Tools {
		schemas = append(schemas, llm.ToolSchema{
			Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		})
	}

	// Built-in Go tools, gated by capability AND optionally by the agent's
	// `builtins:` allowlist:
	//   - def.Builtins == nil           → default gating only (back-compat)
	//   - def.Builtins == &[]           → NO built-ins (peer-only orchestrator)
	//   - def.Builtins == &[names…]     → only those names (still subject to gate)
	//   - def.Builtins == &["*"/"all"]  → same as nil (all gated built-ins)
	// Gates themselves (per BuiltinTool.Gate):
	//   - ""          always offered (e.g. web_search — provider-agnostic)
	//   - "skills"    only when the agent opted into skills (def.Skills)
	//   - "knowledge" only when the agent declared at least one KB (def.Knowledge)
	var allow map[string]bool
	wildcardBuiltins := def.Builtins == nil
	if def.Builtins != nil {
		allow = make(map[string]bool, len(*def.Builtins))
		for _, n := range *def.Builtins {
			if n == "*" || n == "all" {
				wildcardBuiltins = true
				continue
			}
			allow[n] = true
		}
	}
	for _, b := range e.builtins {
		// Allowlist filter first (cheap reject).
		if !wildcardBuiltins && !allow[b.Name] {
			continue
		}
		switch b.Gate {
		case "skills":
			if len(def.Skills) == 0 {
				continue
			}
		case "knowledge":
			if len(def.Knowledge) == 0 {
				continue
			}
		}
		schemas = append(schemas, llm.ToolSchema{
			Name: b.Name, Description: b.Description, Parameters: b.Parameters,
		})
	}

	// MCP tools from connected servers are offered according to the agent's
	// mcp_servers / mcp_tools allowlists. For backwards compatibility, agents
	// that omit both fields still see every connected MCP tool.
	if e.mcpClient != nil {
		for _, t := range e.mcpClient.AllTools() {
			if !mcpToolAllowed(def, t.FullName()) {
				continue
			}
			schemas = append(schemas, llm.ToolSchema{
				Name:        t.FullName(),
				Description: t.Description,
				Parameters:  t.InputSchema,
			})
		}
	}

	// Plugin tools from installed plugins (namespaced as plugin__<id>__<tool>).
	if e.pluginProvider != nil {
		for _, pt := range e.pluginProvider.AllTools() {
			schemas = append(schemas, llm.ToolSchema{
				Name:        pt.Name,
				Description: pt.Description,
				Parameters:  pt.Parameters,
			})
		}
	}

	// System tools (SEC-3 partition). Bot channels (telegram, discord, slack,
	// whatsapp) are ALWAYS excluded — only the local HTTP/web channel may use
	// OS-level built-ins, and this cannot be overridden by agent config alone.
	//
	// Within the http channel, systemToolsFor applies the SEC-3 gating:
	//   - SAFE (read-only) built-ins — read_file, list_dir, find_files,
	//     fetch_url, http_request, env_get, sys_info — are always offered.
	//   - SYSTEM (privileged) built-ins — shell_exec, run_script,
	//     install_library, write_file, download_file — are offered ONLY when
	//     the server permits (runtime.allow_system_tools) AND the agent
	//     declares the "system" capability (capabilities: [system], or the
	//     legacy system_tools: true alias).
	//
	// An explicit `builtins: []` (peer-only orchestrator) suppresses the
	// ambient SAFE system tools too — an agent that opted out of ALL Go-native
	// built-ins should not be handed read_file/list_dir/etc. behind its back.
	// A PRIVILEGED tool, by contrast, is only ever present when the agent made
	// a deliberate `capabilities: [system]` (or system_tools) grant, so it is
	// NOT suppressed by builtins: [] — the explicit privileged opt-in wins.
	// A named allowlist (`builtins: [read_file]`) admits only those names.
	suppressSafe := def.Builtins != nil && len(*def.Builtins) == 0
	if channel == "http" {
		for _, st := range e.systemToolsFor(def) {
			priv := isPrivilegedSystemTool(st.Name)
			if !priv {
				// SAFE tool: respect builtins: [] and any named allowlist.
				if suppressSafe || (!wildcardBuiltins && !allow[st.Name]) {
					continue
				}
			}
			schemas = append(schemas, llm.ToolSchema{
				Name:        st.Name,
				Description: st.Description,
				Parameters:  st.Parameters,
			})
		}
	}

	// Peer agents exposed as tools (namespaced as agent__<id>). Built
	// dynamically because each parent agent gets a DIFFERENT subset of peers
	// depending on its def.Agents list, so we can't preregister them in
	// e.builtins like the other tools.
	schemas = append(schemas, e.buildAgentCallSchemas(def)...)

	return schemas
}

// mcpToolAllowed reports whether an agent may see/call a namespaced MCP tool.
//
// Backwards-compatible default: when both mcp_servers and mcp_tools are absent,
// all MCP tools remain available. Once either field is present, MCP becomes
// deny-by-default and a tool must match either the server allowlist or the full
// tool-name allowlist. A present empty list is therefore an intentional "none".
func mcpToolAllowed(def *agent.Definition, fullName string) bool {
	if def == nil {
		return false
	}
	if def.MCPServers == nil && def.MCPTools == nil {
		return true
	}
	serverID, ok := mcpServerFromFullName(fullName)
	if !ok {
		return false
	}
	if allowMCPServer(def.MCPServers, serverID) {
		return true
	}
	return allowMCPTool(def.MCPTools, fullName)
}

func mcpServerFromFullName(fullName string) (string, bool) {
	if !strings.HasPrefix(fullName, mcp.FullNamePrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(fullName, mcp.FullNamePrefix)
	parts := strings.SplitN(rest, "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0], true
}

func allowMCPServer(allowlist *[]string, serverID string) bool {
	if allowlist == nil {
		return false
	}
	for _, allowed := range *allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || allowed == "all" {
			return true
		}
		if sanitizeMCPID(allowed) == serverID {
			return true
		}
	}
	return false
}

func allowMCPTool(allowlist *[]string, fullName string) bool {
	if allowlist == nil {
		return false
	}
	for _, allowed := range *allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || allowed == "all" {
			return true
		}
		if allowed == fullName {
			return true
		}
	}
	return false
}

func sanitizeMCPID(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, s)
}

// --- Multi-agent (agent-as-tool) ----------------------------------------------
//
// An agent whose SOUL.yaml lists `agents: [other-id, ...]` can invoke each of
// those peers as a tool named `agent__<id>`. The tool's `message` argument is
// delivered to the peer as the inbound user message; the peer runs its own
// loop (including its own tool/skill/KB usage) and its final reply text is
// returned to the caller as the tool result.

// AgentToolPrefix namespaces peer-agent tools so they're unmistakable in the
// tool catalog and tool-call routing.
const AgentToolPrefix = "agent__"

// maxAgentCallDepth caps the recursion chain. Each engine.Handle call invoked
// from a tool handler bumps the depth carried via context.Value; exceeding the
// cap returns an error so a runaway A → B → A loop fails cleanly.
const maxAgentCallDepth = 5

type agentCallDepthKey struct{}

func agentCallDepth(ctx context.Context) int {
	if v, ok := ctx.Value(agentCallDepthKey{}).(int); ok {
		return v
	}
	return 0
}

func withAgentCallDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, agentCallDepthKey{}, d)
}

// chainDeadlineKey carries a wall-clock deadline for the entire nested-agent
// chain. It is stamped once at depth 0 (the first agent call from a tool
// handler) and propagated unchanged through every sub-agent invocation.
//
// Without this, a 5-deep chain of agents each with a 5-minute run_timeout
// could hang the gateway for up to 25 minutes before any cancellation fires.
// With it, the whole fan-out is bounded to a single run_timeout budget.
type chainDeadlineKey struct{}

// withChainDeadline stamps deadline into ctx if no chain deadline exists yet
// (depth 0 case). At depth > 0 the existing deadline is preserved unchanged so
// the budget is not reset on every recursive call.
func withChainDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if _, already := ctx.Value(chainDeadlineKey{}).(time.Time); already {
		// Deadline already set by an ancestor — inherit it unchanged.
		return ctx, func() {}
	}
	ctx = context.WithValue(ctx, chainDeadlineKey{}, deadline)
	return context.WithDeadline(ctx, deadline)
}

// resolveAgentRefs expands the SOUL.yaml `agents:` list into actual peer
// Definitions. Supports "*" / "all" wildcards. Always excludes the caller
// itself — agents can't accidentally invoke themselves through a wildcard,
// which would otherwise be a single-step infinite loop. (Explicit self-call
// via a literal ID is also blocked here; require an explicit `agent__self`
// alias later if that pattern proves useful.)
func (e *Engine) resolveAgentRefs(refs []string, callerID string) []*agent.Definition {
	if len(refs) == 0 {
		return nil
	}
	wantAll := false
	for _, r := range refs {
		if r == "*" || r == "all" {
			wantAll = true
			break
		}
	}
	if wantAll {
		all := e.loader.All()
		out := make([]*agent.Definition, 0, len(all))
		for _, d := range all {
			if d.ID == callerID || !d.Enabled || e.loader.IsBuiltin(d.ID) {
				continue // exclude self, disabled agents, and built-ins from wildcards
			}
			out = append(out, d)
		}
		return out
	}
	out := make([]*agent.Definition, 0, len(refs))
	for _, r := range refs {
		if r == callerID {
			continue // can't call self
		}
		if d := e.loader.Get(r); d != nil {
			out = append(out, d)
		}
	}
	return out
}

// buildAgentCallSchemas produces one ToolSchema per declared peer. Names are
// `agent__<peer-id>`, descriptions are pulled from the peer's SOUL.yaml so
// the LLM can pick the right peer based on what each one does.
func (e *Engine) buildAgentCallSchemas(def *agent.Definition) []llm.ToolSchema {
	peers := e.resolveAgentRefs(def.Agents, def.ID)
	if len(peers) == 0 {
		return nil
	}
	out := make([]llm.ToolSchema, 0, len(peers))
	for _, p := range peers {
		desc := strings.TrimSpace(p.Description)
		if desc == "" {
			desc = "(no description provided)"
		}
		out = append(out, llm.ToolSchema{
			Name:        AgentToolPrefix + p.ID,
			Description: fmt.Sprintf("Delegate a sub-task to the %q agent. %s", p.ID, desc),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "The instruction, question, or task to send to this agent. Be specific and self-contained — the agent has no shared context with you.",
					},
				},
				"required": []string{"message"},
			},
		})
	}
	return out
}

// dispatchRouter handles Kind=="router" agents — see docs/CHANNEL_DESIGN.md
// Q2. The router has no LLM loop: it inspects the inbound text, picks the
// first matching route, and dispatches to the named peer agent via the
// existing peer-call path. The peer's reply becomes the router's reply.
//
// Matching:
//  1. Routes are tried in declaration order; the first match wins.
//  2. Each route has at most one effective match clause — Regex,
//     Prefix, or any-of Contains. A route with NONE of those clauses
//     is the "else" fallback (must be last if present).
//  3. Match is case-insensitive for Prefix and Contains. Regex follows
//     Go's regexp semantics (use `(?i)…` for case-insensitive patterns).
//  4. If no route matches and no fallback exists, the router emits an
//     error event and returns an empty reply.
//
// Authorization: the Target must be in the router's declared peer list
// (def.Agents). runAgentCall enforces this independently, so a router
// can't manufacture a forbidden Target via a bad config — but we also
// pre-validate here to produce a clearer error event.
//
// Trace: emits a `router.match` event recording which route matched
// (and what text triggered it) so misclassifications are debuggable.
func (e *Engine) dispatchRouter(ctx context.Context, def *agent.Definition, msg message.Message) (message.Message, error) {
	text := flattenParts(msg.Parts)
	matchIdx, matched := pickRouterRoute(def.Routes, text)
	if !matched {
		errMsg := fmt.Sprintf("engine: router %q has no matching route for inbound text and no fallback configured", def.ID)
		e.sink.Emit(message.Event{
			Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{
				"message":     errMsg,
				"reason":      "router_no_match",
				"text_prefix": truncate(text, 200),
			},
			Timestamp: time.Now().UTC(),
		})
		return message.Message{}, fmt.Errorf("%s", errMsg)
	}
	route := def.Routes[matchIdx]

	e.sink.Emit(message.Event{
		Type: "router.match", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{
			"router":      def.ID,
			"target":      route.Target,
			"match_index": matchIdx,
			"match_kind":  routeMatchKind(route),
		},
		Timestamp: time.Now().UTC(),
	})

	// Reuse the existing peer-call path for the actual dispatch. This
	// gives us — for free — the peer-list authorization check
	// (runAgentCall.allowed) and the depth-limit guard.
	args := map[string]any{"message": text}
	peerReply, err := e.runAgentCall(ctx, def, AgentToolPrefix+route.Target, args)
	if err != nil {
		return message.Message{}, err
	}

	return message.Message{
		ID:        msg.ID,
		SessionID: msg.SessionID,
		AgentID:   def.ID,
		Channel:   msg.Channel,
		ThreadID:  msg.ThreadID,
		UserID:    msg.UserID,
		Username:  msg.Username,
		Role:      message.RoleAssistant,
		Parts:     message.Text(peerReply),
		CreatedAt: time.Now().UTC(),
	}, nil
}

// pickRouterRoute returns the index of the first matching route, or
// (-1, false) if no route matches and no fallback exists. A "fallback"
// route is one with no Regex, no Prefix, and no Contains — pure else.
// Pre-compiles regexes per call; cheap enough at typical route counts.
// For agents with hundreds of routes this could be cached at load time,
// but that's premature optimisation given typical router shapes (3-10
// routes).
func pickRouterRoute(routes []agent.RouterRoute, text string) (int, bool) {
	lowText := strings.ToLower(text)
	for i, r := range routes {
		switch {
		case r.Regex != "":
			re, err := regexp.Compile(r.Regex)
			if err != nil {
				continue // skip malformed; runtime warning is too noisy here
			}
			if re.MatchString(text) {
				return i, true
			}
		case r.Prefix != "":
			if strings.HasPrefix(lowText, strings.ToLower(r.Prefix)) {
				return i, true
			}
		case len(r.Contains) > 0:
			for _, sub := range r.Contains {
				if sub == "" {
					continue
				}
				if strings.Contains(lowText, strings.ToLower(sub)) {
					return i, true
				}
			}
		default:
			// No match clauses → this is the else fallback. Always wins.
			return i, true
		}
	}
	return -1, false
}

func routeMatchKind(r agent.RouterRoute) string {
	switch {
	case r.Regex != "":
		return "regex"
	case r.Prefix != "":
		return "prefix"
	case len(r.Contains) > 0:
		return "contains"
	default:
		return "fallback"
	}
}

// runAgentCall is the dispatcher invoked when an LLM emits a tool call whose
// name starts with AgentToolPrefix. It validates the peer reference, enforces
// the depth limit, and recurses into engine.Handle with a fresh session.
func (e *Engine) runAgentCall(ctx context.Context, callerDef *agent.Definition, fullToolName string, args map[string]any) (string, error) {
	targetID := strings.TrimPrefix(fullToolName, AgentToolPrefix)
	if targetID == "" || targetID == fullToolName {
		return "", fmt.Errorf("agent call: malformed tool name %q", fullToolName)
	}

	// Authorisation: the LLM is only allowed to invoke peers the parent
	// explicitly declared in its SOUL.yaml `agents:` list. We don't trust the
	// model not to manufacture an `agent__some-other-id` tool name.
	allowed := false
	for _, p := range e.resolveAgentRefs(callerDef.Agents, callerDef.ID) {
		if p.ID == targetID {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("agent call: %q is not in this agent's declared peer list", targetID)
	}

	// Depth limit — bounds A → B → A → … recursion chains.
	depth := agentCallDepth(ctx)
	if depth >= maxAgentCallDepth {
		return "", fmt.Errorf("agent call depth limit (%d) exceeded calling %q — possible infinite loop", maxAgentCallDepth, targetID)
	}

	target := e.loader.Get(targetID)
	if target == nil {
		return "", fmt.Errorf("agent call: %q not loaded", targetID)
	}
	if !target.Enabled {
		return "", fmt.Errorf("agent call: %q is disabled", targetID)
	}

	msg := strings.TrimSpace(argString(args, "message"))
	if msg == "" {
		return "", fmt.Errorf("agent call: message is required")
	}

	e.log.Info("agent call",
		zap.String("caller", callerDef.ID),
		zap.String("target", targetID),
		zap.Int("depth", depth+1),
	)

	// Wall-clock chain budget: at depth 0 (first sub-agent call) stamp a
	// deadline equal to the CALLER's run_timeout so the entire nested chain
	// is bounded to one timeout budget, not one per depth level.
	// withChainDeadline is a no-op at depth > 0 — the ancestor deadline is
	// preserved unchanged through all recursive calls.
	chainTimeout := e.toolTimeout // fallback if agent has no run_timeout
	if callerDef.RunTimeout != "" {
		if d, err := time.ParseDuration(callerDef.RunTimeout); err == nil && d > 0 {
			chainTimeout = d
		}
	}
	subCtx, chainCancel := withChainDeadline(ctx, time.Now().Add(chainTimeout))
	defer chainCancel()

	subCtx = withAgentCallDepth(subCtx, depth+1)
	reply, err := e.Handle(subCtx, message.Message{
		AgentID:   targetID,
		SessionID: "agent-call-" + uuidShort(),
		Channel:   "internal",
		Username:  "agent:" + callerDef.ID,
		Parts:     message.Text(msg),
	})
	if err != nil {
		return "", fmt.Errorf("agent call %q: %w", targetID, err)
	}
	return flattenParts(reply.Parts), nil
}

// uuidShort returns a fresh UUIDv4 string. Used for sub-agent session IDs
// and synthetic tool-call IDs in the auto-delegate path. Was time-based
// (`time.Now().UnixNano()`) — two parallel agent calls on the same tick
// could collide on the same session id and end up sharing a Session struct
// via `e.sessions`, bleeding history across them.
// (PRODUCTION_AUDIT → MEDIUM/Engine)
func uuidShort() string {
	return uuid.New().String()
}

// providerIsOllama reports whether the agent resolves to the Ollama provider
// (its configured provider, or the gateway default when unset).
func (e *Engine) providerIsOllama(def *agent.Definition) bool {
	p := def.LLM.Provider
	if p == "" {
		p = e.llmRouter.DefaultProvider()
	}
	return p == "ollama"
}

// providerAllowed enforces the SOUL.yaml `llm.allowed_providers` guard.
// Returns true when the configured provider is permitted (the list is
// empty/nil → no restriction, or the provider name is on the list).
//
// Closes the "I clicked the wrong provider in the GUI dropdown and burned
// paid-API credit" failure mode: an agent that declares
// `llm.allowed_providers: [ollama]` is bricked the moment someone tries
// to point it at Anthropic / OpenAI / Gemini — surfaced as a clear error
// in the trace instead of a downstream HTTP 400 / 401 / 402 that looks
// like the model's fault.
//
// Package-level (not a method on Engine) because it has no engine state
// dependencies and so the unit test can exercise it without spinning up
// an LLM router or loader.
func providerAllowed(allowlist []string, provider string) bool {
	if len(allowlist) == 0 {
		return true
	}
	for _, name := range allowlist {
		if name == provider {
			return true
		}
	}
	return false
}

func applyPlaygroundOverrides(def *agent.Definition, meta map[string]string) {
	if def == nil || len(meta) == 0 {
		return
	}
	if v := strings.TrimSpace(meta["playground.llm.provider"]); v != "" {
		def.LLM.Provider = v
	}
	if v := strings.TrimSpace(meta["playground.llm.model"]); v != "" {
		def.LLM.Model = v
	}
	if v := strings.TrimSpace(meta["playground.llm.temperature"]); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			def.LLM.Temperature = f
		}
	}
	if v := strings.TrimSpace(meta["playground.llm.max_tokens"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			def.LLM.MaxTokens = n
		}
	}
	if v := strings.TrimSpace(meta["playground.max_turns"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			def.MaxTurns = n
		}
	}
	if v := strings.TrimSpace(meta["playground.llm.tool_choice"]); v != "" {
		def.LLM.ToolChoice = v
	}
}

// skillCatalogFor builds the <available_skills> catalog for the named skills.
// names may contain "*" or "all" to include every installed skill.
func (e *Engine) skillCatalogFor(names []string) string {
	if e.skillLoader == nil {
		return ""
	}
	var skills []*skill.Skill
	all := false
	for _, n := range names {
		if n == "*" || n == "all" {
			all = true
			break
		}
	}
	if all {
		skills = e.skillLoader.All()
	} else {
		for _, n := range names {
			if s := e.skillLoader.Get(n); s != nil {
				skills = append(skills, s)
			}
		}
	}
	if len(skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<available_skills>\n")
	for _, s := range skills {
		sb.WriteString("  <skill>\n")
		sb.WriteString(fmt.Sprintf("    <name>%s</name>\n", s.Name))
		sb.WriteString(fmt.Sprintf("    <description>%s</description>\n", s.Description))
		sb.WriteString("  </skill>\n")
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

// knowledgeCatalogFor builds an XML-ish catalog of the named knowledge bases
// for injection into the system prompt. Unknown names are silently dropped —
// the agent's SOUL.yaml may reference a KB that hasn't been created yet, and
// we don't want that to brick the agent.
func (e *Engine) knowledgeCatalogFor(names []string) string {
	if e.knowledge == nil {
		return ""
	}
	summaries := e.knowledge.ListAvailable(names)
	if len(summaries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<knowledge_bases>\n")
	for _, kb := range summaries {
		sb.WriteString("  <kb>\n")
		sb.WriteString(fmt.Sprintf("    <name>%s</name>\n", kb.Name))
		if kb.Description != "" {
			sb.WriteString(fmt.Sprintf("    <description>%s</description>\n", kb.Description))
		}
		sb.WriteString(fmt.Sprintf("    <documents>%d</documents>\n", kb.DocCount))
		sb.WriteString(fmt.Sprintf("    <chunks>%d</chunks>\n", kb.ChunkCount))
		sb.WriteString("  </kb>\n")
	}
	sb.WriteString("</knowledge_bases>")
	return sb.String()
}

// agentCatalogFor builds an XML-ish catalog of the peer agents this caller
// declared in its SOUL.yaml. Unknown IDs and self-references are silently
// dropped (resolveAgentRefs handles both).
func (e *Engine) agentCatalogFor(def *agent.Definition) string {
	peers := e.resolveAgentRefs(def.Agents, def.ID)
	if len(peers) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<available_agents>\n")
	for _, p := range peers {
		sb.WriteString("  <agent>\n")
		sb.WriteString(fmt.Sprintf("    <id>%s</id>\n", p.ID))
		if p.Name != "" && p.Name != p.ID {
			sb.WriteString(fmt.Sprintf("    <name>%s</name>\n", p.Name))
		}
		if d := strings.TrimSpace(p.Description); d != "" {
			sb.WriteString(fmt.Sprintf("    <description>%s</description>\n", d))
		}
		sb.WriteString("  </agent>\n")
	}
	sb.WriteString("</available_agents>")
	return sb.String()
}

// skillNamesCSV returns a comma-separated list of all installed skill names,
// used to help the model self-correct when it calls read_skill with a bad name.
func (e *Engine) skillNamesCSV() string {
	if e.skillLoader == nil {
		return "(none)"
	}
	all := e.skillLoader.All()
	if len(all) == 0 {
		return "(none)"
	}
	names := make([]string, len(all))
	for i, s := range all {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

// ── Memory accessors (called by the gateway API handlers) ────────────────────

// MemoryList returns up to limit archived entries for an agent, newest first.
func (e *Engine) MemoryList(agentID string, limit int) ([]memory.Entry, error) {
	if e.archive == nil {
		return []memory.Entry{}, nil
	}
	if limit <= 0 {
		limit = 200
	}
	entries, err := e.archive.ReadGlobal(agentID, limit)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []memory.Entry{}
	}
	return entries, nil
}

// MemorySearch performs a substring search over an agent's archived memories.
// If query is empty it falls back to MemoryList.
func (e *Engine) MemorySearch(agentID, query string, limit int) ([]memory.Entry, error) {
	if e.archive == nil {
		return []memory.Entry{}, nil
	}
	if limit <= 0 {
		limit = 200
	}
	if query == "" {
		return e.MemoryList(agentID, limit)
	}
	entries, err := e.archive.Search(agentID, query, limit)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []memory.Entry{}
	}
	return entries, nil
}

// MemoryPurgeSession removes hot-memory entries for a specific session.
func (e *Engine) MemoryPurgeSession(sessionID string) error {
	return e.memory.PurgeSession(sessionID)
}

func flattenParts(parts []message.Part) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == message.ContentText {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

const (
	GuardrailActionSafe    = "SAFE"
	GuardrailActionConfirm = "CONFIRM"
	GuardrailActionDeny    = "DENY"
)

// isPathSafe determines if a given target path is within the agent's safe sandbox
// (e.g. /tmp or the engine's active data directory).
func isPathSafe(targetPath string, dataDir string) bool {
	if targetPath == "" {
		return false
	}
	cleanPath := filepath.Clean(targetPath)
	
	// Always allow /tmp
	if strings.HasPrefix(cleanPath, "/tmp/") || cleanPath == "/tmp" {
		return true
	}
	
	// Allow if within the designated DataDir
	if dataDir != "" {
		cleanDataDir := filepath.Clean(dataDir)
		if strings.HasPrefix(cleanPath, cleanDataDir+"/") || cleanPath == cleanDataDir {
			return true
		}
	}
	
	return false
}

// deterministicGuardrail enforces a static, rules-based security boundary for privileged tools.
// It relies on path isolation (sandbox) rather than LLM intent classification, resulting
// in faster execution, zero hallucination risk, and predictable user prompts.
func (e *Engine) deterministicGuardrail(ctx context.Context, def *agent.Definition, sessionID string, call message.ToolCall) (string, string, error) {
	ws, _ := os.Getwd()

	switch call.Name {
	case "write_file", "replace_file_content", "download_file":
		// Find the target path in the arguments
		var targetPath string
		if p, ok := call.Arguments["path"].(string); ok {
			targetPath = p
		} else if p, ok := call.Arguments["target_file"].(string); ok {
			targetPath = p
		} else if p, ok := call.Arguments["destination"].(string); ok {
			targetPath = p
		}

		if targetPath != "" && isPathSafe(targetPath, ws) {
			return GuardrailActionSafe, "", nil
		}
		return GuardrailActionConfirm, fmt.Sprintf("Writing to file outside workspace: %s", targetPath), nil

	case "run_script":
		var targetPath string
		if p, ok := call.Arguments["path"].(string); ok {
			targetPath = p
		}

		if targetPath != "" && isPathSafe(targetPath, ws) {
			return GuardrailActionSafe, "", nil
		}
		return GuardrailActionConfirm, fmt.Sprintf("Executing script outside workspace: %s", targetPath), nil

	case "install_library":
		// Installing global/environment packages always requires confirmation
		return GuardrailActionConfirm, "Installing environment libraries requires confirmation.", nil

	case "shell_exec":
		// Arbitrary shell commands are too risky to blindly allow without a strict whitelist.
		// Always prompt the user for confirmation.
		var cmd string
		if c, ok := call.Arguments["command"].(string); ok {
			cmd = c
		}
		return GuardrailActionConfirm, fmt.Sprintf("Executing arbitrary shell command: %s", cmd), nil

	default:
		// Any other privileged tool defaults to CONFIRM
		return GuardrailActionConfirm, fmt.Sprintf("Privileged system action requires confirmation: %s", call.Name), nil
	}
}
