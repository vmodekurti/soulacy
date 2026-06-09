package main

import (
	"encoding/json"
	"fmt"
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
	add(checkPython())
	add(checkOllama())
	add(checkKnowledgeDB(runtimeDir))
	add(checkGatewayHealth())
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
