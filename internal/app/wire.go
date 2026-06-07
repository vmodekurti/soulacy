package app

// wire.go — App.Run: the full subsystem wiring, extracted verbatim from
// cmd/soulacy/main.go (Story E10 part 3). Order matters and is documented
// inline; deferred closes run when Run returns, mirroring the original
// run() semantics exactly.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/audit"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/builder"
	"github.com/soulacy/soulacy/internal/caps"
	"github.com/soulacy/soulacy/internal/channels"
	httpchan "github.com/soulacy/soulacy/internal/channels/http"
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
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/internal/pluginmigrate"
	"github.com/soulacy/soulacy/internal/plugins"
	"github.com/soulacy/soulacy/internal/queue/dlq"
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
	"github.com/soulacy/soulacy/internal/voice"
	"github.com/soulacy/soulacy/internal/workboard"
	"github.com/soulacy/soulacy/sdk/registry"
	sdkstorage "github.com/soulacy/soulacy/sdk/storage"
)

// Run wires every subsystem and blocks until the gateway exits (parent ctx
// cancellation and SIGINT/SIGTERM both shut the process down cleanly).
func (a *App) Run(parent context.Context) error {
	cfg, cfgPath, log := a.cfg, a.cfgPath, a.log
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

	// ── Plugin database migrations (Story E16) ───────────────────────────────
	// Compiled-in plugins register schema steps via storage.RegisterMigration
	// from init(); they apply here — the database boot phase — one
	// transaction per step, into the DEDICATED plugin database (never the
	// core stores). Namespace enforcement (plugin_<id>_* tables only) happens
	// before any SQL executes; a broken plugin warns and skips, never aborts.
	if pending := sdkstorage.RegisteredMigrations(); len(pending) > 0 {
		pmPath := filepath.Join(home, ".soulacy", "plugins.db")
		if pm, pmErr := pluginmigrate.Open(pmPath); pmErr != nil {
			log.Warn("plugin database unavailable; plugin migrations skipped", zap.Error(pmErr))
		} else {
			defer pm.Close()
			applied, merrs := pm.Apply(pending)
			for _, e := range merrs {
				log.Warn("plugin migration refused or failed", zap.Error(e))
			}
			if applied > 0 {
				log.Info("plugin migrations applied",
					zap.Int("count", applied), zap.String("path", pmPath))
			}
		}
	}

	// ── LLM Router ───────────────────────────────────────────────────────────
	// Providers are resolved through the SDK factory registry (Story E10);
	// the built-ins self-register from init() via cmd/soulacy/builtins_gen.go.
	llmRouter := llm.NewRouter(cfg.LLM.DefaultProvider)

	// Ollama registers unconditionally (zero-config local default).
	ollamaCfg := cfg.LLM.Providers["ollama"]
	if p, ok, perr := registry.NewProvider("ollama", providerCfgMap(ollamaCfg)); ok && perr == nil {
		llmRouter.Register(p)
	} else {
		log.Warn("ollama provider init failed", zap.Error(perr))
	}

	// Every other configured provider resolves by its config id. Known names
	// hit their dedicated factory; anything else with base_url + api_key gets
	// the generic OpenAI-compatible adapter (OpenRouter / Together / Groq /
	// vLLM under a custom id, no code changes).
	for id, pcfg := range cfg.LLM.Providers {
		if id == "ollama" || pcfg.APIKey == "" {
			continue
		}
		m := providerCfgMap(pcfg)
		m["id"] = id
		p, ok, perr := registry.NewProvider(id, m)
		if !ok {
			if pcfg.BaseURL == "" {
				log.Warn("llm provider skipped: unknown id and no base_url for the generic adapter",
					zap.String("id", id))
				continue
			}
			p, _, perr = registry.NewProvider("openai", m)
		}
		if perr != nil {
			log.Warn("llm provider init failed", zap.String("id", id), zap.Error(perr))
			continue
		}
		llmRouter.Register(p)
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
	// config field to the absolute path. Catches the launchd/Finder tiny-PATH
	// problem and config typos at boot rather than on the first cron fire.
	// Missing python is a warn, not fatal — built-in-only deployments are fine.
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

	// ── Plugin Loader ─────────────────────────────────────────────────────────
	// Scans plugin_dirs for plugin.yaml manifests; loads Python tool libraries
	// and (manifest_schema 2, E7) sidecar channels, providers, skills, GUI mounts.
	pluginLoader := plugins.New(cfg.PluginDirs, log)
	if pluginLoader.Count() > 0 {
		log.Info("plugins loaded", zap.Int("count", pluginLoader.Count()))
	}

	// ── Skill Loader ─────────────────────────────────────────────────────────
	// Scans ~/.soulacy/skills/, ~/.agents/skills/, ./.agents/skills/, etc.
	// Extra skill dirs come from config.skill_dirs and manifest-v2 plugins (E7).
	workDir, _ := os.Getwd()
	skillDirs := append([]string{}, cfg.SkillDirs...)
	for _, lp := range pluginLoader.All() {
		skillDirs = append(skillDirs, lp.SkillDirs()...)
	}
	skillLoader := skills.New(workDir, skillDirs, log)
	if errs := skillLoader.Scan(); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("skill load warning", zap.Error(e))
		}
	}
	if skillLoader.Count() > 0 {
		log.Info("agent skills loaded", zap.Int("count", skillLoader.Count()))
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
	// Disabled silently when the DB path is empty. An unreachable embedder is
	// not fatal — it surfaces at kb_search call time with a clear error.
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

		// Resolved through the SDK factory registry (Story E10). The
		// sqlite-vec path keeps building *memory.VectorStore host-side —
		// the engine consumes the store directly — and hands it to the
		// factory under the "store" key.
		switch vectorBackendKey {
		case "qdrant":
			qURL := cfg.Vector.URL
			if qURL == "" {
				qURL = "http://localhost:6333"
			}
			qCol := cfg.Vector.Collection
			if qCol == "" {
				qCol = "soulacy_memory"
			}
			qb, _, qerr := registry.NewVector("qdrant", map[string]any{
				"base_url":   qURL,
				"collection": qCol,
				"api_key":    cfg.Vector.APIKey,
				"dims":       dims,
				"embedder":   memory.Embedder(memEmbedder),
			})
			if qerr != nil {
				log.Warn("qdrant vector backend unavailable", zap.Error(qerr))
			} else {
				vecBackend = qb
				log.Info("vector memory enabled (qdrant)",
					zap.String("url", qURL),
					zap.String("collection", qCol),
					zap.Int("dims", dims),
				)
			}
		default: // "sqlite-vec" or any legacy non-empty value
			vs, verr := memory.NewVectorStore(archive.DB(), memEmbedder, dims)
			if verr != nil {
				log.Warn("vector memory disabled (sqlite-vec not loaded or schema error)", zap.Error(verr))
			} else {
				vectorStore = vs
				svb, _, sverr := registry.NewVector("sqlite-vec", map[string]any{"store": vs})
				if sverr != nil {
					log.Warn("sqlite-vec backend init failed", zap.Error(sverr))
				} else {
					vecBackend = svb
					log.Info("vector memory enabled (sqlite-vec)",
						zap.Int("dims", dims),
						zap.String("embedding_provider", cfg.Knowledge.EmbeddingProvider),
					)
				}
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
	// Resolved through the SDK factory registry (Story E10).
	queueName := cfg.Queue.Backend
	if queueName == "" {
		queueName = "memory"
	}
	queueBackend, qok, qerr := registry.NewQueue(queueName, map[string]any{
		"url":            cfg.Queue.NATSUrl,
		"stream":         cfg.Queue.NATSStream,
		"subject_prefix": cfg.Queue.NATSSubjectPrefix,
		"ack_wait":       cfg.Queue.NATSAckWait,
		"max_deliver":    cfg.Queue.NATSMaxDeliver,
	})
	if !qok {
		return fmt.Errorf("unknown queue backend %q (registered: %v)", queueName, registry.Queues())
	}
	if qerr != nil {
		return fmt.Errorf("%s queue backend: %w", queueName, qerr)
	}
	defer queueBackend.Close()
	log.Info("queue backend ready", zap.String("backend", queueName))

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

	// PRODUCTION_AUDIT → F1 (2026-05-27): host-enforced rlimits on every
	// Python tool subprocess via the soulacy __exec-sandbox wrapper.
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
	// every long-running subsystem derives from it and SIGTERM (or parent
	// cancellation) cancels them all in unison.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// ── Scheduler ────────────────────────────────────────────────────────────
	sched := scheduler.New(engine, loader, log, ctx)
	sched.SetStatePath(filepath.Join(cfg.Memory.Dir, "scheduler-state.json"))
	for _, def := range loader.All() {
		if err := sched.RegisterAgent(def); err != nil {
			log.Warn("scheduler register failed", zap.String("agent", def.ID), zap.Error(err))
		}
	}

	// ── Credential Vault ──────────────────────────────────────────────────────
	// Created before the channel registry so plugin sidecar channels (E6/E7)
	// can resolve their delegated credentials at spawn.
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

	// ── Channel Registry ─────────────────────────────────────────────────────
	chanReg := channels.NewRegistry(512)
	chanReg.SetLogger(log)
	sched.SetChannelRegistry(chanReg)
	httpAdapter := httpchan.New()
	chanReg.Register(httpAdapter)

	// ── Plugin manifest-v2 contributions (E7) ─────────────────────────────────
	// Sidecar channels become supervised external adapters (started by
	// StartAll below); OpenAI-compatible providers join the LLM router.
	// Best-effort: a broken plugin contribution logs a warning, never aborts.
	if pluginLoader.Count() > 0 {
		wireSandboxSelf := ""
		wireSandboxLimits := sandbox.Limits{}
		if sbx := cfg.Runtime.Sandbox; sbx.Enabled {
			if selfPath, e := os.Executable(); e == nil {
				wireSandboxSelf = selfPath
				wireSandboxLimits = sandbox.Limits{
					Enabled:    true,
					CPUSeconds: sbx.CPUSeconds,
					MemoryMB:   sbx.MemoryMB,
					OpenFiles:  sbx.OpenFiles,
					FileSizeMB: sbx.FileSizeMB,
				}
			}
		}
		for _, werr := range plugins.Wire(ctx, pluginLoader, plugins.WireDeps{
			Channels:      chanReg,
			LLM:           llmRouter,
			Vault:         credVault,
			Log:           log,
			SandboxSelf:   wireSandboxSelf,
			SandboxLimits: wireSandboxLimits,
			PluginsConfig: cfg.PluginsConfig, // Story E17
		}) {
			log.Warn("plugin contribution skipped", zap.Error(werr))
		}
	}

	// Start optional channel adapters based on config. Construction is
	// registry-routed (Story E10); the host keeps config-shape handling
	// (single-bot vs multi-bot lists, adapter ids, system-agent guard).
	chanCfg := cfg.Channels

	// ── Telegram ─────────────────────────────────────────────────────────────
	// Single-bot (legacy): channels.telegram.token / agent_id.
	// Multi-bot: channels.telegram.bots: [{token, agent_id, ...}, ...].
	// Each bot gets a unique adapter ID: "telegram" for the primary (or first
	// when multi-bot), "telegram-<agentID>" for subsequent bots.
	if tgCfg, ok := chanCfg["telegram"]; ok {
		if enabled, _ := tgCfg["enabled"].(bool); enabled {
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
						// Primary bot keeps the canonical "telegram" ID for backwards
						// compatibility; additional bots get "telegram-<agentID>".
						adapterID := "telegram"
						if i > 0 {
							adapterID = "telegram-" + sanitizeID(agentID)
						}
						tg, cerr := buildChannel("telegram", adapterID, botMap, log)
						if cerr != nil {
							log.Warn("telegram bot skipped", zap.String("adapter_id", adapterID), zap.Error(cerr))
							continue
						}
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
						if tg, cerr := buildChannel("telegram", "telegram", tgCfg, log); cerr != nil {
							log.Warn("telegram channel skipped", zap.Error(cerr))
						} else {
							chanReg.Register(tg)
						}
					}
				}
			}
		}
	}

	// ── Discord ───────────────────────────────────────────────────────────────
	// Same dual-mode as Telegram: single-bot (legacy) or multi-bot via `bots:`.
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
						ds, cerr := buildChannel("discord", adapterID, botMap, log)
						if cerr != nil {
							log.Warn("discord bot skipped", zap.String("adapter_id", adapterID), zap.Error(cerr))
							continue
						}
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
				if token != "" {
					if externalChannelAgentAllowed("discord", agentID, log) {
						if ds, cerr := buildChannel("discord", "discord", dsCfg, log); cerr != nil {
							log.Warn("discord channel skipped", zap.Error(cerr))
						} else {
							chanReg.Register(ds)
						}
					}
				}
			}
		}
	}

	// ── Slack ─────────────────────────────────────────────────────────────────
	// Same dual-mode as Telegram: single-bot (legacy) or multi-bot via `bots:`.
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
						sl, cerr := buildChannel("slack", adapterID, botMap, log)
						if cerr != nil {
							log.Warn("slack bot skipped", zap.String("adapter_id", adapterID), zap.Error(cerr))
							continue
						}
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
						if sl, cerr := buildChannel("slack", "slack", slCfg, log); cerr != nil {
							log.Warn("slack channel skipped", zap.Error(cerr))
						} else {
							chanReg.Register(sl)
						}
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
			// app_secret (HMAC verification on inbound webhook POSTs,
			// PRODUCTION_AUDIT → CRIT/Security) is consumed by the factory.
			phoneNumberID, _ := waCfg["phone_number_id"].(string)
			accessToken, _ := waCfg["access_token"].(string)
			verifyToken, _ := waCfg["verify_token"].(string)
			agentID, _ := waCfg["agent_id"].(string)
			if phoneNumberID != "" && accessToken != "" && verifyToken != "" {
				if externalChannelAgentAllowed("whatsapp", agentID, log) {
					if wa, cerr := buildChannel("whatsapp", "", waCfg, log); cerr != nil {
						log.Warn("whatsapp channel skipped", zap.Error(cerr))
					} else {
						// The gateway needs the concrete adapter for its
						// webhook routes; the factory contract returns the
						// channel.Adapter interface.
						waAdapter, _ = wa.(*wachan.Adapter)
						chanReg.Register(wa) // StartAll will call wa.Start()
					}
				}
			}
		}
	}

	// WhatsApp Web is an experimental QR-linked channel backed by a Node
	// sidecar (Baileys). Kept separate from the official Meta Cloud API
	// adapter above so deployments make an explicit tradeoff.
	if waWebCfg, ok := chanCfg["whatsapp_web"]; ok {
		if enabled, _ := waWebCfg["enabled"].(bool); enabled {
			command, _ := waWebCfg["command"].(string)
			args := channels.ParseStringList(waWebCfg["args"])
			sessionDir, _ := waWebCfg["session_dir"].(string)
			accountID, _ := waWebCfg["account_id"].(string)
			agentID, _ := waWebCfg["agent_id"].(string)
			activation := channels.ActivationFromConfig(waWebCfg, true)
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

	// ── Third-party registry channels (E10/E12) ──────────────────────────────
	// Any channels.<key> block whose key isn't handled above resolves through
	// the SDK factory registry under that key — this is how flavored-binary
	// drivers (docs/CUSTOM_DISTRIBUTIONS.md) wire from config with no host
	// changes. Unknown names warn and skip; the gateway always boots.
	for chID, chCfg := range chanCfg {
		switch chID {
		case "telegram", "discord", "slack", "whatsapp", "whatsapp_web", "http":
			continue
		}
		if enabled, _ := chCfg["enabled"].(bool); !enabled {
			continue
		}
		agentID, _ := chCfg["agent_id"].(string)
		if !externalChannelAgentAllowed(chID, agentID, log) {
			continue
		}
		a, cerr := buildChannel(chID, chID, chCfg, log)
		if cerr != nil {
			log.Warn("channel skipped (no registered factory or bad config)",
				zap.String("channel", chID), zap.Error(cerr))
			continue
		}
		chanReg.Register(a)
		log.Info("registry channel registered",
			zap.String("channel", chID), zap.String("agent_id", agentID))
	}

	if errs := chanReg.StartAll(ctx); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("channel start error", zap.Error(e))
		}
	}
	defer func() { _ = chanReg.StopAll() }()

	// Engine ← failure notifier. Run errors produce a real outbound
	// notification (per agent's notify_on_failure block). Wired AFTER
	// chanReg.StartAll so adapters are connected before the first cron tick
	// or HTTP request can trigger a failure path.
	engine.SetFailureNotifier(&failureNotifier{chanReg: chanReg, log: log})

	sched.Start()
	defer sched.Stop()

	// PRODUCTION_AUDIT → F2 (2026-05-27): replay any in-flight runs that the
	// previous gateway process didn't finish. Bounded to the last hour;
	// synchronous HTTP + cron triggers are skipped.
	replayIncompleteRuns(actionBackend, chanReg, log)

	// ── Message Router — bounded worker pool draining the shared inbox ──────
	// HTTP channel messages are handled synchronously via the HTTP handler;
	// all other channel messages flow through the engine here and are
	// replied to via chanReg.Send(). Concurrency bounded by
	// runtime.max_concurrent_sessions (default 100); per-run timeout uses
	// each agent's declared run_timeout. (PRODUCTION_AUDIT → CRITICAL/Concurrency)
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
				// Worker-pool saturation gauge. (PRODUCTION_AUDIT → MED/Observability)
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
	// Default "apikey" mode checks the static API key on every request;
	// auth.mode=jwt enables short-lived token issuance + optional OIDC.
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
	// Per-agent grants persist in SQLite; the static default policy lives in
	// the rbac package. NoopStore fallback = static-policy-only degradation.
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

	// Plugin GUI mounts + capability enforcement for scoped plugin tokens
	// (Story E8). Every loaded plugin's capability set registers with the
	// enforcer; GUI mounts surface in the shell nav.
	{
		capsEnforcer := caps.NewEnforcer(audit.New(cfg.Runtime.AuditDir), log)
		var uiMounts []gateway.PluginUIMount
		for _, lp := range pluginLoader.All() {
			if lp.Caps != nil {
				capsEnforcer.SetPluginSet(lp.Caps)
			}
			if staticDir, nav, ok := lp.GUIMount(); ok {
				uiMounts = append(uiMounts, gateway.PluginUIMount{
					ID: lp.Manifest.ID, StaticDir: staticDir,
					NavLabel: nav.Label, NavIcon: nav.Icon,
				})
			}
		}
		srv.SetCapEnforcer(capsEnforcer)
		if len(uiMounts) > 0 {
			srv.SetPluginUI(uiMounts)
			log.Info("plugin GUI mounts ready", zap.Int("count", len(uiMounts)))
		}
	}

	// Plugin install & management (Story E13): installer rooted at the first
	// plugin_dirs entry. Staged plugins live under <root>/.staging and never
	// load; activation requires explicit approval through the API/GUI.
	if len(cfg.PluginDirs) > 0 {
		if pins, pierr := plugininstall.New(cfg.PluginDirs[0]); pierr != nil {
			log.Warn("plugin installer unavailable", zap.Error(pierr))
		} else {
			srv.SetPluginInstaller(pins)
			log.Info("plugin installer ready", zap.String("dir", cfg.PluginDirs[0]))
		}
	}

	// Realtime voice control plane (Story 11, docs/VOICE_SPIKE.md). Only the
	// ephemeral-key minting lives host-side; audio is browser↔provider.
	if cfg.Voice.Provider == "openai" {
		voiceKey := os.Getenv("OPENAI_API_KEY")
		if oc, ok := cfg.LLM.Providers["openai"]; ok && oc.APIKey != "" {
			voiceKey = oc.APIKey
		}
		minter := voice.NewOpenAIMinter(voiceKey, cfg.Voice.Model, cfg.Voice.BaseURL)
		srv.SetVoiceMinter(minter)
		if ready, detail := minter.Ready(); ready {
			log.Info("realtime voice ready", zap.String("provider", "openai"), zap.String("model", minter.Model()))
		} else {
			log.Warn("realtime voice configured but not ready", zap.String("detail", detail))
		}
	} else if cfg.Voice.Provider != "" {
		log.Warn("unsupported voice provider; voice panel disabled",
			zap.String("provider", cfg.Voice.Provider))
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
