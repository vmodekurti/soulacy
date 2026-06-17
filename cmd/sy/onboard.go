// onboard.go — `sy onboard` first-run wizard.
//
// Distinct from `sy setup` (legacy from-scratch config writer): onboard
// layers ON TOP of the silent first-run bootstrap that `soulacy serve`
// performs via internal/config.EnsureBootstrap. The wizard's job is to
// FILL fields the bootstrap would have left at sensible defaults — pick
// a provider, paste a key, install a daemon, etc. — not to recreate the
// config from scratch.
//
// Design principles:
//
//  1. Idempotent. Re-running onboard on a configured workspace must not
//     destroy state. We read what's there, present the current value,
//     and only mutate when the operator confirms.
//
//  2. Skippable. Every step has a "skip" option. The wizard's value is
//     in walking through the choices, not in coercing answers.
//
//  3. No new file format. Everything written goes through the same path
//     EnsureBootstrap uses — config.yaml mutations via textual patch so
//     operator comments and edits are preserved.
//
//  4. Reuses helpers from setup.go (prompt, confirm, promptChoices,
//     color funcs). No new TUI dependencies. Stays a single Go binary.
//
// Why we keep `sy setup` AND `sy onboard`: setup is the "I have nothing,
// generate me a fresh config.yaml" path. onboard is the "I just ran
// install.sh and soulacy serve once — what now?" path. The distinction
// matters because onboard knows about EnsureBootstrap's output and
// works WITH it; setup assumes a blank slate.

package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/soulacy/soulacy/internal/config"
)

func buildOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Guided first-run wizard — pick a provider, install a daemon, see your API key",
		Long: `Walk through the choices most operators want to make right after install:

  1. Confirm the workspace path (~/.soulacy/soulspace by default).
  2. Choose loopback or expose (loopback = local-only; expose = LAN/remote).
  3. Pick an LLM provider (Ollama auto-detect / OpenAI / Anthropic / skip).
  4. Optionally adopt a starter agent so the GUI isn't empty on first open.
  5. Optionally install a daemon so soulacy starts on login.

Re-running ` + "`sy onboard`" + ` is safe: each step shows the current value and lets
you skip without changing anything. The wizard never deletes data.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnboardWizard()
		},
	}
}

func runOnboardWizard() error {
	printOnboardBanner()

	// Step 0: workspace resolution + bootstrap. This is the gateway's
	// own first-run logic — calling it here means the wizard works on a
	// truly virgin install (no config.yaml, no workspace dirs).
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	if err := config.EnsureDirs(loadOrFreshConfig(ws)); err != nil {
		return fmt.Errorf("ensure workspace dirs: %w", err)
	}

	cfg, cfgPath, _ := config.Load("")
	if cfg == nil {
		cfg = loadOrFreshConfig(ws)
		cfgPath = ws.ConfigFile
	}
	bootstrap, berr := config.EnsureBootstrap(cfg, cfgPath)
	if berr != nil {
		return fmt.Errorf("first-run bootstrap: %w", berr)
	}
	if bootstrap.Action != config.BootstrapNoop {
		fmt.Printf("%s Bootstrapped %s\n", green("✓"), bootstrap.ConfigPath)
		if bootstrap.APIKey != "" {
			fmt.Printf("  %s API key: %s\n", cyan("→"), bold(bootstrap.APIKey))
			fmt.Printf("  %s Save it — the GUI will ask for it.\n\n", gray("(this banner appears once)"))
		}
	} else {
		fmt.Printf("%s Existing config detected: %s\n\n", green("✓"), cfgPath)
	}

	// ── Step 1: Workspace path ─────────────────────────────────────────────
	printStep(1, "Workspace location")
	fmt.Printf("  %s %s\n", dim("Current:"), gray(ws.Root))
	if ws.Legacy {
		fmt.Printf("  %s You're on the legacy flat layout. Migrate with: %s\n",
			yellow("⚠"), bold("sy workspace migrate"))
	}
	fmt.Println()

	// ── Step 2: Loopback vs expose ─────────────────────────────────────────
	printStep(2, "Bind address")
	currentHost := cfg.Server.Host
	if currentHost == "" {
		currentHost = "127.0.0.1"
	}
	fmt.Printf("  %s %s:%d %s\n", dim("Current:"), currentHost, cfg.Server.Port,
		gray("(127.0.0.1 = local-only; 0.0.0.0 = expose on LAN)"))
	if confirm("  Change bind address?", false) {
		choice := promptChoices("Bind to:", []string{
			"127.0.0.1 (local-only, recommended)",
			"0.0.0.0 (expose on LAN — make sure auth is on)",
			"Custom IP",
		})
		switch choice {
		case 0:
			cfg.Server.Host = "127.0.0.1"
		case 1:
			cfg.Server.Host = "0.0.0.0"
			fmt.Printf("  %s Exposing means anyone on your network can hit the gateway.\n", yellow("⚠"))
			fmt.Printf("  %s Make sure server.api_key is set. (It is: %s)\n",
				gray("Note:"), maskKey(cfg.Server.APIKey))
		case 2:
			cfg.Server.Host = prompt("  Custom IP", "")
		}
		if err := patchServerHost(cfgPath, cfg.Server.Host); err != nil {
			fmt.Printf("  %s Couldn't patch config: %v\n", red("✗"), err)
		} else {
			fmt.Printf("  %s Updated bind address to %s\n", green("✓"), cfg.Server.Host)
		}
	}
	fmt.Println()

	// ── Step 3: LLM provider ───────────────────────────────────────────────
	printStep(3, "LLM provider")
	fmt.Printf("  %s %s\n", dim("Current default:"), cyan(cfg.LLM.DefaultProvider))
	fmt.Printf("  %s Detecting Ollama on localhost:11434...\n", gray("→"))
	if ollamaUp() {
		fmt.Printf("  %s Ollama is running locally. You're good — `sy chat` should work right now.\n", green("✓"))
	} else {
		fmt.Printf("  %s Ollama not reachable. You'll want OpenAI or Anthropic.\n", yellow("⚠"))
	}
	fmt.Println()
	if confirm("  Add or change an LLM provider key?", false) {
		choice := promptChoices("Provider:", []string{
			"OpenAI",
			"Anthropic",
			"Google (Gemini)",
			"Groq",
			"OpenRouter",
			"Custom (OpenAI-compatible)",
			"Skip",
		})
		switch choice {
		case 0:
			runProviderSetup(cfgPath, "openai", "https://api.openai.com/v1")
		case 1:
			runProviderSetup(cfgPath, "anthropic", "https://api.anthropic.com/v1")
		case 2:
			runProviderSetup(cfgPath, "google", "https://generativelanguage.googleapis.com/v1beta")
		case 3:
			runProviderSetup(cfgPath, "groq", "https://api.groq.com/openai/v1")
		case 4:
			runProviderSetup(cfgPath, "openrouter", "https://openrouter.ai/api/v1")
		case 5:
			runProviderSetup(cfgPath, "custom", "")
		}
	}
	fmt.Println()

	// ── Step 4: Starter agent ──────────────────────────────────────────────
	printStep(4, "Starter agent")
	agentCount := countAgentsOnDisk(ws)
	if agentCount > 0 {
		fmt.Printf("  %s %d agent(s) on disk. Skipping.\n", green("✓"), agentCount)
	} else {
		fmt.Printf("  %s No agents yet.\n", yellow("⚠"))
		if confirm("  Drop in the basic-chat starter?", true) {
			if err := writeStarterAgent(ws); err != nil {
				fmt.Printf("  %s Couldn't write starter: %v\n", red("✗"), err)
			} else {
				fmt.Printf("  %s Wrote %s\n", green("✓"),
					filepath.Join(ws.Agents, "basic-chat", "SOUL.yaml"))
			}
		}
	}
	fmt.Println()

	// ── Step 5: Daemon install ─────────────────────────────────────────────
	printStep(5, "Auto-start on login")
	switch runtime.GOOS {
	case "darwin":
		fmt.Printf("  %s On macOS this installs a LaunchAgent.\n", gray("→"))
	case "linux":
		fmt.Printf("  %s On Linux this installs a systemd --user unit.\n", gray("→"))
	default:
		fmt.Printf("  %s Auto-start not supported on %s. Skipping.\n", yellow("⚠"), runtime.GOOS)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if confirm("  Install daemon so soulacy starts on login?", false) {
			if err := daemonInstall(); err != nil {
				fmt.Printf("  %s Daemon install failed: %v\n", red("✗"), err)
				fmt.Printf("  %s You can try again later: %s\n", gray("Hint:"), bold("sy daemon install"))
			}
		} else {
			fmt.Printf("  %s Skipped. Start manually with: %s\n", gray("→"), bold("soulacy serve"))
		}
	}
	fmt.Println()

	// ── Done ────────────────────────────────────────────────────────────────
	printOnboardDone(cfg, cfgPath)
	return nil
}

// ── Step helpers ────────────────────────────────────────────────────────────

// loadOrFreshConfig returns a usable Config for EnsureDirs/EnsureBootstrap
// even when config.Load fails (no config file yet on a virgin install).
// The returned config has just enough defaults for the bootstrap path
// to take over.
func loadOrFreshConfig(ws config.Paths) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 18789,
		},
		AgentDirs: []string{ws.Agents},
	}
}

// runProviderSetup prompts for an API key and patches config.yaml under
// llm.providers.<id>.api_key. Doesn't change default_provider unless the
// operator confirms.
func runProviderSetup(cfgPath, providerID, baseURL string) {
	if providerID == "custom" {
		providerID = prompt("  Provider ID (e.g. vllm, together)", "")
		if providerID == "" {
			fmt.Printf("  %s No provider ID entered. Nothing changed.\n", yellow("→"))
			return
		}
		baseURL = prompt("  Base URL (e.g. https://api.together.xyz/v1)", "")
	}

	if baseURL != "" {
		fmt.Printf("  %s Provider base URL: %s\n", dim("Endpoint:"), gray(baseURL))
	}
	key := prompt("  Paste API key (or blank to skip)", "")
	if key == "" {
		fmt.Printf("  %s No key entered. Nothing changed.\n", yellow("→"))
		return
	}
	if err := patchProviderKey(cfgPath, providerID, key); err != nil {
		fmt.Printf("  %s Patch failed: %v\n", red("✗"), err)
		return
	}
	if baseURL != "" {
		if err := patchProviderBaseURL(cfgPath, providerID, baseURL); err != nil {
			fmt.Printf("  %s Base URL patch failed: %v\n", red("✗"), err)
		}
	}
	fmt.Printf("  %s Wrote llm.providers.%s.api_key (masked: %s)\n",
		green("✓"), providerID, maskKey(key))
	if confirm(fmt.Sprintf("  Make %s the default provider?", providerID), false) {
		if err := patchDefaultProvider(cfgPath, providerID); err != nil {
			fmt.Printf("  %s Patch failed: %v\n", red("✗"), err)
		} else {
			fmt.Printf("  %s Default provider: %s\n", green("✓"), providerID)
		}
	}
}

// ollamaUp returns true when a HEAD/GET to localhost:11434 succeeds in
// under 1 second. We use this for the "Ollama is running locally" hint
// in step 3 — never to make a decision for the operator.
func ollamaUp() bool {
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// countAgentsOnDisk walks the agents dir looking for SOUL.yaml files.
// Doesn't recurse beyond the immediate child dirs — agents live at
// <agents>/<id>/SOUL.yaml in the soulspace layout.
func countAgentsOnDisk(ws config.Paths) int {
	entries, err := os.ReadDir(ws.Agents)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(ws.Agents, e.Name(), "SOUL.yaml")); err == nil {
			n++
		}
	}
	return n
}

// writeStarterAgent writes a minimal but functional SOUL.yaml so the
// GUI isn't empty on first open. The agent uses the configured default
// provider with no special tools — just chat.
func writeStarterAgent(ws config.Paths) error {
	dir := filepath.Join(ws.Agents, "basic-chat")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "SOUL.yaml")
	if _, err := os.Stat(path); err == nil {
		return nil // don't overwrite
	}
	const content = `# Starter agent written by ` + "`sy onboard`" + `.
# Edit freely — the gateway watches this file and reloads on change.
id: basic-chat
name: Basic chat
description: A friendly assistant for getting started.
system_prompt: |
  You are a helpful assistant. Answer questions accurately and concisely.
  When you don't know, say so.
llm:
  # default_provider from config.yaml wins unless you override here:
  # provider: openai
  # model: gpt-4o-mini
  temperature: 0.7
`
	return os.WriteFile(path, []byte(content), 0o644)
}

// ── Textual patches to config.yaml ──────────────────────────────────────────
//
// Same philosophy as internal/config.patchConfigAPIKey: a YAML round-trip
// via viper would reformat the file and strip operator comments. We do
// targeted line-level edits so the file looks "lived in."

func patchServerHost(path, host string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	patched := replaceKeyLine(string(body), "server", "host", fmt.Sprintf("%q", host))
	if patched == string(body) {
		return nil
	}
	return os.WriteFile(path, []byte(patched), 0o600)
}

func patchProviderKey(path, providerID, apiKey string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Look for an existing `<providerID>:` block under `providers:` and
	// replace/insert its `api_key:` line. If the block doesn't exist,
	// append a minimal one at the end of the file under llm.providers.
	patched := injectProviderKey(string(body), providerID, apiKey)
	if patched == string(body) {
		return nil
	}
	return os.WriteFile(path, []byte(patched), 0o600)
}

func patchDefaultProvider(path, providerID string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	patched := replaceKeyLine(string(body), "llm", "default_provider", providerID)
	if patched == string(body) {
		return nil
	}
	return os.WriteFile(path, []byte(patched), 0o600)
}

// replaceKeyLine finds `<key>:` inside the `<parent>:` block (at any
// indentation > 0) and replaces the value. If the key doesn't exist
// under the parent, returns body unchanged.
func replaceKeyLine(body, parent, key, value string) string {
	parentTag := "\n" + parent + ":"
	pIdx := strings.Index(body, parentTag)
	if pIdx < 0 && !strings.HasPrefix(body, parent+":") {
		return body
	}
	if pIdx < 0 {
		pIdx = 0
	} else {
		pIdx++ // skip leading \n
	}
	// Find end of parent's block: next top-level key (line starting with [a-z]) or EOF.
	endIdx := findBlockEnd(body, pIdx+len(parent)+1)
	inside := body[pIdx:endIdx]
	keyTok := "\n  " + key + ":"
	kIdx := strings.Index(inside, keyTok)
	if kIdx < 0 {
		return body // key not present; don't auto-insert (caller can decide)
	}
	abs := pIdx + kIdx + 1 // skip newline
	// Walk to end of that line.
	lineEnd := abs
	for lineEnd < len(body) && body[lineEnd] != '\n' {
		lineEnd++
	}
	return body[:abs] + "  " + key + ": " + value + body[lineEnd:]
}

// injectProviderKey rewrites llm.providers.<id>.api_key, inserting the
// provider block if needed. Best-effort textual edit.
func injectProviderKey(body, providerID, apiKey string) string {
	provBlock := "\n    " + providerID + ":"
	// Easy case: providers.<id> block already exists.
	if strings.Contains(body, provBlock) {
		// Locate api_key inside it.
		pIdx := strings.Index(body, provBlock) + 1
		endIdx := findBlockEnd(body, pIdx+len(providerID)+4) // crude — next 4-space-indent peer
		inside := body[pIdx:endIdx]
		keyTok := "\n      api_key:"
		if kIdx := strings.Index(inside, keyTok); kIdx >= 0 {
			abs := pIdx + kIdx + 1
			lineEnd := abs
			for lineEnd < len(body) && body[lineEnd] != '\n' {
				lineEnd++
			}
			return body[:abs] + fmt.Sprintf("      api_key: %q", apiKey) + body[lineEnd:]
		}
		// Insert api_key right after the provider block header line.
		eol := pIdx
		for eol < len(body) && body[eol] != '\n' {
			eol++
		}
		return body[:eol] + fmt.Sprintf("\n      api_key: %q", apiKey) + body[eol:]
	}
	// Harder case: providers block exists but no <id>.
	if idx := strings.Index(body, "providers:"); idx >= 0 {
		eol := idx
		for eol < len(body) && body[eol] != '\n' {
			eol++
		}
		insert := fmt.Sprintf("\n    %s:\n      api_key: %q", providerID, apiKey)
		return body[:eol] + insert + body[eol:]
	}
	// Hardest case: no llm.providers block at all. Append one.
	tail := ""
	if len(body) > 0 && body[len(body)-1] != '\n' {
		tail = "\n"
	}
	return body + tail + fmt.Sprintf("\nllm:\n  providers:\n    %s:\n      api_key: %q\n", providerID, apiKey)
}

// injectProviderBaseURL rewrites llm.providers.<id>.base_url.
// It assumes the provider block already exists (since patchProviderKey is called first).
func injectProviderBaseURL(body, providerID, baseURL string) string {
	provBlock := "\n    " + providerID + ":"
	if !strings.Contains(body, provBlock) {
		return body // should not happen if patchProviderKey succeeded
	}
	pIdx := strings.Index(body, provBlock) + 1
	endIdx := findBlockEnd(body, pIdx+len(providerID)+4)
	inside := body[pIdx:endIdx]
	keyTok := "\n      base_url:"
	if kIdx := strings.Index(inside, keyTok); kIdx >= 0 {
		abs := pIdx + kIdx + 1
		lineEnd := abs
		for lineEnd < len(body) && body[lineEnd] != '\n' {
			lineEnd++
		}
		return body[:abs] + fmt.Sprintf("      base_url: %q", baseURL) + body[lineEnd:]
	}
	// Insert base_url right after the provider block header line.
	eol := pIdx
	for eol < len(body) && body[eol] != '\n' {
		eol++
	}
	return body[:eol] + fmt.Sprintf("\n      base_url: %q", baseURL) + body[eol:]
}

func patchProviderBaseURL(path, providerID, baseURL string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	patched := injectProviderBaseURL(string(body), providerID, baseURL)
	if patched == string(body) {
		return nil
	}
	return os.WriteFile(path, []byte(patched), 0o600)
}

// findBlockEnd returns the byte index of the first character of the
// line that ends the current YAML block (i.e. the next non-indented
// non-blank line). Used to scope textual edits to a single block.
func findBlockEnd(body string, from int) int {
	i := from
	for i < len(body) {
		// Advance to start of next line.
		for i < len(body) && body[i] != '\n' {
			i++
		}
		if i >= len(body) {
			return len(body)
		}
		i++ // past the \n
		if i >= len(body) {
			return len(body)
		}
		// If next line starts with a letter at column 0, that's a new
		// top-level key — block ended on the previous line.
		c := body[i]
		if c != ' ' && c != '\t' && c != '#' && c != '\n' {
			return i
		}
	}
	return len(body)
}

// ── Cosmetic helpers ────────────────────────────────────────────────────────

func maskKey(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("•", len(k))
	}
	return k[:3] + strings.Repeat("•", len(k)-7) + k[len(k)-4:]
}

func printOnboardBanner() {
	fmt.Println()
	fmt.Println(bold(cyan("  ╔═══════════════════════════════════════════════╗")))
	fmt.Println(bold(cyan("  ║   Soulacy — onboarding                        ║")))
	fmt.Println(bold(cyan("  ║   We'll only ask about things that matter.    ║")))
	fmt.Println(bold(cyan("  ╚═══════════════════════════════════════════════╝")))
	fmt.Println()
}

func printOnboardDone(cfg *config.Config, cfgPath string) {
	host := cfg.Server.Host
	if host == "" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%d", host, cfg.Server.Port)
	fmt.Println(bold(green("  ╔═══════════════════════════════════════════════╗")))
	fmt.Println(bold(green("  ║   You're set.                                 ║")))
	fmt.Println(bold(green("  ╚═══════════════════════════════════════════════╝")))
	fmt.Println()
	fmt.Printf("  %s %s\n", dim("Config:"), gray(cfgPath))
	fmt.Printf("  %s %s\n", dim("URL:"), cyan(url))
	if cfg.Server.APIKey != "" {
		fmt.Printf("  %s %s\n", dim("API key:"), gray(maskKey(cfg.Server.APIKey)))
	}
	fmt.Println()
	fmt.Printf("  Next:\n")
	fmt.Printf("    %s %s     %s\n", green("→"), bold("soulacy serve"), gray("(start the gateway)"))
	fmt.Printf("    %s %s          %s\n", green("→"), bold("sy doctor"), gray("(verify everything's healthy)"))
	fmt.Printf("    %s %s         %s\n", green("→"), bold("sy agent ls"), gray("(see what's installed)"))
	fmt.Println()
}
