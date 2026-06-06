// main.go — Soulacy gateway server entry point.
// This binary starts the gateway, loads agents, starts all channel adapters,
// and begins the scheduler. It is designed to run as a system service.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/audit"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/builder"
	"github.com/soulacy/soulacy/internal/channels"
	discordchan "github.com/soulacy/soulacy/internal/channels/discord"
	httpchan "github.com/soulacy/soulacy/internal/channels/http"
	slackchan "github.com/soulacy/soulacy/internal/channels/slack"
	telegramchan "github.com/soulacy/soulacy/internal/channels/telegram"
	wachan "github.com/soulacy/soulacy/internal/channels/whatsapp"
	wawebchan "github.com/soulacy/soulacy/internal/channels/whatsappweb"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/events"
	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/executor/pool"
	"github.com/soulacy/soulacy/internal/executor/process"
	"github.com/soulacy/soulacy/internal/gateway"
	"github.com/soulacy/soulacy/internal/hooks"
	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/plugins"
	"github.com/soulacy/soulacy/internal/queue"
	"github.com/soulacy/soulacy/internal/queue/dlq"
	queuememory "github.com/soulacy/soulacy/internal/queue/memory"
	queuenats "github.com/soulacy/soulacy/internal/queue/nats"
	"github.com/soulacy/soulacy/internal/ratelimit"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/internal/skills"
	"github.com/soulacy/soulacy/internal/storage"
	storagepg "github.com/soulacy/soulacy/internal/storage/postgres"
	storagesqlite "github.com/soulacy/soulacy/internal/storage/sqlite"
	"github.com/soulacy/soulacy/internal/telemetry"
	"github.com/soulacy/soulacy/internal/vector"
	vectorqdrant "github.com/soulacy/soulacy/internal/vector/qdrant"
	vectorsqlitevec "github.com/soulacy/soulacy/internal/vector/sqlitevec"
	"github.com/soulacy/soulacy/internal/workboard"
)

// llmEmbedAdapter wraps an llm.Embedder so it satisfies memory.Embedder.
// The llm.Embedder interface takes a model name and a slice of texts; memory
// only needs one text at a time and a fixed model baked in at construction.
type llmEmbedAdapter struct {
	inner llm.Embedder
	model string
}

func (a *llmEmbedAdapter) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := a.inner.Embed(ctx, a.model, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedder returned no vectors")
	}
	return vecs[0], nil
}

// pluginToolAdapter bridges *plugins.Loader → runtime.PluginToolProvider.
// Converts plugin.ToolSpec → runtime.PluginTool so the engine package doesn't
// need to import the plugins package (which would create a cycle).
type pluginToolAdapter struct{ loader *plugins.Loader }

func (a *pluginToolAdapter) AllTools() []runtime.PluginTool {
	specs := a.loader.AllTools()
	out := make([]runtime.PluginTool, 0, len(specs))
	for _, s := range specs {
		out = append(out, runtime.PluginTool{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.Parameters,
			Handler:     s.Handler,
		})
	}
	return out
}

// Ensure the adapter satisfies the interface at compile time.
var _ runtime.PluginToolProvider = (*pluginToolAdapter)(nil)

// engineTracerAdapter bridges telemetry.Tracer → runtime.telemetryTracer.
// Both use the same Start(ctx, name, ...string) signature so this is a thin
// wrapper that satisfies the runtime package's local interface without
// requiring runtime to import the telemetry package.
type engineTracerAdapter struct{ t telemetry.Tracer }

func (a *engineTracerAdapter) Start(ctx context.Context, name string, kv ...string) (context.Context, interface{ End() }) {
	newCtx, span := a.t.Start(ctx, name, kv...)
	return newCtx, span
}

// engineDLQAdapter bridges dlq.Store → runtime.deadLetterStore.
// Translates the engine's PushFailed(queue, payload, errMsg) call into
// the dlq.Store Push(DeadLetter) API.
type engineDLQAdapter struct{ s dlq.Store }

func (a *engineDLQAdapter) PushFailed(ctx context.Context, queue string, payload []byte, errMsg string) error {
	return a.s.Push(ctx, dlq.DeadLetter{
		ID:       dlq.NewID(),
		Queue:    queue,
		Payload:  payload,
		ErrorMsg: errMsg,
		Attempts: 1,
	})
}

// engineCostStoreAdapter bridges *costs.Store → runtime.agentCostStore.
// The runtime interface Record method takes individual fields; costs.Store
// takes a UsageRecord struct. This adapter converts between them.
type engineCostStoreAdapter struct{ s *costs.Store }

func (a *engineCostStoreAdapter) Record(ctx context.Context,
	agentID, sessionID, provider, model string,
	promptTokens, compTokens, totalTokens int,
	costUSD float64,
) error {
	return a.s.Record(ctx, costs.UsageRecord{
		AgentID:      agentID,
		SessionID:    sessionID,
		Provider:     provider,
		Model:        model,
		PromptTokens: promptTokens,
		CompTokens:   compTokens,
		TotalTokens:  totalTokens,
		CostUSD:      costUSD,
	})
}

func main() {
	// PRODUCTION_AUDIT → F1 (2026-05-27): sandbox subcommand intercept.
	// When the engine re-execs us as a sandbox wrapper (soulacy
	// __exec-sandbox …), we set rlimits and execve straight away —
	// without paying for config load, watcher setup, etc. The "if argv[1]
	// is the sentinel" check is intentionally before anything else.
	if sandbox.IsSandboxInvocation(os.Args) {
		sandbox.RunSandboxedAndExit(os.Args)
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "soulacy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// ── Config ──────────────────────────────────────────────────────────────
	cfgPath := os.Getenv("SOULACY_CONFIG_PATH")
	cfg, resolvedPath, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfgPath == "" {
		cfgPath = resolvedPath
	}
	if err := config.EnsureDirs(cfg); err != nil {
		return fmt.Errorf("ensure dirs: %w", err)
	}

	// ── Security guardrail ──────────────────────────────────────────────────
	// Behaviour change: was hard-block, now warn-only.
	//
	// Previously, starting with a non-loopback host and no API key caused the
	// process to exit immediately with a fatal error. This turned out to be too
	// aggressive for a common and legitimate deployment pattern: a VPS that
	// binds on 0.0.0.0 with nginx/Caddy/Traefik sitting in front, enforcing
	// TLS and authentication at the proxy layer. Those operators were forced to
	// set a meaningless api_key (e.g. "disabled") just to get past the check,
	// which is arguably worse than the warning.
	//
	// The current behaviour prints a prominent stderr warning but allows startup
	// to continue. The warning is intentionally noisy (emoji, multi-line) so it
	// is visible in service manager logs and cannot be easily missed.
	if cfg.Server.APIKey == "" && !isLoopbackHost(cfg.Server.Host) {
		fmt.Fprintf(os.Stderr,
			"\n⚠  SECURITY WARNING: server.host=%q is a non-loopback address with no server.api_key.\n"+
				"   All API endpoints are UNAUTHENTICATED. Set server.api_key in config.yaml\n"+
				"   unless a reverse proxy is enforcing authentication upstream.\n\n",
			cfg.Server.Host,
		)
	}

	// ── Logger ───────────────────────────────────────────────────────────────
	log, err := buildLogger(cfg.Log)
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	defer log.Sync() //nolint:errcheck

	log.Info("Soulacy starting", zap.String("version", config.Version))

	// ── Agent brain memory (episodic / semantic / procedural) ────────────────
	// MEM-02: instantiate the CompositeStore. Base dir reads from the
	// SOULACY_MEMORY_DIR env var (MEM-07), falling back to ~/.soulacy/memory/.
	brainMemDir := os.Getenv("SOULACY_MEMORY_DIR")
	if brainMemDir == "" {
		home, _ := os.UserHomeDir()
		brainMemDir = filepath.Join(home, ".soulacy", "memory")
	}
	if err := os.MkdirAll(brainMemDir, 0755); err != nil {
		log.Warn("brain memory dir create failed — long-term memory disabled",
			zap.String("dir", brainMemDir), zap.Error(err))
		brainMemDir = ""
	}
	var brainStore *agentmemory.CompositeStore
	if brainMemDir != "" {
		brainStore = agentmemory.NewCompositeStore(brainMemDir, nil)
		log.Info("agent brain memory enabled", zap.String("dir", brainMemDir))
	}

	// ── Memory ───────────────────────────────────────────────────────────────
	fileStore, err := memory.NewFileStore(cfg.Memory.Dir)
	if err != nil {
		return fmt.Errorf("file store: %w", err)
	}

	archive, err := memory.NewSQLiteArchive(cfg.Memory.SQLitePath)
	if err != nil {
		return fmt.Errorf("sqlite archive: %w", err)
	}
	defer archive.Close()

	// ── Storage backend (action log + memory archive) ─────────────────────
	// Default: SQLite wrappers around the existing types (zero new deps).
	// Optional: Postgres via pgx/v5 when storage.backend = "postgres".
	home, _ := os.UserHomeDir()
	var (
		actionBackend storage.ActionLogBackend
		memBackend    storage.MemoryBackend
	)
	switch cfg.Storage.Backend {
	case "postgres":
		pgLogDir := cfg.Storage.PostgresLogDir
		if pgLogDir == "" {
			pgLogDir = filepath.Join(home, ".soulacy", "logs")
		}
		pgAL, pgMem, _, pgErr := storagepg.Open(cfg.Storage.PostgresDSN, pgLogDir, log)
		if pgErr != nil {
			return fmt.Errorf("postgres storage backend: %w", pgErr)
		}
		// pgAL.Close() flushes the async queue and closes the shared pgx pool.
		defer pgAL.Close()
		actionBackend = pgAL
		memBackend = pgMem
		log.Info("storage backend: postgres", zap.String("log_dir", pgLogDir))
	default: // "sqlite" or empty
		logsDir := filepath.Join(home, ".soulacy", "logs")
		actionsDB := filepath.Join(home, ".soulacy", "actions.db")
		sqAL, sqErr := actionlog.New(logsDir, actionsDB, log)
		if sqErr != nil {
			return fmt.Errorf("sqlite action log: %w", sqErr)
		}
		defer sqAL.Close()
		actionBackend = storagesqlite.NewActionLog(sqAL)
		memBackend = storagesqlite.NewMemoryArchive(archive)
		log.Info("storage backend: sqlite", zap.String("dir", logsDir))
	}

	// ── LLM Router ───────────────────────────────────────────────────────────
	llmRouter := llm.NewRouter(cfg.LLM.DefaultProvider)

	// Register Ollama (default, always available)
	ollamaCfg := cfg.LLM.Providers["ollama"]
	model := ollamaCfg.Model
	if model == "" {
		model = "llama3"
	}
	llmRouter.Register(llm.NewOllamaProvider(ollamaCfg.BaseURL, model, ollamaCfg.KeepAlive, ollamaCfg.Options))

	// Register OpenAI (also serves OpenRouter / Together / Groq / vLLM / any
	// OpenAI-compatible endpoint — set base_url accordingly).
	if openaiCfg, ok := cfg.LLM.Providers["openai"]; ok && openaiCfg.APIKey != "" {
		baseURL := openaiCfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		llmRouter.Register(llm.NewOpenAIProviderWithOptions("openai", baseURL, openaiCfg.APIKey, openaiCfg.Model, openaiCfg.Organization, openaiCfg.ParallelToolCalls))
	}
	// Register Anthropic (native Messages API).
	if anthropicCfg, ok := cfg.LLM.Providers["anthropic"]; ok && anthropicCfg.APIKey != "" {
		llmRouter.Register(llm.NewAnthropicProviderWithOptions(
			anthropicCfg.BaseURL, anthropicCfg.APIKey, anthropicCfg.Model,
			anthropicCfg.PromptCaching, anthropicCfg.ExtendedThinking, anthropicCfg.ThinkingBudget,
		))
	}
	// Register Google Gemini.
	if googleCfg, ok := cfg.LLM.Providers["google"]; ok && googleCfg.APIKey != "" {
		llmRouter.Register(llm.NewGeminiProviderWithOptions(googleCfg.BaseURL, googleCfg.APIKey, googleCfg.Model, googleCfg.ThinkingBudget, googleCfg.SafetyLevel))
	}
	// Additional OpenAI-compatible providers configured by URL — anything else
	// in cfg.LLM.Providers with a base_url and api_key gets a generic OpenAI
	// adapter. Lets users add OpenRouter / Together / Groq / vLLM under their
	// own id without code changes (e.g. "openrouter": { base_url: "https://...", api_key: "..." }).
	for id, pcfg := range cfg.LLM.Providers {
		switch id {
		case "ollama", "openai", "anthropic", "google":
			continue
		}
		if pcfg.APIKey != "" && pcfg.BaseURL != "" {
			llmRouter.Register(llm.NewOpenAIProviderWithOptions(id, pcfg.BaseURL, pcfg.APIKey, pcfg.Model, pcfg.Organization, pcfg.ParallelToolCalls))
		}
	}
	log.Info("llm providers registered",
		zap.Strings("ids", llmRouter.ProviderIDs()),
		zap.String("default", llmRouter.DefaultProvider()),
	)

	// ── Agent Loader ─────────────────────────────────────────────────────────
	loader := runtime.NewLoader(cfg.AgentDirs)
	loader.SetLogger(log)
	if errs := loader.LoadAll(); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("agent load error", zap.Error(e))
		}
	}
	log.Info("agents loaded", zap.Int("count", len(loader.All())))

	// ── Python binary pre-flight ─────────────────────────────────────────────
	// Resolve runtime.python_bin via $PATH at startup and rewrite the
	// config field to the absolute path. Two failures get caught this way:
	//
	//   1. The gateway launched from Finder / launchd / systemd inherits a
	//      tiny PATH (often just /usr/bin:/bin). A homebrew python at
	//      /opt/homebrew/bin/python3 won't resolve at exec time and every
	//      python-tool run dies with an ENOENT the user can't easily
	//      explain (observed 2026-05-28 after F1 shipped).
	//   2. config typo (python3.13 when 3.12 is installed) — we want to
	//      learn at boot, not on the first cron fire.
	//
	// We don't refuse to start when python is missing — a deployment may
	// legitimately have only built-in agents and no python tools — but we
	// log a stark warn so the operator notices.
	if cfg.Runtime.PythonBin != "" {
		if resolved, perr := exec.LookPath(cfg.Runtime.PythonBin); perr == nil {
			if resolved != cfg.Runtime.PythonBin {
				log.Info("python_bin resolved to absolute path",
					zap.String("configured", cfg.Runtime.PythonBin),
					zap.String("resolved", resolved))
			}
			cfg.Runtime.PythonBin = resolved
		} else {
			log.Warn("python_bin not found on $PATH — python tools will fail until this is fixed",
				zap.String("python_bin", cfg.Runtime.PythonBin),
				zap.String("PATH", os.Getenv("PATH")),
				zap.String("hint", "set runtime.python_bin to an absolute path (e.g. /opt/homebrew/bin/python3) in config.yaml"),
			)
		}
	}

	// ── Skill Loader ─────────────────────────────────────────────────────────
	// Scans ~/.soulacy/skills/, ~/.agents/skills/, ./.agents/skills/, etc.
	// Extra skill dirs can be added via config.skill_dirs (same map key as plugin_dirs).
	workDir, _ := os.Getwd()
	skillLoader := skills.New(workDir, cfg.SkillDirs, log)
	if errs := skillLoader.Scan(); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("skill load warning", zap.Error(e))
		}
	}
	if skillLoader.Count() > 0 {
		log.Info("agent skills loaded", zap.Int("count", skillLoader.Count()))
	}

	// ── Plugin Loader ─────────────────────────────────────────────────────────
	// Scans plugin_dirs for plugin.yaml manifests; loads Python tool libraries.
	// Missing dirs are silently skipped; malformed plugins are warned and skipped.
	pluginLoader := plugins.New(cfg.PluginDirs, log)
	if pluginLoader.Count() > 0 {
		log.Info("plugins loaded", zap.Int("count", pluginLoader.Count()))
	}

	// ── Event Hub (GUI real-time stream + action-log persistence) ─────────────
	hub := gateway.NewEventHub(log, actionBackend)

	// ── Engine ───────────────────────────────────────────────────────────────
	toolTimeout, _ := time.ParseDuration(cfg.Runtime.ToolTimeout)
	if toolTimeout == 0 {
		toolTimeout = 30 * time.Second
	}
	// Ollama Web Search API key for the built-in web_search tool: env var first,
	// then llm.providers.ollama.api_key from config.
	ollamaAPIKey := os.Getenv("OLLAMA_API_KEY")
	if ollamaAPIKey == "" {
		if oc, ok := cfg.LLM.Providers["ollama"]; ok {
			ollamaAPIKey = oc.APIKey
		}
	}

	// ── MCP client (connects to configured MCP servers; tools auto-injected into every agent) ──
	mcpServers := make(map[string]mcp.ServerConfig, len(cfg.MCP.Servers))
	for id, sc := range cfg.MCP.Servers {
		mcpServers[id] = mcp.ServerConfig{
			Transport: sc.Transport,
			Command:   sc.Command,
			Args:      sc.Args,
			Env:       sc.Env,
			URL:       sc.URL,
			Headers:   sc.Headers,
		}
	}
	mcpClient := mcp.New(mcp.Config{Servers: mcpServers}, log)
	defer mcpClient.Close()

	// ── Knowledge (RAG) — SQLite + sqlite-vec + Ollama embeddings ─────────────
	// Disabled silently when the DB path is empty (lets users opt out by
	// blanking knowledge.db_path in config). If the store opens but the
	// configured embedder isn't reachable, that's not fatal — it'll surface
	// at kb_search call time with a clear error.
	var knowledgeSvc *knowledge.Service
	if cfg.Knowledge.DBPath != "" {
		kbStore, kerr := knowledge.Open(cfg.Knowledge.DBPath)
		if kerr != nil {
			log.Warn("knowledge store unavailable (RAG disabled)", zap.Error(kerr))
		} else {
			defer kbStore.Close()
			embedders := llm.NewEmbedderRegistry()
			// Ollama embedder — points at the same baseURL as the chat provider.
			embedders.Register(llm.NewOllamaEmbedder(ollamaCfg.BaseURL))
			// OpenAI embedder — same key/baseURL as the chat OpenAI provider, if configured.
			if oc, ok := cfg.LLM.Providers["openai"]; ok && oc.APIKey != "" {
				openaiBase := oc.BaseURL
				if openaiBase == "" {
					openaiBase = "https://api.openai.com"
				}
				embedders.Register(llm.NewOpenAIEmbedder(openaiBase, oc.APIKey))
			}
			knowledgeSvc = knowledge.NewService(kbStore, embedders)
			log.Info("knowledge store ready",
				zap.String("path", cfg.Knowledge.DBPath),
				zap.String("default_embedding_model", cfg.Knowledge.EmbeddingModel),
				zap.Strings("embedders", embedders.IDs()),
			)
		}
	}

	// ── Vector Memory (optional semantic tier) ───────────────────────────────
	// Determine which vector backend to use:
	//   vector.backend = "qdrant"     → Qdrant REST
	//   vector.backend = "sqlite-vec" → built-in sqlite-vec
	//   memory.vector_db (legacy key) → same
	// When both are unset, vector memory is disabled.
	vectorBackendKey := cfg.Vector.Backend
	if vectorBackendKey == "" {
		vectorBackendKey = cfg.Memory.VectorDB // backwards-compat
	}

	var vectorStore *memory.VectorStore // kept for engine (sqlite-vec path only)
	var vecBackend vector.Backend       // new interface (used by future memory tools)

	if vectorBackendKey != "" {
		embedModel := cfg.Knowledge.EmbeddingModel
		if embedModel == "" {
			embedModel = "nomic-embed-text"
		}
		embedCfg := cfg.LLM.Providers[cfg.Knowledge.EmbeddingProvider]
		var rawEmbedder llm.Embedder
		switch cfg.Knowledge.EmbeddingProvider {
		case "openai":
			openaiBase := embedCfg.BaseURL
			if openaiBase == "" {
				openaiBase = "https://api.openai.com"
			}
			rawEmbedder = llm.NewOpenAIEmbedder(openaiBase, embedCfg.APIKey)
		default:
			rawEmbedder = llm.NewOllamaEmbedder(ollamaCfg.BaseURL)
		}
		memEmbedder := &llmEmbedAdapter{inner: rawEmbedder, model: embedModel}

		dims := cfg.Vector.Dims
		if dims <= 0 {
			dims = cfg.Memory.VectorDims
		}
		if dims <= 0 {
			dims = 768
		}

		switch vectorBackendKey {
		case "qdrant":
			qdrantURL := cfg.Vector.URL
			if qdrantURL == "" {
				qdrantURL = "http://localhost:6333"
			}
			col := cfg.Vector.Collection
			if col == "" {
				col = "soulacy_memory"
			}
			qctx, qcancel := context.WithTimeout(context.Background(), 15*time.Second)
			qb, qerr := vectorqdrant.New(qctx, vectorqdrant.Config{
				BaseURL:    qdrantURL,
				Collection: col,
				APIKey:     cfg.Vector.APIKey,
				Dims:       dims,
				Embedder:   memEmbedder,
			})
			qcancel()
			if qerr != nil {
				log.Warn("qdrant vector backend unavailable", zap.Error(qerr))
			} else {
				vecBackend = qb
				log.Info("vector memory enabled (qdrant)",
					zap.String("url", qdrantURL),
					zap.String("collection", col),
					zap.Int("dims", dims),
				)
			}
		default: // "sqlite-vec" or any legacy non-empty value
			vs, verr := memory.NewVectorStore(archive.DB(), memEmbedder, dims)
			if verr != nil {
				log.Warn("vector memory disabled (sqlite-vec not loaded or schema error)", zap.Error(verr))
			} else {
				vectorStore = vs
				vecBackend = vectorsqlitevec.New(vs)
				log.Info("vector memory enabled (sqlite-vec)",
					zap.Int("dims", dims),
					zap.String("embedding_provider", cfg.Knowledge.EmbeddingProvider),
				)
			}
		}
	}
	_ = vecBackend // available for future memory-tool use; engine uses vectorStore directly

	// ── Python Executor Backend ───────────────────────────────────────────────
	// "process" (default): one python3 subprocess per call, simple + compatible.
	// "pool": N pre-forked persistent workers, eliminates interpreter cold-start.
	var pyExecutor executor.Backend
	switch cfg.Executor.Backend {
	case "pool":
		workers := cfg.Executor.Workers
		if workers <= 0 {
			workers = 4
		}
		pb, perr := pool.New(cfg.Runtime.PythonBin, workers)
		if perr != nil {
			log.Warn("python worker pool failed to start, falling back to process executor",
				zap.Error(perr), zap.Int("workers", workers))
			pyExecutor = process.New(cfg.Runtime.PythonBin)
		} else {
			pyExecutor = pb
			defer pb.Close()
			log.Info("python executor: pre-forked pool",
				zap.Int("workers", workers),
				zap.String("python_bin", cfg.Runtime.PythonBin),
			)
		}
	default: // "process" or empty
		pyExecutor = process.New(cfg.Runtime.PythonBin)
		log.Info("python executor: process-per-call",
			zap.String("python_bin", cfg.Runtime.PythonBin))
	}
	_ = pyExecutor // wired into engine via SetExecutor below

	// ── Queue Backend ─────────────────────────────────────────────────────────
	// "memory" (default): in-process channel-based pub/sub, zero-dependency.
	// "nats": NATS JetStream — durable, multi-instance delivery.
	// The queue is currently wired but not yet consumed by the engine; it is
	// available for future channel adapters, inter-gateway message passing, and
	// Phase 3 tasks (RBAC event bus, cost-tracking events, etc.).
	var queueBackend queue.Backend
	switch cfg.Queue.Backend {
	case "nats":
		ackWait, _ := time.ParseDuration(cfg.Queue.NATSAckWait)
		if ackWait == 0 {
			ackWait = 30 * time.Second
		}
		nb, nerr := queuenats.New(queuenats.Config{
			URL:           cfg.Queue.NATSUrl,
			StreamName:    cfg.Queue.NATSStream,
			SubjectPrefix: cfg.Queue.NATSSubjectPrefix,
			AckWait:       ackWait,
			MaxDeliver:    cfg.Queue.NATSMaxDeliver,
		})
		if nerr != nil {
			return fmt.Errorf("nats queue backend: %w", nerr)
		}
		defer nb.Close()
		queueBackend = nb
		log.Info("queue backend: nats jetstream",
			zap.String("url", cfg.Queue.NATSUrl),
			zap.String("stream", cfg.Queue.NATSStream),
		)
	default: // "memory" or empty
		queueBackend = queuememory.New()
		log.Info("queue backend: in-process memory")
	}
	// ── Event publishing (extensibility E1) ───────────────────────────────────
	// Every EventHub emission is wrapped in a schema-v1 envelope and published
	// to the queue backend on "soulacy.events.<type>" (see docs/EVENTS.md).
	eventPublisher := events.NewPublisher(queueBackend, log)
	defer eventPublisher.Close()
	hub.SetEventPublisher(eventPublisher)
	log.Info("event publishing ready", zap.String("subject", "soulacy.events.>"))

	// ── Outbound webhooks (extensibility E2) ──────────────────────────────────
	// Queue-buffered, HMAC-signed, best-effort with bounded retries.
	if len(cfg.Hooks) > 0 {
		hookDispatcher := hooks.NewDispatcher(queueBackend, cfg.Hooks, log)
		if err := hookDispatcher.Start(context.Background()); err != nil {
			log.Warn("webhook dispatcher failed to start", zap.Error(err))
		} else {
			defer hookDispatcher.Close()
			log.Info("webhooks ready", zap.Int("endpoints", len(cfg.Hooks)))
		}
	}

	// pluginAdapter bridges plugins.Loader → runtime.PluginToolProvider.
	// The runtime package defines its own PluginTool type to avoid an import
	// cycle (runtime → plugins → runtime), so we convert here.
	var pluginProvider runtime.PluginToolProvider
	if pluginLoader.Count() > 0 {
		pluginProvider = &pluginToolAdapter{loader: pluginLoader}
	}

	engine := runtime.NewEngine(
		loader, llmRouter, fileStore, memBackend,
		cfg.Runtime.PythonBin, toolTimeout, log, hub, skillLoader, ollamaAPIKey, mcpClient, knowledgeSvc,
		cfg.Runtime.AllowSystemTools, vectorStore, pluginProvider,
	)
	engine.SetExecutor(pyExecutor)

	// PRODUCTION_AUDIT → F1 (2026-05-27): wire host-enforced rlimits on
	// every Python tool subprocess via the soulacy __exec-sandbox
	// wrapper. Falls back to direct exec if we can't discover our own
	// binary path (shouldn't happen on any supported OS, but we'd
	// rather lose sandboxing than refuse to start).
	if sbx := cfg.Runtime.Sandbox; sbx.Enabled {
		if selfPath, e := os.Executable(); e == nil {
			engine.SetSandbox(selfPath, sandbox.Limits{
				Enabled:    true,
				CPUSeconds: sbx.CPUSeconds,
				MemoryMB:   sbx.MemoryMB,
				OpenFiles:  sbx.OpenFiles,
				FileSizeMB: sbx.FileSizeMB,
			})
			log.Info("python sandbox enabled",
				zap.Int("cpu_seconds", sbx.CPUSeconds),
				zap.Int("memory_mb", sbx.MemoryMB),
				zap.Int("open_files", sbx.OpenFiles),
				zap.Int("file_size_mb", sbx.FileSizeMB),
			)
		} else {
			log.Warn("python sandbox requested but os.Executable() failed; running unsandboxed", zap.Error(e))
		}
	}

	// Audit log — append-only JSONL per session under cfg.Runtime.AuditDir.
	// MEM-03: pass the brain memory store into the engine.
	if brainStore != nil {
		engine.SetBrainMemory(brainStore)
	}

	engine.SetAuditLog(audit.New(cfg.Runtime.AuditDir))
	if cfg.Runtime.AuditDir != "" {
		log.Info("audit logging enabled", zap.String("dir", cfg.Runtime.AuditDir))
	}

	// python_file path allowlist — prevents crafted SOUL.yaml from executing
	// arbitrary host files. Skipped (all paths allowed) when list is empty.
	engine.SetAllowedToolDirs(cfg.Runtime.AllowedToolDirs)
	if len(cfg.Runtime.AllowedToolDirs) > 0 {
		log.Info("python_file allowlist active",
			zap.Strings("allowed_tool_dirs", cfg.Runtime.AllowedToolDirs))
	}

	// SSRF protection for HTTP-fetching built-in tools.
	engine.SetSSRF(cfg.Runtime.SSRFProtection, cfg.Runtime.AllowPrivateHosts)
	if cfg.Runtime.SSRFProtection {
		log.Info("SSRF protection enabled",
			zap.Strings("allow_private_hosts", cfg.Runtime.AllowPrivateHosts))
	}

	// App-lifetime context. Created here (before scheduler + channels) so
	// every long-running subsystem can derive its own context from it and
	// SIGTERM cancels them all in unison. (PRODUCTION_AUDIT → HIGH/Concurrency)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Scheduler ────────────────────────────────────────────────────────────
	sched := scheduler.New(engine, loader, log, ctx)
	sched.SetStatePath(filepath.Join(cfg.Memory.Dir, "scheduler-state.json"))
	for _, def := range loader.All() {
		if err := sched.RegisterAgent(def); err != nil {
			log.Warn("scheduler register failed", zap.String("agent", def.ID), zap.Error(err))
		}
	}

	// ── Channel Registry ─────────────────────────────────────────────────────
	chanReg := channels.NewRegistry(512)
	chanReg.SetLogger(log)
	sched.SetChannelRegistry(chanReg)
	httpAdapter := httpchan.New()
	chanReg.Register(httpAdapter)

	// Start optional channel adapters based on config
	chanCfg := cfg.Channels

	// ── Telegram ─────────────────────────────────────────────────────────────
	// Supports two config shapes:
	//
	// Single-bot (legacy, backwards-compatible):
	//   channels.telegram.token:   "..."
	//   channels.telegram.agent_id: "assistant"
	//
	// Multi-bot (one adapter per agent, separate bot tokens):
	//   channels.telegram.bots:
	//     - token: "..."
	//       agent_id: "assistant"
	//       allowed_user_ids: [123]
	//     - token: "..."
	//       agent_id: "financial-agent"
	//       allowed_user_ids: [123]
	//
	// Each bot gets a unique adapter ID: "telegram" for the primary (or first
	// when multi-bot), "telegram-<agentID>" for subsequent bots. This lets the
	// channel registry route outbound replies to the right bot.
	if tgCfg, ok := chanCfg["telegram"]; ok {
		if enabled, _ := tgCfg["enabled"].(bool); enabled {
			// Multi-bot path: channels.telegram.bots is a list
			if rawBots, hasBots := tgCfg["bots"]; hasBots {
				if botList, ok := rawBots.([]any); ok {
					for i, rawBot := range botList {
						botMap, ok := rawBot.(map[string]any)
						if !ok {
							continue
						}
						token, _ := botMap["token"].(string)
						agentID, _ := botMap["agent_id"].(string)
						botName, _ := botMap["bot_name"].(string)
						if token == "" {
							continue
						}
						if !externalChannelAgentAllowed(adapterIDForLog("telegram", i, agentID), agentID, log) {
							continue
						}
						allowedIDs := parseTelegramAllowedIDs(botMap)
						// Primary bot keeps the canonical "telegram" ID for backwards
						// compatibility; additional bots get "telegram-<agentID>".
						adapterID := "telegram"
						if i > 0 {
							adapterID = "telegram-" + sanitizeID(agentID)
						}
						tg := telegramchan.NewWithIDAndActivation(adapterID, token, agentID, allowedIDs, activationPolicyFromConfig(botMap, true))
						chanReg.Register(tg)
						log.Info("telegram bot registered",
							zap.String("adapter_id", adapterID),
							zap.String("agent_id", agentID),
							zap.String("bot_name", botName))
					}
				}
			} else {
				// Single-bot (legacy) path
				token, _ := tgCfg["token"].(string)
				agentID, _ := tgCfg["agent_id"].(string)
				if token != "" {
					if externalChannelAgentAllowed("telegram", agentID, log) {
						allowedIDs := parseTelegramAllowedIDs(tgCfg)
						tg := telegramchan.NewWithIDAndActivation("telegram", token, agentID, allowedIDs, activationPolicyFromConfig(tgCfg, true))
						chanReg.Register(tg)
					}
				}
			}
		}
	}

	// ── Discord ───────────────────────────────────────────────────────────────
	// Same dual-mode as Telegram: single-bot (legacy) or multi-bot via `bots:`.
	//   channels.discord.bots:
	//     - token: "Bot ..."
	//       agent_id: "assistant"
	//       guild_id: ""
	if dsCfg, ok := chanCfg["discord"]; ok {
		if enabled, _ := dsCfg["enabled"].(bool); enabled {
			if rawBots, hasBots := dsCfg["bots"]; hasBots {
				if botList, ok := rawBots.([]any); ok {
					for i, rawBot := range botList {
						botMap, ok := rawBot.(map[string]any)
						if !ok {
							continue
						}
						token, _ := botMap["token"].(string)
						agentID, _ := botMap["agent_id"].(string)
						guildID, _ := botMap["guild_id"].(string)
						botName, _ := botMap["bot_name"].(string)
						if token == "" {
							continue
						}
						if !externalChannelAgentAllowed(adapterIDForLog("discord", i, agentID), agentID, log) {
							continue
						}
						adapterID := "discord"
						if i > 0 {
							adapterID = "discord-" + sanitizeID(agentID)
						}
						ds := discordchan.NewWithIDAndActivation(adapterID, token, agentID, guildID, activationPolicyFromConfig(botMap, true))
						chanReg.Register(ds)
						log.Info("discord bot registered",
							zap.String("adapter_id", adapterID),
							zap.String("agent_id", agentID),
							zap.String("bot_name", botName))
					}
				}
			} else {
				token, _ := dsCfg["token"].(string)
				agentID, _ := dsCfg["agent_id"].(string)
				guildID, _ := dsCfg["guild_id"].(string)
				if token != "" {
					if externalChannelAgentAllowed("discord", agentID, log) {
						ds := discordchan.NewWithIDAndActivation("discord", token, agentID, guildID, activationPolicyFromConfig(dsCfg, true))
						chanReg.Register(ds)
					}
				}
			}
		}
	}

	// ── Slack ─────────────────────────────────────────────────────────────────
	// Same dual-mode as Telegram: single-bot (legacy) or multi-bot via `bots:`.
	//   channels.slack.bots:
	//     - bot_token: "xoxb-..."
	//       app_token: "xapp-..."
	//       agent_id: "assistant"
	if slCfg, ok := chanCfg["slack"]; ok {
		if enabled, _ := slCfg["enabled"].(bool); enabled {
			if rawBots, hasBots := slCfg["bots"]; hasBots {
				if botList, ok := rawBots.([]any); ok {
					for i, rawBot := range botList {
						botMap, ok := rawBot.(map[string]any)
						if !ok {
							continue
						}
						botToken, _ := botMap["bot_token"].(string)
						appToken, _ := botMap["app_token"].(string)
						agentID, _ := botMap["agent_id"].(string)
						botName, _ := botMap["bot_name"].(string)
						if botToken == "" || appToken == "" {
							continue
						}
						if !externalChannelAgentAllowed(adapterIDForLog("slack", i, agentID), agentID, log) {
							continue
						}
						adapterID := "slack"
						if i > 0 {
							adapterID = "slack-" + sanitizeID(agentID)
						}
						sl := slackchan.NewWithIDAndActivation(adapterID, botToken, appToken, agentID, activationPolicyFromConfig(botMap, true))
						chanReg.Register(sl)
						log.Info("slack bot registered",
							zap.String("adapter_id", adapterID),
							zap.String("agent_id", agentID),
							zap.String("bot_name", botName))
					}
				}
			} else {
				botToken, _ := slCfg["bot_token"].(string)
				appToken, _ := slCfg["app_token"].(string)
				agentID, _ := slCfg["agent_id"].(string)
				if botToken != "" && appToken != "" {
					if externalChannelAgentAllowed("slack", agentID, log) {
						sl := slackchan.NewWithIDAndActivation("slack", botToken, appToken, agentID, activationPolicyFromConfig(slCfg, true))
						chanReg.Register(sl)
					}
				}
			}
		}
	}

	// WhatsApp is webhook-driven (Meta pushes to us), but it still needs to
	// send replies via the Graph API. Register it in chanReg so StartAll()
	// wires the shared inbox and Send() can route replies to it.
	var waAdapter *wachan.Adapter
	if waCfg, ok := chanCfg["whatsapp"]; ok {
		if enabled, _ := waCfg["enabled"].(bool); enabled {
			phoneNumberID, _ := waCfg["phone_number_id"].(string)
			accessToken, _ := waCfg["access_token"].(string)
			verifyToken, _ := waCfg["verify_token"].(string)
			// app_secret is used to verify the HMAC on inbound webhook
			// POSTs. PRODUCTION_AUDIT → CRIT/Security.
			appSecret, _ := waCfg["app_secret"].(string)
			agentID, _ := waCfg["agent_id"].(string)
			if phoneNumberID != "" && accessToken != "" && verifyToken != "" {
				if externalChannelAgentAllowed("whatsapp", agentID, log) {
					waAdapter = wachan.NewWithActivation(phoneNumberID, accessToken, verifyToken, appSecret, agentID, activationPolicyFromConfig(waCfg, false), log)
					chanReg.Register(waAdapter) // StartAll will call waAdapter.Start()
				}
			}
		}
	}

	// WhatsApp Web is an experimental QR-linked channel backed by a Node
	// sidecar (Baileys). Keep it separate from the official Meta Cloud API
	// adapter above so deployments can make an explicit tradeoff.
	if waWebCfg, ok := chanCfg["whatsapp_web"]; ok {
		if enabled, _ := waWebCfg["enabled"].(bool); enabled {
			command, _ := waWebCfg["command"].(string)
			args := parseStringList(waWebCfg["args"])
			sessionDir, _ := waWebCfg["session_dir"].(string)
			accountID, _ := waWebCfg["account_id"].(string)
			agentID, _ := waWebCfg["agent_id"].(string)
			activation := activationPolicyFromConfig(waWebCfg, true)
			if command == "" {
				command = "node"
			}
			if len(args) > 0 && agentID != "" {
				if externalChannelAgentAllowed("whatsapp_web", agentID, log) {
					waWeb := wawebchan.New("whatsapp_web", command, args, sessionDir, agentID, accountID, activation, log)
					chanReg.Register(waWeb)
					log.Warn("experimental WhatsApp Web channel enabled",
						zap.String("agent_id", agentID),
						zap.String("account_id", accountID),
						zap.String("trigger_phrase", activation.TriggerPhrase),
						zap.Bool("ignore_groups", activation.IgnoreGroups))
				}
			}
		}
	}

	if errs := chanReg.StartAll(ctx); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("channel start error", zap.Error(e))
		}
	}
	defer func() { _ = chanReg.StopAll() }()

	// Engine ← failure notifier. Run errors now produce a real outbound
	// notification (per agent's notify_on_failure block) instead of just
	// landing silently in the actionlog. See cmd/soulacy/notify.go.
	// Wired AFTER chanReg.StartAll so adapters are connected before the
	// first cron tick or HTTP request can trigger a failure path.
	engine.SetFailureNotifier(&failureNotifier{chanReg: chanReg, log: log})

	sched.Start()
	defer sched.Stop()

	// PRODUCTION_AUDIT → F2 (2026-05-27): replay any in-flight runs that
	// the previous gateway process didn't finish (host crash, kill -9,
	// upgrade restart, etc.). Looks at the actionlog for recent
	// message.in events without a paired message.out / error, and
	// re-enqueues them onto the live inbox before the worker pool
	// starts draining. Bounded to runs within the last hour so an
	// operator coming back from a long outage isn't blasted by stale
	// prompts. Synchronous HTTP channel + cron triggers are skipped
	// (no original requester / re-fires naturally).
	replayIncompleteRuns(actionBackend, chanReg, log)

	// ── Message Router — bounded worker pool draining the shared inbox ──────
	// HTTP channel messages are handled synchronously via the HTTP handler;
	// all other channel messages (Telegram, Discord, Slack, WhatsApp, etc.)
	// flow through the engine here and are replied to via chanReg.Send().
	//
	// PRODUCTION_AUDIT → CRITICAL/Concurrency: previously this spawned ONE
	// goroutine PER inbound message with no semaphore. A burst (or a hostile
	// Telegram user spamming) could OOM the process or saturate the Python
	// tool subprocesses. We now bound concurrency by a worker pool sized to
	// runtime.max_concurrent_sessions (default 100). Per-run timeout uses
	// each agent's declared run_timeout (via def.ResolvedRunTimeout) instead
	// of the previous hard-coded 5 minutes.
	workerCount := cfg.Runtime.MaxConcurrentSessions
	if workerCount <= 0 {
		workerCount = 100
	}
	for w := 0; w < workerCount; w++ {
		go func() {
			for msg := range chanReg.Inbox() {
				if msg.Channel == "http" {
					continue // synchronous path
				}
				def := loader.Get(msg.AgentID)
				timeout := 5 * time.Minute
				if def != nil {
					timeout = def.ResolvedRunTimeout(timeout)
				}
				mCtx, mCancel := context.WithTimeout(ctx, timeout)
				// Worker-pool saturation gauge: increment while a run is
				// in-flight, decrement when done. (PRODUCTION_AUDIT → MED/Observability)
				metrics.WorkerPoolActiveRuns.Inc()
				reply, err := engine.Handle(mCtx, msg)
				metrics.WorkerPoolActiveRuns.Dec()
				if err != nil {
					mCancel()
					log.Error("engine error", zap.String("agent", msg.AgentID), zap.Error(err))
					continue
				}
				if err := chanReg.Send(mCtx, reply); err != nil {
					log.Error("channel send error",
						zap.String("channel", msg.Channel), zap.Error(err))
				}
				mCancel()
			}
		}()
	}

	// ── Auth Engine ───────────────────────────────────────────────────────────
	// Builds the authentication subsystem from config. In the default "apikey"
	// mode this is functionally identical to Phase 2 — the static API key is
	// checked on every request. Set auth.mode=jwt in config.yaml to enable
	// short-lived token issuance and optional OIDC validation.
	accessTTL, _ := time.ParseDuration(cfg.Auth.JWTAccessTTL)
	refreshTTL, _ := time.ParseDuration(cfg.Auth.JWTRefreshTTL)
	authEngine, authErr := auth.New(auth.Config{
		Mode:          cfg.Auth.Mode,
		JWTSecret:     cfg.Auth.JWTSecret,
		JWTAccessTTL:  accessTTL,
		JWTRefreshTTL: refreshTTL,
		OIDCIssuer:    cfg.Auth.OIDCIssuer,
		OIDCAudience:  cfg.Auth.OIDCAudience,
		OIDCClientID:  cfg.Auth.OIDCClientID,
	}, cfg.Server.APIKey, log)
	if authErr != nil {
		return fmt.Errorf("auth engine: %w", authErr)
	}
	defer authEngine.Close()
	log.Info("auth engine ready", zap.String("mode", authEngine.Mode()))

	// ── RBAC Manager ──────────────────────────────────────────────────────────
	// Initialise the RBAC store (SQLite). Per-agent grants are persisted here;
	// the static default policy lives in the rbac package and needs no DB.
	// The NoopStore is used as fallback on failure so the gateway can still
	// start — RBAC will degrade to static-policy-only (no per-agent overrides).
	rbacDBPath := filepath.Join(home, ".soulacy", "rbac.db")
	var rbacStore rbac.Store
	if rs, rerr := rbac.NewSQLiteStore(rbacDBPath); rerr != nil {
		log.Warn("RBAC SQLite store unavailable, falling back to static policy only",
			zap.String("path", rbacDBPath), zap.Error(rerr))
		rbacStore = rbac.NoopStore{}
	} else {
		defer rs.Close()
		rbacStore = rs
		log.Info("RBAC store ready", zap.String("path", rbacDBPath))
	}
	rbacManager := rbac.NewManager(rbacStore, log)

	// ── Credential Vault ──────────────────────────────────────────────────────
	var credVault credentials.Vault
	localKMS, kmsErr := credentials.NewLocalKMS()
	if kmsErr != nil {
		log.Warn("credential vault KMS init failed, vault disabled", zap.Error(kmsErr))
	} else {
		vaultPath := filepath.Join(home, ".soulacy", "credentials.db")
		if cv, cvErr := credentials.NewSQLiteVault(vaultPath, localKMS); cvErr != nil {
			log.Warn("credential vault unavailable", zap.String("path", vaultPath), zap.Error(cvErr))
		} else {
			defer cv.Close()
			credVault = cv
			log.Info("credential vault ready", zap.String("path", vaultPath))
		}
	}

	// ── Workflow Checkpoint Store (E5) ────────────────────────────────────────
	checkpointPath := filepath.Join(home, ".soulacy", "checkpoints.db")
	if cs, cserr := runtime.NewCheckpointStore(checkpointPath); cserr != nil {
		log.Warn("workflow checkpoint store unavailable", zap.Error(cserr))
	} else {
		defer cs.Close()
		engine.SetCheckpointStore(cs)
		log.Info("workflow checkpoints ready", zap.String("path", checkpointPath))
	}

	// ── Telemetry (OTEL) ─────────────────────────────────────────────────────
	telCfg := telemetry.Config{
		Enabled:      cfg.Telemetry.Enabled,
		Exporter:     cfg.Telemetry.Exporter,
		OTLPEndpoint: cfg.Telemetry.OTLPEndpoint,
		ServiceName:  cfg.Telemetry.ServiceName,
	}
	if telCfg.ServiceName == "" {
		telCfg.ServiceName = "soulacy"
	}
	telProvider, telErr := telemetry.New(ctx, telCfg)
	if telErr != nil {
		log.Warn("telemetry init failed, tracing disabled", zap.Error(telErr))
	} else {
		defer telProvider.Shutdown(context.Background()) //nolint:errcheck
		engine.SetTracer(&engineTracerAdapter{t: telProvider.Tracer})
		log.Info("telemetry ready", zap.String("exporter", telCfg.Exporter))
	}

	// ── Cost Store ────────────────────────────────────────────────────────────
	costsPath := filepath.Join(home, ".soulacy", "costs.db")
	var openedCostStore *costs.Store
	if costsStore, cserr := costs.NewStore(costsPath); cserr != nil {
		log.Warn("cost store unavailable", zap.Error(cserr))
	} else {
		defer costsStore.Close()
		openedCostStore = costsStore
		engine.SetCostStore(&engineCostStoreAdapter{s: costsStore})
		log.Info("cost tracking ready", zap.String("path", costsPath))
	}

	// ── Gateway Server ────────────────────────────────────────────────────────
	// Created BEFORE the watcher so the watcher can wire its OnPyChange hook
	// to the server's tool-catalog cache.
	srv := gateway.New(cfg, cfgPath, engine, loader, llmRouter, chanReg, sched, httpAdapter, waAdapter, skillLoader, actionBackend, mcpClient, hub, log)
	srv.SetAuth(authEngine)
	srv.SetRBAC(rbacManager)
	if credVault != nil {
		srv.SetCredentialVault(credVault)
	}

	// Wire the cost store into the gateway so /api/v1/costs routes work.
	if openedCostStore != nil {
		srv.SetCostStore(openedCostStore)
	}

	// ── Workboard Store (Story 5) ─────────────────────────────────────────────
	workboardPath := filepath.Join(home, ".soulacy", "workboard.db")
	if wbStore, wberr := workboard.NewStore(workboardPath); wberr != nil {
		log.Warn("workboard store unavailable", zap.Error(wberr))
	} else {
		defer wbStore.Close()
		srv.SetWorkboardStore(wbStore)
		log.Info("workboard ready", zap.String("path", workboardPath))
	}

	// ── Rate Limiter (Task #33) ───────────────────────────────────────────────
	rlCfg := ratelimit.Config{
		Enabled:           cfg.RateLimit.Enabled,
		PerUserRPM:        cfg.RateLimit.PerUserRPM,
		PerAgentRPM:       cfg.RateLimit.PerAgentRPM,
		PerUserTokensDay:  cfg.RateLimit.PerUserTokensDay,
		PerAgentTokensDay: cfg.RateLimit.PerAgentTokensDay,
		Backend:           cfg.RateLimit.Backend,
		RedisURL:          cfg.RateLimit.RedisURL,
	}
	if !rlCfg.Enabled && rlCfg.PerUserRPM == 0 && rlCfg.PerAgentRPM == 0 {
		rlCfg = ratelimit.DefaultConfig()
	}
	if rlManager, rlErr := ratelimit.New(rlCfg, log); rlErr != nil {
		log.Warn("rate limiter init failed, running without rate limiting", zap.Error(rlErr))
	} else {
		defer rlManager.Close()
		srv.SetRateLimiter(rlManager)
		log.Info("rate limiter ready",
			zap.Bool("enabled", rlCfg.Enabled),
			zap.Int("per_user_rpm", rlCfg.PerUserRPM),
			zap.Int("per_agent_rpm", rlCfg.PerAgentRPM),
			zap.Int("per_user_tokens_day", rlCfg.PerUserTokensDay),
			zap.String("backend", rlCfg.Backend),
		)
	}

	// ── API Key Store ─────────────────────────────────────────────────────────
	apiKeyPath := filepath.Join(home, ".soulacy", "apikeys.db")
	if akStore, akErr := apikeys.NewSQLiteStore(apiKeyPath); akErr != nil {
		log.Warn("api key store unavailable", zap.Error(akErr))
	} else {
		defer akStore.Close()
		srv.SetAPIKeyStore(akStore)
		authEngine.SetAPIKeyStore(akStore) // wire into auth middleware (sk_ prefix validation)
		log.Info("api key store ready", zap.String("path", apiKeyPath))
	}

	// ── Dead-Letter Queue ─────────────────────────────────────────────────────
	dlqPath := filepath.Join(home, ".soulacy", "dlq.db")
	if dlqStore, dlqErr := dlq.NewSQLiteStore(dlqPath); dlqErr != nil {
		log.Warn("dead-letter queue unavailable", zap.Error(dlqErr))
	} else {
		defer dlqStore.Close()
		srv.SetDLQStore(dlqStore)
		engine.SetDLQStore(&engineDLQAdapter{s: dlqStore}) // wire into engine failure path
		log.Info("dead-letter queue ready", zap.String("path", dlqPath))
	}

	// ── Conversation History Store ────────────────────────────────────────────
	historyPath := filepath.Join(home, ".soulacy", "history.db")
	if histStore, histErr := session.NewSQLiteHistoryStore(historyPath); histErr != nil {
		log.Warn("conversation history store unavailable", zap.Error(histErr))
	} else {
		defer histStore.Close()
		srv.SetHistoryStore(histStore)
		engine.SetHistoryStore(histStore) // wire into engine turn recording
		log.Info("conversation history ready", zap.String("path", historyPath))
	}

	// ── Builder Registry (E4 gap detection) ───────────────────────────────────
	builderReg := builder.NewRegistry()
	// Seed from MCP client's known tools, if available.
	// (Full integration with Python tool catalog is a follow-up.)
	srv.SetBuilderRegistry(builderReg)

	// ── File Watcher — hot-reload SOUL.yaml + invalidate tool catalog on .py edits ──
	fsWatcher, watchErr := runtime.NewWatcher(loader, sched, cfg.AgentDirs, log, srv.PythonToolDirs()...)
	if watchErr != nil {
		log.Warn("file watcher unavailable (agents require restart to reload)", zap.Error(watchErr))
	} else {
		fsWatcher.OnPyChange = srv.InvalidateToolCatalog
		fsWatcher.Start()
		defer fsWatcher.Stop()
	}

	// Graceful shutdown on SIGINT / SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Info("shutdown signal received", zap.String("signal", sig.String()))
		cancel()
	}()

	log.Info("gateway ready",
		zap.String("host", cfg.Server.Host),
		zap.Int("port", cfg.Server.Port),
		zap.Bool("gui", cfg.Server.GUIEnabled),
	)

	return srv.Listen(ctx)
}

// parseTelegramAllowedIDs extracts the allowed_user_ids list from a channel
// config block (works for both single-bot and multi-bot map shapes).
func parseTelegramAllowedIDs(m map[string]any) []int64 {
	raw, ok := m["allowed_user_ids"]
	if !ok {
		return nil
	}
	var out []int64
	if list, ok := raw.([]any); ok {
		for _, item := range list {
			switch v := item.(type) {
			case int64:
				out = append(out, v)
			case float64:
				out = append(out, int64(v))
			case int:
				out = append(out, int64(v))
			}
		}
	}
	return out
}

// parseStringList extracts a string argv list from config values that may have
// been loaded from YAML as []any, []string, or a whitespace-separated string.
func parseStringList(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(v)
	default:
		return nil
	}
}

func parseDelimitedConfigList(raw any) []string {
	switch v := raw.(type) {
	case []string, []any:
		return parseStringList(raw)
	case string:
		parts := strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\t'
		})
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}

func parseBoolConfig(raw any, fallback bool) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1", "on":
			return true
		case "false", "no", "0", "off":
			return false
		}
	}
	return fallback
}

func activationPolicyFromConfig(m map[string]any, defaultIgnoreGroups bool) channels.ActivationPolicy {
	trigger := strings.TrimSpace(fmt.Sprint(m["trigger_phrase"]))
	if trigger == "" {
		trigger = "!soulacy"
	}
	userIDs := parseDelimitedConfigList(m["allowed_user_ids"])
	for _, uid := range parseTelegramAllowedIDs(m) {
		userIDs = append(userIDs, strconv.FormatInt(uid, 10))
	}
	return channels.ActivationPolicy{
		TriggerPhrase:    trigger,
		IgnoreGroups:     parseBoolConfig(m["ignore_groups"], defaultIgnoreGroups),
		AllowedThreadIDs: parseDelimitedConfigList(m["allowed_chat_ids"]),
		AllowedUserIDs:   userIDs,
	}
}

func adapterIDForLog(channel string, index int, agentID string) string {
	if index == 0 {
		return channel
	}
	return channel + "-" + sanitizeID(agentID)
}

func externalChannelAgentAllowed(adapterID, agentID string, log *zap.Logger) bool {
	if strings.TrimSpace(agentID) != runtime.SystemAgentID {
		return true
	}
	log.Warn("external channel mapping skipped: system agent is web-only",
		zap.String("adapter_id", adapterID),
		zap.String("agent_id", agentID),
	)
	return false
}

// sanitizeID replaces characters that are not safe for adapter IDs or log
// fields with hyphens. Keeps letters, digits, hyphens, and underscores.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// isLoopbackHost returns true if `host` is one of the well-known loopback
// addresses (127.0.0.0/8, ::1, localhost). Used by the security guardrail in
// run() to gate the empty-API-key check.
func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	// 127.0.0.0/8 is loopback per RFC 1122 — anything starting with "127." is fine.
	if strings.HasPrefix(host, "127.") {
		return true
	}
	return false
}

func buildLogger(cfg config.LogConfig) (*zap.Logger, error) {
	var zcfg zap.Config
	if cfg.Format == "json" {
		zcfg = zap.NewProductionConfig()
	} else {
		// Console mode: human-readable but no stack traces on WARN.
		// Stack traces only appear on ERROR and above.
		zcfg = zap.NewDevelopmentConfig()
		zcfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		zcfg.DisableStacktrace = false // keep for ERROR
		// Override stack trace level to ERROR (dev default is WARN)
		zcfg.Development = false // disables panic-on-DPanic and WARN stack traces
	}

	level := zap.NewAtomicLevel()
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		level.SetLevel(zap.InfoLevel)
	}
	zcfg.Level = level

	if cfg.File != "" {
		zcfg.OutputPaths = append(zcfg.OutputPaths, cfg.File)
	}

	return zcfg.Build(zap.WithCaller(true), zap.AddStacktrace(zap.ErrorLevel))
}
