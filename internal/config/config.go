// Package config manages all Soulacy configuration.
// Config is loaded from ~/.soulacy/config.yaml (or SOULACY_CONFIG_PATH),
// then overridden by environment variables prefixed with SOULACY_.
// All subsystems receive a *Config at startup; no globals are used.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// Version is injected at build time via ldflags.
var Version = "dev"

// Config is the top-level configuration object.
type Config struct {
	// Server settings
	Server ServerConfig `mapstructure:"server"`

	// Agent runtime settings
	Runtime RuntimeConfig `mapstructure:"runtime"`

	// Memory layer settings
	Memory MemoryConfig `mapstructure:"memory"`

	// LLM provider settings
	LLM LLMConfig `mapstructure:"llm"`

	// Channel configurations — keyed by channel adapter ID
	Channels map[string]map[string]any `mapstructure:"channels"`

	// MCP (Model Context Protocol) servers — keyed by server ID.
	// Tools from connected MCP servers are auto-injected into every agent's
	// tool list with namespaced names: mcp__<server>__<tool>.
	MCP MCPConfig `mapstructure:"mcp"`

	// Plugin directories to scan
	PluginDirs []string `mapstructure:"plugin_dirs"`

	// PluginsConfig collects arbitrary plugin-specific settings (Story E17),
	// keyed by plugin ID. The shape under each key is owned entirely by the
	// plugin — the core parser never validates it, so plugins can grow
	// settings without core changes or unmarshalling errors:
	//
	//	plugins_config:
	//	  weather-bot:
	//	    units: metric
	//	    cache_ttl: 15m
	//
	// Exposed to plugin wiring via plugins.Wire (WireDeps.PluginsConfig);
	// the gateway config API redacts secret-looking values before they
	// reach the browser.
	PluginsConfig map[string]map[string]any `mapstructure:"plugins_config"`

	// Registries lists package registries for skill/plugin installs
	// (Story E19). Entries are queried in ascending Priority order with
	// fallback: the first registry that resolves a slug wins.
	//
	//	registries:
	//	  - id: main
	//	    type: http
	//	    base_url: https://registry.example.com
	//	    priority: 10
	//	    auth_headers:
	//	      Authorization: "Bearer abc123"
	//	  - id: github
	//	    type: git
	//	    priority: 100
	Registries []RegistryConfig `mapstructure:"registries"`

	// Agent definition directories to scan
	AgentDirs []string `mapstructure:"agent_dirs"`

	// Skill directories to scan (in addition to the default ~/.soulacy/skills/ and ~/.agents/skills/)
	SkillDirs []string `mapstructure:"skill_dirs"`

	// Knowledge (RAG) settings — see KnowledgeConfig for details.
	Knowledge KnowledgeConfig `mapstructure:"knowledge"`

	// Storage backend — see StorageConfig for details.
	Storage StorageConfig `mapstructure:"storage"`

	// Vector backend — see VectorConfig for details.
	// (Overrides Memory.VectorDB when set.)
	Vector VectorConfig `mapstructure:"vector"`

	// Executor backend — see ExecutorConfig for details.
	Executor ExecutorConfig `mapstructure:"executor"`

	// Queue backend — see QueueConfig for details.
	Queue QueueConfig `mapstructure:"queue"`

	// Auth controls request authentication. See AuthConfig for details.
	Auth AuthConfig `mapstructure:"auth"`

	// Credentials configures the encrypted credential vault.
	Credentials CredentialsConfig `mapstructure:"credentials"`

	// Telemetry configures OpenTelemetry tracing.
	Telemetry TelemetryConfig `mapstructure:"telemetry"`

	// Costs configures token usage cost estimation. Usage is always recorded
	// when the cost store is available; pricing entries are optional and only
	// affect the estimated cost_usd fields.
	Costs CostConfig `mapstructure:"costs"`

	// Ops configures production reliability guardrails for recent runs.
	Ops OpsConfig `mapstructure:"ops"`

	// Updates configures release manifest discovery for `sy update` and the
	// launch-readiness checklist.
	Updates UpdateConfig `mapstructure:"updates"`

	// RateLimit configures per-user and per-agent rate limiting (Task #33).
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`

	// Hooks are signed outbound webhooks fed by the event stream (story E2,
	// schema contract in docs/EVENTS.md).
	Hooks []HookConfig `mapstructure:"hooks"`

	// Voice configures the realtime voice control plane (Story 11,
	// docs/VOICE_SPIKE.md). Empty provider = voice panel disabled.
	Voice VoiceConfig `mapstructure:"voice"`

	// Search configures the built-in web_search tool.
	Search SearchConfig `mapstructure:"search"`

	// Logging
	Log LogConfig `mapstructure:"log"`
}

// VoiceConfig selects the realtime voice provider for the Chat panel.
//
//	voice:
//	  provider: openai            # only "openai" is supported (v1)
//	  model: gpt-realtime-mini    # default
//	  base_url: ""                # override for Azure/compatible endpoints
//
// The API key comes from llm.providers.openai.api_key or OPENAI_API_KEY.
type VoiceConfig struct {
	Provider string `mapstructure:"provider"`
	Model    string `mapstructure:"model"`
	BaseURL  string `mapstructure:"base_url"`
}

// HookConfig declares one outbound webhook endpoint.
//
//	hooks:
//	  - on: [run.failed, "tool.*"]    # event types; "*" = all, "x.*" = prefix
//	    agents: [support-bot]          # optional agent filter (empty = all)
//	    url: https://ops.example.com/soulacy
//	    secret_env: SOULACY_HOOK_SECRET  # env var holding the HMAC secret
type HookConfig struct {
	On        []string `mapstructure:"on"`
	Agents    []string `mapstructure:"agents"`
	URL       string   `mapstructure:"url"`
	SecretEnv string   `mapstructure:"secret_env"`
}

// RateLimitConfig controls per-user and per-agent request/token quotas.
//
//	enabled:               true
//	per_user_rpm:          60     — max API requests/min per JWT subject (0 = off)
//	per_agent_rpm:         0      — max requests/min to one agent across all users (0 = off)
//	per_user_tokens_day:   0      — max LLM tokens/day per user (0 = off)
//	per_agent_tokens_day:  0      — max LLM tokens/day per agent across all users (0 = off)
//	backend:               memory — or "redis" (multi-instance; requires redis_url)
//	redis_url:             redis://localhost:6379
type RateLimitConfig struct {
	Enabled           bool   `mapstructure:"enabled"`
	PerUserRPM        int    `mapstructure:"per_user_rpm"`
	PerAgentRPM       int    `mapstructure:"per_agent_rpm"`
	PerUserTokensDay  int    `mapstructure:"per_user_tokens_day"`
	PerAgentTokensDay int    `mapstructure:"per_agent_tokens_day"`
	Backend           string `mapstructure:"backend"`
	RedisURL          string `mapstructure:"redis_url"`
}

// TelemetryConfig holds OpenTelemetry tracing configuration.
type TelemetryConfig struct {
	Enabled      bool   `mapstructure:"enabled"`
	Exporter     string `mapstructure:"exporter"`      // "otlp_grpc" | "otlp_http" | "stdout"
	OTLPEndpoint string `mapstructure:"otlp_endpoint"` // e.g. "localhost:4317"
	ServiceName  string `mapstructure:"service_name"`  // default "soulacy"
}

// CostConfig controls estimated dollar costs for recorded token usage.
//
// Pricing keys are matched as provider/model, provider/*, then */model.
// Values are USD per 1 million tokens. Unknown providers/models still record
// token usage, but cost_usd remains 0 until an operator configures pricing.
//
//	costs:
//	  daily_budget_usd: 25
//	  monthly_budget_usd: 500
//	  alert_threshold: 0.8
//	  pricing:
//	    openai/gpt-4.1-mini:
//	      input_per_mtok: 0.40
//	      output_per_mtok: 1.60
//	    omniroute/*:
//	      input_per_mtok: 0.25
//	      output_per_mtok: 0.75
type CostConfig struct {
	DailyBudgetUSD   float64                `mapstructure:"daily_budget_usd"`
	MonthlyBudgetUSD float64                `mapstructure:"monthly_budget_usd"`
	AlertThreshold   float64                `mapstructure:"alert_threshold"`
	Pricing          map[string]CostPricing `mapstructure:"pricing"`
}

// OpsConfig controls launch-readiness SLO checks over the durable action log.
//
//	ops:
//	  slo_window: 24h
//	  max_failure_rate: 0.1
//	  max_incomplete_rate: 0.05
//	  max_p95_run_duration: 5m
//	  min_runs_for_signal: 3
type OpsConfig struct {
	SLOWindow         string  `mapstructure:"slo_window"`
	MaxFailureRate    float64 `mapstructure:"max_failure_rate"`
	MaxIncompleteRate float64 `mapstructure:"max_incomplete_rate"`
	MaxP95RunDuration string  `mapstructure:"max_p95_run_duration"`
	MinRunsForSignal  int     `mapstructure:"min_runs_for_signal"`
}

// UpdateConfig points Soulacy at a signed or checksum-backed release manifest.
//
//	updates:
//	  manifest_url: https://releases.example.com/soulacy/release-manifest.json
type UpdateConfig struct {
	ManifestURL string `mapstructure:"manifest_url"`
}

// CostPricing is the YAML face of internal/costs.Pricing.
type CostPricing struct {
	InputPerMTok  float64 `mapstructure:"input_per_mtok"`
	OutputPerMTok float64 `mapstructure:"output_per_mtok"`
}

// CredentialsConfig holds credential vault settings.
type CredentialsConfig struct {
	KMSProvider    string `mapstructure:"kms_provider"` // "local" (default), "hashicorp", "awskms"
	HashiCorpAddr  string `mapstructure:"hashicorp_addr"`
	HashiCorpToken string `mapstructure:"hashicorp_token"`
	AWSKMSKeyID    string `mapstructure:"aws_kms_key_id"`
}

type ServerConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	GUIEnabled   bool   `mapstructure:"gui_enabled"`
	GUIStaticDir string `mapstructure:"gui_static_dir"`
	APIKey       string `mapstructure:"api_key"` // gateway auth key; empty = no auth
	TLSCert      string `mapstructure:"tls_cert"`
	TLSKey       string `mapstructure:"tls_key"`

	// AllowUnauthenticated explicitly permits starting with no API key while
	// bound to a non-loopback address. Without it, such a configuration is a
	// hard error (SEC-4) — an open gateway on a public interface is almost
	// never intended. Settable via config, SOULACY_SERVER_ALLOW_UNAUTHENTICATED,
	// or the `--allow-unauthenticated` flag.
	AllowUnauthenticated bool `mapstructure:"allow_unauthenticated"`

	// AllowedOrigins is an explicit allow-list of CORS origins. When empty,
	// only the gateway's own UI origin (`http://<host>:<port>`) is allowed —
	// no localhost:3000 / 5173 dev-server escape hatch. (PRODUCTION_AUDIT
	// → LOW/Config) Set explicitly in production.
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

type RuntimeConfig struct {
	MaxConcurrentSessions int    `mapstructure:"max_concurrent_sessions"`
	DefaultMaxTurns       int    `mapstructure:"default_max_turns"`
	PythonBin             string `mapstructure:"python_bin"`   // path to python3 interpreter
	ToolTimeout           string `mapstructure:"tool_timeout"` // e.g. "30s"

	// AdaptiveNodes is the global default for runtime LLM salvage of flow nodes
	// that hit an unexpected data shape (see FlowNode.Adaptive). nil = enabled
	// (the flow tries to keep running through surprises); set false to require
	// per-node opt-in only. Salvage is bounded (one attempt/node) and never fires
	// for auth/network/consent failures — only shape/format surprises.
	//
	//	runtime:
	//	  adaptive_nodes: false
	AdaptiveNodes *bool `mapstructure:"adaptive_nodes"`

	// MaxTurnsCeiling is a hard server-side cap on an agent's max_turns
	// (Story 1 / S3.2). A single inbound message can fan out to
	// depth × max_turns × tool-calls LLM requests; without a ceiling a
	// misconfigured agent (max_turns: 10000) is a cost/runaway foot-gun.
	// The engine clamps each agent's effective max_turns to this value.
	// Zero/negative defaults to 50.
	MaxTurnsCeiling int `mapstructure:"max_turns_ceiling"`

	// MaxAgentCallDepth bounds recursive peer-agent chains such as
	// orchestrator -> researcher -> reviewer. Zero defaults to the runtime
	// default (5). Increase carefully for deeper team hierarchies; the
	// chain-wide run_timeout still applies.
	MaxAgentCallDepth int `mapstructure:"max_agent_call_depth"`

	// Sandbox controls the per-Python-tool resource caps applied via the
	// soulacy __exec-sandbox wrapper. Zero values mean "no limit for
	// that knob"; enabling the sandbox without configuring any limits is
	// equivalent to disabled.
	// (PRODUCTION_AUDIT → F1, 2026-05-27)
	Sandbox SandboxConfig `mapstructure:"sandbox"`

	// AllowSystemAgents is the SERVER-LEVEL permit for the destructive OS-level
	// "system" built-ins (shell_exec, run_script, install_library, write_file,
	// download_file). This now defaults to ["*"] to rely on the agent-level opt-in.
	// Even when an agent is listed here, it only receives system tools if it ALSO declares
	// the "system" capability (capabilities: [system] in SOUL.yaml). The two
	// gates are independent: the server permits, the agent opts in.
	//
	// NOTE: the read-only "safe" built-ins (read_file, list_dir, find_files,
	// http_request, fetch_url, env_get, sys_info) are NOT governed by this flag
	// — they are always available, see Engine.buildSystemTools / safeSystemTools.
	AllowSystemAgents []string `mapstructure:"allow_system_agents"`

	// SSRFProtection, when true, blocks HTTP tools (fetch_url, http_request,
	// download_file) from reaching private RFC-1918 address ranges and the
	// link-local metadata endpoint (169.254.0.0/16). Loopback (127.x / ::1)
	// is always allowed so local MCP servers still work.
	// Default false for single-user; enable for multi-tenant deployments.
	SSRFProtection bool `mapstructure:"ssrf_protection"`

	// AllowPrivateHosts lists hostnames/IPs reachable even when SSRFProtection
	// is true (e.g. an internal API server the agent legitimately needs).
	AllowPrivateHosts []string `mapstructure:"allow_private_hosts"`

	// AuditDir is where the OPTIONAL JSONL tool-call audit log is written.
	// Each session gets <AuditDir>/<date>/<sessionID>.jsonl.
	//
	// DOC-4: this JSONL log is debug/convenience output and defaults OFF
	// (empty string). The authoritative incident-reconstruction record is the
	// SQLite action log (internal/actionlog), which is always on regardless of
	// this setting. Set AuditDir to a path (e.g. <workspace>/audit) to ALSO
	// emit the redundant per-session JSONL files. See docs/security/audit.md.
	AuditDir string `mapstructure:"audit_dir"`

	// AllowedToolDirs is an allowlist of directory prefixes that python_file
	// tool paths must resolve under. If the list is non-empty, any python_file
	// path that does not start with one of these prefixes is rejected before the
	// subprocess is forked — preventing a SOUL.yaml with a crafted python_file
	// from reading or executing arbitrary files on the host.
	//
	// Example config.yaml:
	//   runtime:
	//     allowed_tool_dirs:
	//       - /Users/you/.soulacy/agents
	//       - /Users/you/.soulacy/tools
	//
	// Default (empty list): all paths are permitted (single-user / trusted
	// operator deployments where every SOUL.yaml author is already trusted).
	AllowedToolDirs []string `mapstructure:"allowed_tool_dirs"`

	// SessionTTL bounds how long an idle in-memory session is retained before
	// the engine's eviction sweep reclaims it. Sessions that have not been
	// touched (no inbound message) within this window are persisted (if a
	// memory backend is present) and then dropped from the in-memory map.
	// Empty/zero defaults to 24h. (PERF-1 — session eviction)
	SessionTTL string `mapstructure:"session_ttl"` // e.g. "24h"

	// MaxSessions caps the number of live in-memory sessions. When the map
	// exceeds this count, the eviction sweep drops the oldest-idle sessions
	// (never one mid-conversation) until the count is back under the cap.
	// Zero/negative defaults to 10000. (PERF-1 — session eviction)
	MaxSessions int `mapstructure:"max_sessions"`

	// MaxHistoryTurns bounds the number of conversation turns kept in a
	// session's in-memory History. When appends push history past this many
	// non-system messages, the oldest are trimmed first — the system prompt
	// (if present at index 0) is always preserved. Zero/negative defaults to
	// 100. (PERF-2 — history windowing)
	MaxHistoryTurns int `mapstructure:"max_history_turns"`
}

// SandboxConfig is the YAML face of internal/sandbox.Limits.
type SandboxConfig struct {
	Enabled    bool `mapstructure:"enabled"`
	CPUSeconds int  `mapstructure:"cpu_seconds"`
	MemoryMB   int  `mapstructure:"memory_mb"`
	OpenFiles  int  `mapstructure:"open_files"`
	FileSizeMB int  `mapstructure:"file_size_mb"`
}

type MemoryConfig struct {
	Dir        string `mapstructure:"dir"`         // base directory for file memory
	SQLitePath string `mapstructure:"sqlite_path"` // path to SQLite archive
	VectorDB   string `mapstructure:"vector_db"`   // "sqlite-vec" (built-in) or "" (disabled)
	VectorURL  string `mapstructure:"vector_url"`
	VectorDims int    `mapstructure:"vector_dims"` // embedding dimensions (default 768 for nomic-embed-text)
	MaxHistory int    `mapstructure:"max_history"` // max messages to keep in hot memory
}

// StorageConfig selects the durable event-log and memory-archive backend.
//
//	backend:     "sqlite"    — embedded SQLite (default, zero-dependency)
//	             "postgres"  — PostgreSQL via pgx/v5
//	postgres_dsn: "postgres://user:pass@host:5432/soulacy?sslmode=disable"
//	postgres_log_dir: path where per-agent .log mirror files are written
//	             (defaults to the same directory as Memory.Dir)
type StorageConfig struct {
	Backend        string   `mapstructure:"backend"`          // "sqlite" (default), "postgres", or "external"
	PostgresDSN    string   `mapstructure:"postgres_dsn"`     // libpq connection string
	PostgresLogDir string   `mapstructure:"postgres_log_dir"` // per-agent .log mirror directory
	Command        string   `mapstructure:"command"`          // external sidecar command (E24)
	Args           []string `mapstructure:"args"`             // external sidecar args (E24)
}

// VectorConfig selects the semantic vector-search backend.
// When empty, falls back to Memory.VectorDB for backwards compatibility.
//
//	backend:     "sqlite-vec"  — built-in sqlite-vec (default)
//	             "qdrant"      — Qdrant REST API
//	             "external"    — storage sidecar over stdio (E24)
//	url:         "http://localhost:6333"   (Qdrant only)
//	collection:  "soulacy_memory"        (Qdrant only)
//	api_key:     ""                        (Qdrant optional auth)
//	dims:        768                       (must match your embedder)
//	command:     "/path/to/sidecar"        (external only)
//	args:        ["--flag"]                (external only)
type VectorConfig struct {
	Backend    string   `mapstructure:"backend"`    // "sqlite-vec", "qdrant", or "external"
	URL        string   `mapstructure:"url"`        // Qdrant base URL
	Collection string   `mapstructure:"collection"` // Qdrant collection name
	APIKey     string   `mapstructure:"api_key"`    // Qdrant API key (optional)
	Dims       int      `mapstructure:"dims"`       // embedding dimensionality
	Command    string   `mapstructure:"command"`    // external sidecar command (E24)
	Args       []string `mapstructure:"args"`       // external sidecar args (E24)
}

// ExecutorConfig selects the Python tool executor backend.
//
//	backend:  "process"  — one python3 subprocess per call (default, simple)
//	          "pool"     — N pre-forked persistent workers (low-latency)
//	          "docker"   — one short-lived Docker container per call
//	          "ssh"      — run Python on a remote host over the system ssh client
//	workers:  4          — pool only: number of pre-forked Python processes
type ExecutorConfig struct {
	Backend       string `mapstructure:"backend"`        // "process" (default), "pool", "docker", or "ssh"
	Workers       int    `mapstructure:"workers"`        // pool only: worker count
	DockerImage   string `mapstructure:"docker_image"`   // docker only: image to run
	DockerNetwork string `mapstructure:"docker_network"` // docker only: defaults to none
	SSHHost       string `mapstructure:"ssh_host"`       // ssh only: host or user@host
	SSHUser       string `mapstructure:"ssh_user"`       // ssh only: optional user
	SSHPythonBin  string `mapstructure:"ssh_python_bin"` // ssh only: remote python binary
	SSHIdentity   string `mapstructure:"ssh_identity"`   // ssh only: private key path

	// DockerVolumes is the explicit mount allowlist for the docker backend
	// (each entry "host:container[:ro]"). No host paths are mounted unless
	// listed here, so the operator fully controls container disk access.
	DockerVolumes []string `mapstructure:"docker_volumes"`

	// SSHIdentityCredential, when set, names a secret in the encrypted vault
	// holding the SSH private key. It is resolved at startup and written to a
	// 0600 temp file used as the ssh identity — keeping the key out of config
	// files and the process environment. Takes precedence over SSHIdentity.
	SSHIdentityCredential string `mapstructure:"ssh_identity_credential"`

	// CloudPreset registers a cloud-sandbox execution backend under its own name
	// (one of: modal, runpod, daytona) that agents can select via
	// execution.backend. CloudTarget is the provider handle (pod id / workspace /
	// app); CloudCLI optionally overrides the provider CLI binary. The chosen
	// provider CLI must be installed and authenticated on the host.
	CloudPreset string `mapstructure:"cloud_preset"`
	CloudTarget string `mapstructure:"cloud_target"`
	CloudCLI    string `mapstructure:"cloud_cli"`
}

// AuthConfig controls API request authentication.
//
//	mode:            "apikey"  — static bearer token (default, backwards-compatible)
//	                 "jwt"     — locally-issued short-lived JWTs; static key still accepted
//	jwt_secret:      HMAC-SHA256 signing key (required when mode=jwt; if empty an ephemeral
//	                 key is generated — tokens are invalidated on restart).
//	                 Set to a stable 32-byte hex string in production.
//	jwt_access_ttl:  "15m"    — access token lifetime
//	jwt_refresh_ttl: "168h"   — refresh token lifetime (7 days)
//	oidc_issuer:     ""       — OIDC provider base URL (e.g. https://accounts.google.com)
//	                 When set, JWTs from this issuer are also accepted.
//	oidc_audience:   ""       — expected `aud` claim; defaults to oidc_client_id
//	oidc_client_id:  ""       — used as audience fallback when oidc_audience is empty
type AuthConfig struct {
	Mode          string `mapstructure:"mode"`            // "apikey" or "jwt"
	JWTSecret     string `mapstructure:"jwt_secret"`      // HMAC signing key
	JWTAccessTTL  string `mapstructure:"jwt_access_ttl"`  // e.g. "15m"
	JWTRefreshTTL string `mapstructure:"jwt_refresh_ttl"` // e.g. "168h"
	OIDCIssuer    string `mapstructure:"oidc_issuer"`     // provider discovery URL
	OIDCAudience  string `mapstructure:"oidc_audience"`   // aud claim value
	OIDCClientID  string `mapstructure:"oidc_client_id"`  // audience fallback
}

// QueueConfig selects the durable message queue backend.
//
//	backend:             "memory"  — in-process channel-based (default, zero-dependency)
//	                     "nats"    — NATS JetStream (durable, multi-instance)
//	nats_url:            "nats://localhost:4222"
//	                     Comma-separated list accepted for cluster deployments.
//	nats_stream:         "soulacy"
//	                     Name of the JetStream stream that owns Soulacy subjects.
//	nats_subject_prefix: ""
//	                     Subject filter applied to the stream. Defaults to "<stream>.>".
//	nats_ack_wait:       "30s"
//	                     How long JetStream waits for an Ack before redelivering.
//	nats_max_deliver:    0
//	                     Max delivery attempts per message; 0 = unlimited.
//	command/args:        external sidecar process (backend: "external", E24)
type QueueConfig struct {
	Backend           string   `mapstructure:"backend"`             // "memory" (default), "nats", or "external"
	NATSUrl           string   `mapstructure:"nats_url"`            // NATS server URL
	NATSStream        string   `mapstructure:"nats_stream"`         // JetStream stream name
	NATSSubjectPrefix string   `mapstructure:"nats_subject_prefix"` // subjects filter
	NATSAckWait       string   `mapstructure:"nats_ack_wait"`       // e.g. "30s"
	NATSMaxDeliver    int      `mapstructure:"nats_max_deliver"`    // 0 = unlimited
	Command           string   `mapstructure:"command"`             // external sidecar command (E24)
	Args              []string `mapstructure:"args"`                // external sidecar args (E24)
}

type LLMConfig struct {
	DefaultProvider string                    `mapstructure:"default_provider"`
	Providers       map[string]ProviderConfig `mapstructure:"providers"`

	// Studio optionally overrides which provider/model the Studio visual
	// builder uses for its COMPILE reasoning (turning a plain-language intent
	// into a workflow graph, and — soon — authoring Python tool scripts).
	// Compilation is reasoning-heavy, so operators can point Studio at a
	// stronger model than the global default without changing it for every
	// agent. Empty fields fall back to DefaultProvider and that provider's Model.
	//
	//	llm:
	//	  default_provider: google
	//	  studio:
	//	    provider: anthropic
	//	    model: claude-opus-4-8
	Studio StudioLLMConfig `mapstructure:"studio"`

	// Reasoner optionally overrides which provider/model the multi-step
	// reasoning loop (ReAct / Plan-Execute) uses for its think/plan/reflect
	// calls — independent of the model an agent uses to chat. Planning needs
	// reliable structured-JSON output, so operators can point it at a stronger
	// model than the agent runs on. Empty fields fall back to the agent's own
	// llm.provider/model. Only providers with a reasoning backend are honored
	// (anthropic / openai-compatible / ollama).
	//
	//	llm:
	//	  reasoner:
	//	    provider: anthropic
	//	    model: claude-opus-4-8
	Reasoner StudioLLMConfig `mapstructure:"reasoner"`
}

// StudioLLMConfig overrides the provider/model used for Studio workflow
// compilation. Both fields are optional and independently fall back to the
// global default.
type StudioLLMConfig struct {
	Provider string `mapstructure:"provider"`
	Model    string `mapstructure:"model"`
	// Learning toggles whether Studio records lessons from accepted live-run
	// repairs and injects them into future workflow generation so it builds
	// flows that work the first time. nil = enabled (default); set false to
	// opt out. Only meaningful on the `studio` block (ignored on `reasoner`).
	//
	//	llm:
	//	  studio:
	//	    learning: false
	Learning *bool `mapstructure:"learning"`
}

type ProviderConfig struct {
	BaseURL       string         `mapstructure:"base_url"`
	APIKey        string         `mapstructure:"api_key"`
	Model         string         `mapstructure:"model"`
	KeepAlive     string         `mapstructure:"keep_alive"`
	Options       map[string]any `mapstructure:"options"`
	PromptCaching bool           `mapstructure:"prompt_caching"` // cache system prompt + tools between turns (provider support varies; Anthropic: 90% discount on cache hits)

	// ── Google-specific ──────────────────────────────────────────────────────
	// ThinkingBudget controls Gemini 2.5 extended thinking.
	//   0  = disabled (default — fast, no reasoning trace)
	//  -1  = auto (model decides)
	//   N  = up to N tokens of reasoning
	ThinkingBudget int `mapstructure:"thinking_budget"`
	// SafetyLevel sets Gemini content-filter thresholds.
	//   ""/"default" = Gemini defaults
	//   "off"        = BLOCK_NONE on all categories (needed for most agent work)
	//   "strict"     = BLOCK_LOW_AND_ABOVE
	SafetyLevel string `mapstructure:"safety_level"`

	// ── Anthropic-specific ───────────────────────────────────────────────────
	// ExtendedThinking enables Claude 3.7+ extended thinking (beta).
	// ThinkingBudget (shared field above) sets the token budget when this is on.
	ExtendedThinking bool `mapstructure:"extended_thinking"`

	// ── OpenAI / compatible ──────────────────────────────────────────────────
	// Organization is the OpenAI-Organization header value (enterprise/team accounts).
	Organization string `mapstructure:"organization"`
	// ParallelToolCalls controls whether the model may call multiple tools in one
	// turn. nil = provider default (usually true). false = serialize tool calls,
	// which reduces agent loop failures on weaker models.
	ParallelToolCalls *bool `mapstructure:"parallel_tool_calls"`
}

// KnowledgeConfig holds RAG defaults.
//
//	db_path                ~/.soulacy/knowledge.db
//	embedding_provider     "ollama" (default) — see internal/llm/embed.go
//	embedding_model        "nomic-embed-text"
//	chunk_size             1000 characters
//	chunk_overlap          200 characters
//	max_document_bytes     52428800 (50 MiB)
type KnowledgeConfig struct {
	DBPath            string `mapstructure:"db_path"`
	EmbeddingProvider string `mapstructure:"embedding_provider"`
	EmbeddingModel    string `mapstructure:"embedding_model"`
	ChunkSize         int    `mapstructure:"chunk_size"`
	ChunkOverlap      int    `mapstructure:"chunk_overlap"`
	MaxDocumentBytes  int64  `mapstructure:"max_document_bytes"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`  // debug, info, warn, error
	Format string `mapstructure:"format"` // json, console
	File   string `mapstructure:"file"`   // path; empty = stdout only
}

// MCPConfig groups configured MCP servers.
type MCPConfig struct {
	Servers map[string]MCPServerConfig `mapstructure:"servers"`
}

// MCPServerConfig describes one MCP server connection.
type MCPServerConfig struct {
	Transport string            `mapstructure:"transport"` // "stdio" (default) or "http"
	Command   string            `mapstructure:"command"`   // stdio: executable
	Args      []string          `mapstructure:"args"`      // stdio: arguments
	Env       map[string]string `mapstructure:"env"`       // stdio: extra env
	URL       string            `mapstructure:"url"`       // http: server URL
	Headers   map[string]string `mapstructure:"headers"`   // http: extra headers
}

// RegistryConfig describes one package registry for skill/plugin installs
// (Story E19). Provider construction goes through the SDK factory registry
// (registry.NewPkgRegistry) keyed by Type, so flavored binaries can ship
// custom registry providers.
type RegistryConfig struct {
	// ID is the operator-chosen name shown in consent dialogs. Defaults to
	// Type when empty.
	ID string `mapstructure:"id"`
	// Type selects the provider factory: "http" (default) or "git".
	Type string `mapstructure:"type"`
	// BaseURL is the registry root for http providers
	// (e.g. https://registry.example.com).
	BaseURL string `mapstructure:"base_url"`
	// Priority orders multi-registry resolution — LOWER runs first.
	// Entries with equal priority keep config order.
	Priority int `mapstructure:"priority"`
	// AuthHeaders are sent verbatim on every http request to this registry.
	AuthHeaders map[string]string `mapstructure:"auth_headers"`
	// SigningKey is the registry operator's hex-encoded 32-byte ed25519
	// public key. When set, EVERY package from this registry must carry a
	// valid signature over its archive sha256 digest — unsigned packages
	// are refused.
	SigningKey string `mapstructure:"signing_key"`
}

// Load reads configuration from disk and environment, returning a validated Config
// and the resolved configuration file path.
func Load(cfgPath string) (*Config, string, error) {
	v := viper.New()

	// Defaults — default to loopback to keep first-run gateways off the LAN.
	// (PRODUCTION_AUDIT → CRITICAL/Security: default-open posture). Users
	// explicitly opt into 0.0.0.0 via config or the SOULACY_SERVER_HOST env
	// var; main.go additionally refuses to start when bound to a non-loopback
	// address with no API key set.
	v.SetDefault("server.host", "127.0.0.1")
	v.SetDefault("server.port", 18789)
	v.SetDefault("server.gui_enabled", true)
	v.SetDefault("runtime.max_concurrent_sessions", 100)
	v.SetDefault("runtime.default_max_turns", 20)
	v.SetDefault("runtime.max_turns_ceiling", 50)
	v.SetDefault("runtime.max_agent_call_depth", 5)
	v.SetDefault("runtime.python_bin", "python3")
	// PRODUCTION_AUDIT → LOW/Config: NotebookLM-style tools regularly take
	// minutes; the old 30s default silently SIGKILLed them unless every
	// agent declared a per-tool override. 120s is the new sane floor;
	// per-tool override at `tools[i].timeout` still applies.
	v.SetDefault("runtime.tool_timeout", "120s")
	// SEC-3: default to ["*"]. Destructive system tools (shell_exec, run_script,
	// write_file, …) now require BOTH this server permit AND a per-agent
	// `capabilities: [system]` declaration. Breaking change — see CHANGELOG.
	v.SetDefault("runtime.allow_system_agents", []string{"*"})
	v.SetDefault("runtime.ssrf_protection", false)
	v.SetDefault("runtime.session_ttl", "24h")
	v.SetDefault("runtime.max_sessions", 10000)
	v.SetDefault("runtime.max_history_turns", 100)
	// PRODUCTION_AUDIT → F1: sandbox defaults ON with conservative caps
	// suitable for typical agent tools. Disable per-deployment by setting
	// runtime.sandbox.enabled=false. Limits = 0 means "no cap for that knob"
	// so an operator can relax only the constraints they need.
	v.SetDefault("runtime.sandbox.enabled", true)
	v.SetDefault("runtime.sandbox.cpu_seconds", 30)
	v.SetDefault("runtime.sandbox.memory_mb", 512)
	v.SetDefault("runtime.sandbox.open_files", 256)
	v.SetDefault("runtime.sandbox.file_size_mb", 64)
	v.SetDefault("memory.max_history", 50)
	v.SetDefault("memory.vector_db", "")
	v.SetDefault("memory.vector_dims", 768)
	v.SetDefault("storage.backend", "sqlite")
	v.SetDefault("vector.backend", "") // empty → inherit from memory.vector_db
	v.SetDefault("vector.dims", 768)
	v.SetDefault("executor.backend", "process")
	v.SetDefault("executor.workers", 4)
	v.SetDefault("executor.docker_image", "python:3.12-slim")
	v.SetDefault("executor.docker_network", "none")
	v.SetDefault("executor.ssh_python_bin", "python3")
	v.SetDefault("queue.backend", "memory")
	v.SetDefault("queue.nats_url", "nats://localhost:4222")
	v.SetDefault("queue.nats_stream", "soulacy")
	v.SetDefault("queue.nats_ack_wait", "30s")
	v.SetDefault("queue.nats_max_deliver", 0)
	v.SetDefault("auth.mode", "apikey")
	v.SetDefault("auth.jwt_access_ttl", "15m")
	v.SetDefault("auth.jwt_refresh_ttl", "168h")
	v.SetDefault("llm.default_provider", "ollama")
	v.SetDefault("llm.providers.ollama.base_url", "http://localhost:11434")
	v.SetDefault("llm.providers.ollama.model", "llama3")
	v.SetDefault("knowledge.embedding_provider", "ollama")
	v.SetDefault("knowledge.embedding_model", "nomic-embed-text")
	v.SetDefault("knowledge.chunk_size", 1000)
	v.SetDefault("knowledge.chunk_overlap", 200)
	v.SetDefault("knowledge.max_document_bytes", int64(50<<20))
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "console")

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	ws, wsErr := ResolveWorkspace()
	if wsErr != nil {
		return nil, "", fmt.Errorf("resolving workspace: %w", wsErr)
	}
	setHomeDefaults(v, ws)

	// Config file
	if cfgPath != "" {
		v.SetConfigFile(cfgPath)
	} else {
		// Search the WORKSPACE first — the installed config always wins —
		// then the legacy flat ~/.soulacy, and only then the CWD. A stray
		// project-level config.yaml in whatever directory the gateway was
		// launched from must never shadow a real installation (it used to:
		// a repo checkout's dev config silently hijacked fresh installs).
		// Dev override: set SOULACY_CONFIG_PATH=./config.yaml explicitly.
		v.AddConfigPath(ws.Root)
		v.AddConfigPath(filepath.Join(home, ".soulacy"))
		v.AddConfigPath(".")
		v.SetConfigName("config")
		v.SetConfigType("yaml")
	}

	// Environment overrides: SOULACY_SERVER_PORT=8080, SOULACY_SERVER_API_KEY=…, etc.
	// SetEnvKeyReplacer converts dot-separated viper keys to underscored env var names:
	//   server.api_key  →  SOULACY_SERVER_API_KEY
	v.SetEnvPrefix("SOULACY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		// Config file is optional on first run
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, "", fmt.Errorf("reading config: %w", err)
		}
	}

	resolvedPath := v.ConfigFileUsed()
	if resolvedPath == "" {
		resolvedPath = ws.ConfigFile
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, "", fmt.Errorf("unmarshalling config: %w", err)
	}

	// Viper lowercases EVERY key, which corrupts case-sensitive map *values*
	// such as MCP server env-var names (e.g. LETSFG_PYTHON → letsfg_python) and
	// HTTP header names. The spawned MCP process then can't find the variable it
	// expects, and the GUI shows the mangled name. Re-read those specific maps
	// straight from the YAML file (yaml.v3 preserves case) and overwrite the
	// lowercased copies. No-op when no file was read (first run, env-only).
	restoreCaseSensitiveMaps(cfg, resolvedPath)

	// Story 5 / S8.1: strict fail-fast validation. A bad duration or an
	// out-of-range numeric must produce a loud startup error, not a silent
	// fallback to a default that masks the operator's mistake.
	if err := cfg.Validate(); err != nil {
		return nil, "", err
	}

	return cfg, resolvedPath, nil
}

// restoreCaseSensitiveMaps fixes the keys Viper lowercased on load for the maps
// where case is significant: each MCP server's `env` (environment variable
// names are case-sensitive on Unix) and `headers` (HTTP header *values* are
// matched case-insensitively, but some servers care). It re-reads only those
// maps from the raw YAML at path and overwrites the lowercased versions in cfg,
// matching servers case-insensitively (Viper also lowercased the server IDs).
func restoreCaseSensitiveMaps(cfg *Config, path string) {
	if path == "" || cfg == nil || len(cfg.MCP.Servers) == 0 {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw struct {
		MCP struct {
			Servers map[string]struct {
				Env     map[string]string `yaml:"env"`
				Headers map[string]string `yaml:"headers"`
			} `yaml:"servers"`
		} `yaml:"mcp"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return
	}
	for name, rs := range raw.MCP.Servers {
		key := strings.ToLower(strings.TrimSpace(name))
		sc, ok := cfg.MCP.Servers[key]
		if !ok {
			continue
		}
		if len(rs.Env) > 0 {
			sc.Env = rs.Env
		}
		if len(rs.Headers) > 0 {
			sc.Headers = rs.Headers
		}
		cfg.MCP.Servers[key] = sc
	}
}

func setHomeDefaults(v *viper.Viper, ws Paths) {
	v.SetDefault("agent_dirs", []string{ws.Agents})
	v.SetDefault("plugin_dirs", []string{ws.Plugins})
	v.SetDefault("memory.dir", ws.Memory)
	v.SetDefault("memory.sqlite_path", ws.DB("archive"))
	v.SetDefault("knowledge.db_path", ws.DB("knowledge"))
	// DOC-4: the JSONL audit log (internal/audit) is OPTIONAL DEBUG output and
	// defaults OFF. The authoritative incident-reconstruction record is the
	// SQLite action log (internal/actionlog), which is always on. Operators who
	// want the redundant per-session JSONL files set runtime.audit_dir
	// explicitly (e.g. to <workspace>/audit). See docs/security/audit.md.
	v.SetDefault("runtime.audit_dir", "")
	v.SetDefault("server.gui_static_dir", ws.GUI)
	// Default to a workspace log file so the GUI Logs page works out of the
	// box (it tails log.file; empty = stdout-only and the page stays empty).
	// The logger still mirrors to stdout — this only ADDS the file sink.
	v.SetDefault("log.file", filepath.Join(ws.Logs, "soulacy.log"))
	v.SetDefault("search.provider", "ollama")
}

// DataDir returns the workspace root: ~/.soulacy/soulspace for new
// installations, the legacy flat ~/.soulacy for pre-soulspace ones.
// Prefer ResolveWorkspace for structured paths.
func DataDir() (string, error) {
	ws, err := ResolveWorkspace()
	if err != nil {
		return "", err
	}
	return ws.Root, nil
}

// EnsureDirs creates all directories Soulacy needs to run — the full
// workspace layout plus any explicitly configured paths.
func EnsureDirs(cfg *Config) error {
	dirs := []string{
		cfg.Memory.Dir,
		filepath.Dir(cfg.Memory.SQLitePath),
	}
	if cfg.Knowledge.DBPath != "" {
		dirs = append(dirs, filepath.Dir(cfg.Knowledge.DBPath))
	}
	if ws, err := ResolveWorkspace(); err == nil {
		dirs = append(dirs, ws.Dirs()...)
	}
	dirs = append(dirs, cfg.AgentDirs...)
	dirs = append(dirs, cfg.PluginDirs...)
	dirs = append(dirs, cfg.SkillDirs...)
	if cfg.Runtime.AuditDir != "" {
		dirs = append(dirs, cfg.Runtime.AuditDir)
	}

	for _, d := range dirs {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating dir %s: %w", d, err)
		}
	}
	// Secrets hold key material — owner-only.
	if ws, err := ResolveWorkspace(); err == nil {
		_ = os.Chmod(ws.Secrets, 0o700)
	}
	return nil
}

// SearchConfig configures the built-in web_search tool.
type SearchConfig struct {
	Provider string `mapstructure:"provider"`
	APIKey   string `mapstructure:"api_key"`
}
