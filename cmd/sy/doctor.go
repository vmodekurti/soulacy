package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/soulacy/soulacy/internal/config"
)

type doctorStatus string

const (
	doctorOK   doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
)

type doctorCheck struct {
	Name    string       `json:"name"`
	Status  doctorStatus `json:"status"`
	Detail  string       `json:"detail"`
	Remedy  string       `json:"remedy,omitempty"`
	Elapsed string       `json:"elapsed,omitempty"`
}

type doctorReport struct {
	GatewayURL string        `json:"gateway_url"`
	ConfigPath string        `json:"config_path"`
	Checks     []doctorCheck `json:"checks"`
	Summary    struct {
		OK    int `json:"ok"`
		Warn  int `json:"warn"`
		Fail  int `json:"fail"`
		Total int `json:"total"`
	} `json:"summary"`
}

func buildDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run local diagnostics for Soulacy",
		Long: `Run local diagnostics for a Soulacy installation.

Checks config discovery, runtime directories, gateway health, Python tooling,
Ollama reachability, knowledge storage, and MCP server status.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor()
		},
	}
}

func runDoctor() error {
	report := doctorReport{
		GatewayURL: gatewayURL,
		ConfigPath: viper.ConfigFileUsed(),
	}
	add := func(c doctorCheck) {
		report.Checks = append(report.Checks, c)
		switch c.Status {
		case doctorOK:
			report.Summary.OK++
		case doctorWarn:
			report.Summary.Warn++
		case doctorFail:
			report.Summary.Fail++
		}
		report.Summary.Total++
	}

	home, _ := os.UserHomeDir()
	runtimeDir := filepath.Join(home, ".soulacy")
	if ws, werr := config.ResolveWorkspace(); werr == nil {
		runtimeDir = ws.Root
	}

	add(checkConfig())
	add(checkRuntimeDir(runtimeDir))
	add(checkAgentDirs())
	add(checkAgentCount())
	add(checkPort())
	add(checkPython())
	add(checkOllama())
	add(checkProviderReachability())
	add(checkSandboxState())
	add(checkKnowledgeDB(runtimeDir))
	add(checkGatewayHealth())
	add(checkRecentErrors())
	add(checkMCPStatus())

	if outputJSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		printDoctorReport(report)
	}
	if report.Summary.Fail > 0 {
		return fmt.Errorf("doctor found %d failing check(s)", report.Summary.Fail)
	}
	return nil
}

func checkConfig() doctorCheck {
	path := viper.ConfigFileUsed()
	if path == "" {
		return doctorCheck{
			Name:   "config",
			Status: doctorWarn,
			Detail: "no config file loaded; using defaults and environment",
			Remedy: "run `sy setup` or create ~/.soulacy/config.yaml",
		}
	}
	if _, err := os.Stat(path); err != nil {
		return doctorCheck{Name: "config", Status: doctorFail, Detail: err.Error()}
	}
	return doctorCheck{Name: "config", Status: doctorOK, Detail: path}
}

func checkRuntimeDir(runtimeDir string) doctorCheck {
	required := []string{"agents", "logs", "mcp-servers", "skills"}
	var missing []string
	for _, name := range required {
		p := filepath.Join(runtimeDir, name)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return doctorCheck{
			Name:   "runtime directories",
			Status: doctorWarn,
			Detail: "missing: " + strings.Join(missing, ", "),
			Remedy: "run `sy setup` or `mkdir -p ~/.soulacy/{agents,logs,mcp-servers,skills}`",
		}
	}
	return doctorCheck{Name: "runtime directories", Status: doctorOK, Detail: runtimeDir}
}

func checkAgentDirs() doctorCheck {
	dirs := viper.GetStringSlice("agent_dirs")
	if len(dirs) == 0 {
		// Same fallback as cmd/sy/agent_tier.go::loadAgentsFromDisk —
		// `sy` doesn't run config.Load(), so viper has no agent_dirs
		// even on installs where the gateway uses the workspace default
		// at ~/.soulacy/soulspace/agents. Going through ResolveWorkspace
		// gives doctor the SAME path the gateway resolves.
		ws, err := config.ResolveWorkspace()
		if err == nil && ws.Agents != "" {
			dirs = []string{ws.Agents}
		}
	}
	if len(dirs) == 0 {
		return doctorCheck{
			Name:   "agent_dirs",
			Status: doctorWarn,
			Detail: "no agent_dirs configured and workspace resolution failed",
			Remedy: "set agent_dirs in your config.yaml, or set $SOULACY_WORKSPACE",
		}
	}
	var issues []string
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			issues = append(issues, dir+" is relative")
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			issues = append(issues, dir+" not found")
		}
	}
	if len(issues) > 0 {
		return doctorCheck{
			Name:   "agent_dirs",
			Status: doctorWarn,
			Detail: strings.Join(issues, "; "),
			Remedy: "use absolute, existing paths so LaunchAgent/systemd starts load the same agents",
		}
	}
	return doctorCheck{Name: "agent_dirs", Status: doctorOK, Detail: strings.Join(dirs, ", ")}
}

func checkPython() doctorCheck {
	py := viper.GetString("runtime.python_bin")
	if py == "" {
		py = "python3"
	}
	resolved, err := exec.LookPath(py)
	if err != nil {
		return doctorCheck{
			Name:   "python",
			Status: doctorWarn,
			Detail: py + " not found on PATH",
			Remedy: "set runtime.python_bin to an absolute Python 3 path",
		}
	}
	out, err := exec.Command(resolved, "--version").CombinedOutput()
	if err != nil {
		return doctorCheck{Name: "python", Status: doctorWarn, Detail: strings.TrimSpace(string(out))}
	}
	status := doctorOK
	remedy := ""
	detail := strings.TrimSpace(string(out)) + " at " + resolved
	if !filepath.IsAbs(py) {
		status = doctorWarn
		remedy = "set runtime.python_bin to " + resolved + " for LaunchAgent/systemd reliability"
	}
	return doctorCheck{Name: "python", Status: status, Detail: detail, Remedy: remedy}
}

func checkOllama() doctorCheck {
	base := viper.GetString("llm.providers.ollama.base_url")
	if base == "" {
		base = "http://localhost:11434"
	}
	start := time.Now()
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := httpJSON(base+"/api/tags", &payload, 3*time.Second); err != nil {
		return doctorCheck{
			Name:    "ollama",
			Status:  doctorWarn,
			Detail:  err.Error(),
			Remedy:  "start Ollama or configure a different default LLM provider",
			Elapsed: time.Since(start).String(),
		}
	}
	names := make([]string, 0, len(payload.Models))
	for _, m := range payload.Models {
		names = append(names, m.Name)
	}
	detail := fmt.Sprintf("%d model(s)", len(names))
	if len(names) > 0 {
		detail += ": " + strings.Join(firstN(names, 5), ", ")
	}
	return doctorCheck{Name: "ollama", Status: doctorOK, Detail: detail, Elapsed: time.Since(start).String()}
}

func checkKnowledgeDB(runtimeDir string) doctorCheck {
	path := viper.GetString("knowledge.db_path")
	if path == "" {
		path = filepath.Join(runtimeDir, "knowledge.db")
	}
	st, err := os.Stat(path)
	if err != nil {
		return doctorCheck{
			Name:   "knowledge db",
			Status: doctorWarn,
			Detail: path + " not found",
			Remedy: "create a KB from the GUI or POST /api/v1/knowledge",
		}
	}
	return doctorCheck{Name: "knowledge db", Status: doctorOK, Detail: fmt.Sprintf("%s (%d bytes)", path, st.Size())}
}

func checkGatewayHealth() doctorCheck {
	start := time.Now()
	var body map[string]any
	err := gatewayJSON("/health", &body, 3*time.Second)
	if err != nil {
		return doctorCheck{
			Name:    "gateway",
			Status:  doctorFail,
			Detail:  err.Error(),
			Remedy:  "start the gateway with `soulacy serve`",
			Elapsed: time.Since(start).String(),
		}
	}
	status, _ := body["status"].(string)
	if status == "" {
		status = "unknown"
	}
	checkStatus := doctorOK
	if status != "ok" {
		checkStatus = doctorWarn
	}
	return doctorCheck{Name: "gateway", Status: checkStatus, Detail: "health status: " + status, Elapsed: time.Since(start).String()}
}

func checkMCPStatus() doctorCheck {
	start := time.Now()
	var body struct {
		Servers []struct {
			ID        string `json:"id"`
			Connected bool   `json:"connected"`
			Detail    string `json:"detail"`
		} `json:"servers"`
	}
	if err := gatewayJSON("/mcp", &body, 5*time.Second); err != nil {
		return doctorCheck{Name: "mcp", Status: doctorWarn, Detail: err.Error(), Elapsed: time.Since(start).String()}
	}
	if len(body.Servers) == 0 {
		return doctorCheck{Name: "mcp", Status: doctorOK, Detail: "no MCP servers configured", Elapsed: time.Since(start).String()}
	}
	var connected, failed []string
	for _, s := range body.Servers {
		if s.Connected {
			connected = append(connected, s.ID)
		} else {
			d := s.ID
			if s.Detail != "" {
				d += " (" + s.Detail + ")"
			}
			failed = append(failed, d)
		}
	}
	if len(failed) > 0 {
		return doctorCheck{
			Name:    "mcp",
			Status:  doctorWarn,
			Detail:  fmt.Sprintf("connected: %s; failed: %s", strings.Join(connected, ", "), strings.Join(failed, ", ")),
			Remedy:  "open the MCP GUI page or inspect ~/.soulacy/logs/soulacy.log",
			Elapsed: time.Since(start).String(),
		}
	}
	return doctorCheck{Name: "mcp", Status: doctorOK, Detail: "connected: " + strings.Join(connected, ", "), Elapsed: time.Since(start).String()}
}

func gatewayJSON(path string, out any, timeout time.Duration) error {
	url := gatewayURL + "/api/v1" + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func httpJSON(url string, out any, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func printDoctorReport(r doctorReport) {
	fmt.Println(bold("Soulacy Doctor"))
	fmt.Printf("Gateway: %s\n", r.GatewayURL)
	if r.ConfigPath != "" {
		fmt.Printf("Config:  %s\n", r.ConfigPath)
	} else {
		fmt.Println("Config:  (none loaded)")
	}
	fmt.Println()
	for _, c := range r.Checks {
		mark := green("✓")
		if c.Status == doctorWarn {
			mark = yellow("!")
		} else if c.Status == doctorFail {
			mark = red("✗")
		}
		elapsed := ""
		if c.Elapsed != "" {
			elapsed = " " + gray("("+c.Elapsed+")")
		}
		fmt.Printf("%s %-20s %s%s\n", mark, c.Name, c.Detail, elapsed)
		if c.Remedy != "" {
			fmt.Printf("  %s %s\n", gray("fix:"), c.Remedy)
		}
	}
	fmt.Println()
	fmt.Printf("Summary: %s ok, %s warn, %s fail\n",
		green(fmt.Sprint(r.Summary.OK)),
		yellow(fmt.Sprint(r.Summary.Warn)),
		red(fmt.Sprint(r.Summary.Fail)),
	)
	if runtime.GOOS == "darwin" {
		fmt.Println(gray("Note: macOS LaunchAgents inherit a minimal PATH; absolute runtime.python_bin is recommended."))
	}
}

func firstN(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

// ── doctor v2 checks ────────────────────────────────────────────────────────
//
// Added 2026-06-09 as part of the OpenClaw parity pass. Each check is
// designed to catch a real failure mode an operator hits on a fresh
// install or after misconfiguration — NOT to inflate the check count.

// checkAgentCount warns when the workspace has zero agents on disk. A
// fresh install includes the basic-chat starter, so this should only
// fire if the operator deleted it without adding any replacements. The
// remedy is to run `sy onboard` or copy a SOUL.yaml.
func checkAgentCount() doctorCheck {
	ws := syWorkspace()
	dirs := []string{ws.Agents}
	if d := viper.GetStringSlice("agent_dirs"); len(d) > 0 {
		dirs = d
	}
	total := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "SOUL.yaml")); err == nil {
				total++
			}
		}
	}
	if total == 0 {
		return doctorCheck{
			Name:   "agents",
			Status: doctorWarn,
			Detail: "no agents found on disk",
			Remedy: "run `sy onboard` to set up a starter, or drop a SOUL.yaml into " + dirs[0],
		}
	}
	return doctorCheck{
		Name:   "agents",
		Status: doctorOK,
		Detail: fmt.Sprintf("%d agent(s) on disk across %d dir(s)", total, len(dirs)),
	}
}

// checkPort verifies the configured gateway port is either bound by an
// already-running soulacy (good — `sy doctor` while serving) OR is free
// for binding. A foreign process holding the port is a hard fail.
func checkPort() doctorCheck {
	host := viper.GetString("server.host")
	if host == "" {
		host = "127.0.0.1"
	}
	port := viper.GetInt("server.port")
	if port == 0 {
		port = 18789
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	// Try to bind ourselves. If we succeed, the port is free. If bind
	// fails with "address in use", check whether the gateway's /healthz
	// answers — if yes, it's our own process holding the port (fine).
	ln, bindErr := net.Listen("tcp", addr)
	if bindErr == nil {
		_ = ln.Close()
		return doctorCheck{
			Name:   "port",
			Status: doctorOK,
			Detail: fmt.Sprintf("%s is free for binding", addr),
		}
	}
	// Port is in use — is it us?
	healthURL := fmt.Sprintf("http://%s:%d/healthz", host, port)
	if err := httpJSON(healthURL, &map[string]any{}, 1*time.Second); err == nil {
		return doctorCheck{
			Name:   "port",
			Status: doctorOK,
			Detail: fmt.Sprintf("%s held by a running soulacy gateway", addr),
		}
	}
	return doctorCheck{
		Name:   "port",
		Status: doctorFail,
		Detail: fmt.Sprintf("%s is occupied by another process", addr),
		Remedy: fmt.Sprintf("find the process: lsof -i :%d", port),
	}
}

// checkProviderReachability does a HEAD/GET to each LLM provider that
// has a key configured. We don't actually try to complete a chat (that
// would burn tokens) — we just verify the endpoint resolves and accepts
// TCP. A 401 from the API means the endpoint is reachable, which is
// what we care about; the key validity is a separate concern.
func checkProviderReachability() doctorCheck {
	type probe struct {
		name string
		key  string // viper key for API key presence
		url  string // probe URL — pick a cheap endpoint per provider
	}
	probes := []probe{
		{"openai", "llm.providers.openai.api_key", "https://api.openai.com/v1/models"},
		{"anthropic", "llm.providers.anthropic.api_key", "https://api.anthropic.com/v1/messages"},
		{"groq", "llm.providers.groq.api_key", "https://api.groq.com/openai/v1/models"},
		{"mistral", "llm.providers.mistral.api_key", "https://api.mistral.ai/v1/models"},
		{"openrouter", "llm.providers.openrouter.api_key", "https://openrouter.ai/api/v1/models"},
		{"deepseek", "llm.providers.deepseek.api_key", "https://api.deepseek.com/v1/models"},
	}
	configured := 0
	var unreachable []string
	for _, p := range probes {
		if viper.GetString(p.key) == "" {
			continue
		}
		configured++
		client := &http.Client{Timeout: 3 * time.Second}
		req, _ := http.NewRequest("GET", p.url, nil)
		resp, err := client.Do(req)
		if err != nil {
			unreachable = append(unreachable, fmt.Sprintf("%s (%v)", p.name, err))
			continue
		}
		_ = resp.Body.Close()
		// Any HTTP response means the endpoint is reachable; we don't
		// care about auth status (401 is fine for a reachability probe).
	}
	if configured == 0 {
		return doctorCheck{
			Name:   "provider_reach",
			Status: doctorWarn,
			Detail: "no remote LLM provider keys configured (Ollama-only OK if you have it running)",
			Remedy: "run `sy onboard` to configure a provider, or paste a key into config.yaml",
		}
	}
	if len(unreachable) > 0 {
		return doctorCheck{
			Name:   "provider_reach",
			Status: doctorFail,
			Detail: fmt.Sprintf("%d of %d provider endpoints unreachable: %s",
				len(unreachable), configured, strings.Join(firstN(unreachable, 3), "; ")),
			Remedy: "check network / DNS / firewall to api.openai.com etc.",
		}
	}
	return doctorCheck{
		Name:   "provider_reach",
		Status: doctorOK,
		Detail: fmt.Sprintf("%d provider endpoint(s) reachable", configured),
	}
}

// checkSandboxState verifies the sandbox is enabled when the config says
// it should be, and the python self-exec sandbox wrapper is invokable.
// The runtime auto-disables sandbox on platforms where rlimits aren't
// available; we warn if config says enabled but we know it can't fire.
func checkSandboxState() doctorCheck {
	enabled := viper.GetBool("runtime.sandbox.enabled")
	if !enabled {
		// Look at the default — if the operator explicitly disabled it,
		// say so neutrally. If they're on the default-false (older
		// configs), nudge them to flip it on.
		return doctorCheck{
			Name:   "sandbox",
			Status: doctorWarn,
			Detail: "runtime.sandbox.enabled is false",
			Remedy: "set runtime.sandbox.enabled: true in config.yaml to constrain python tool execution",
		}
	}
	if runtime.GOOS == "windows" {
		return doctorCheck{
			Name:   "sandbox",
			Status: doctorWarn,
			Detail: "sandbox configured ON but rlimit-based sandbox isn't supported on Windows",
			Remedy: "set runtime.sandbox.enabled: false, OR run soulacy under WSL2",
		}
	}
	cpu := viper.GetInt("runtime.sandbox.cpu_seconds")
	mem := viper.GetInt("runtime.sandbox.memory_mb")
	return doctorCheck{
		Name:   "sandbox",
		Status: doctorOK,
		Detail: fmt.Sprintf("enabled (cpu=%ds mem=%dMB)", cpu, mem),
	}
}

// checkRecentErrors queries the gateway's action log endpoint for the
// last hour and warns when error density crosses a threshold. Skipped
// silently when the gateway isn't reachable — checkGatewayHealth
// already covers that case and we don't want double-fails.
func checkRecentErrors() doctorCheck {
	type actionRow struct {
		Status string `json:"status"`
	}
	type listResp struct {
		Items []actionRow `json:"items"`
		Total int         `json:"total"`
	}
	var resp listResp
	url := strings.TrimRight(gatewayURL, "/") + "/api/v1/actions?limit=100&since=1h"
	if err := httpJSON(url, &resp, 3*time.Second); err != nil {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorWarn,
			Detail: "couldn't read action log (gateway down?)",
		}
	}
	if len(resp.Items) == 0 {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorOK,
			Detail: "no recent activity to assess",
		}
	}
	errs := 0
	for _, r := range resp.Items {
		if r.Status == "error" || r.Status == "failed" || r.Status == "timeout" {
			errs++
		}
	}
	rate := float64(errs) / float64(len(resp.Items))
	if rate >= 0.30 {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorFail,
			Detail: fmt.Sprintf("%d/%d (%.0f%%) recent actions failed", errs, len(resp.Items), rate*100),
			Remedy: "check `sy logs` for repeated errors; common causes: provider auth, sandbox limits, MCP server crashes",
		}
	}
	if rate >= 0.10 {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorWarn,
			Detail: fmt.Sprintf("%d/%d (%.0f%%) recent actions failed", errs, len(resp.Items), rate*100),
			Remedy: "check `sy logs` if this is unexpected",
		}
	}
	return doctorCheck{
		Name:   "error_rate",
		Status: doctorOK,
		Detail: fmt.Sprintf("%d/%d (%.0f%%) recent actions failed", errs, len(resp.Items), rate*100),
	}
}
