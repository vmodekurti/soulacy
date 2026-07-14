package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type launchReadiness struct {
	Summary struct {
		Status          string `json:"status"`
		Score           int    `json:"score"`
		ReadyItems      int    `json:"ready_items"`
		WarningItems    int    `json:"warning_items"`
		BlockerItems    int    `json:"blocker_items"`
		TotalItems      int    `json:"total_items"`
		ProvidersReady  int    `json:"providers_ready"`
		ChannelsReady   int    `json:"channels_ready"`
		Agents          int    `json:"agents"`
		EnabledAgents   int    `json:"enabled_agents"`
		ChatAgents      int    `json:"chat_agents"`
		ScheduledAgents int    `json:"scheduled_agents"`
		LearningAgents  int    `json:"learning_agents"`
		Templates       int    `json:"templates"`
		UpdatesReady    bool   `json:"updates_ready"`
	} `json:"summary"`
	Journey     []launchReadinessItem `json:"journey"`
	NextActions []launchReadinessItem `json:"next_actions"`
	Parity      struct {
		Score   int                `json:"score"`
		Areas   []launchParityArea `json:"areas"`
		TopGaps []launchParityArea `json:"top_gaps"`
	} `json:"parity"`
	Release struct {
		Version        string `json:"version"`
		UpdateManifest string `json:"update_manifest"`
		UpdatesReady   bool   `json:"updates_ready"`
		UpdateHint     string `json:"update_hint"`
		DryRunCommand  string `json:"dry_run_command"`
		InstallCommand string `json:"install_command"`
	} `json:"release"`
	Deployment struct {
		Profile string `json:"profile"`
		Label   string `json:"label"`
		Status  string `json:"status"`
		Score   int    `json:"score"`
		Ready   int    `json:"ready"`
		Total   int    `json:"total"`
		Strict  bool   `json:"strict"`
		Owner   string `json:"owner"`
		Region  string `json:"region"`
	} `json:"deployment"`
}

type launchParityArea struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	Score     int    `json:"score"`
	Detail    string `json:"detail"`
	Next      string `json:"next"`
	Benchmark string `json:"benchmark"`
	Href      string `json:"href,omitempty"`
}

type launchReadinessItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Href   string `json:"href,omitempty"`
}

func buildLaunchCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "launch",
		Short: "Check production launch readiness",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Show the launch-readiness score and next actions",
		Long: `Show the same production-readiness journey used by the Dashboard.

The default mode is advisory and exits 0 when the gateway responds. Use
--strict in CI or before a production rollout to fail unless Soulacy reports
status=ready.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaunchCheck(strict)
		},
	})
	cmd.Commands()[0].Flags().BoolVar(&strict, "strict", false, "Exit non-zero unless launch readiness is ready")
	cmd.AddCommand(buildLaunchCertifyCmd())
	return cmd
}

type launchCertifyOptions struct {
	ReportDir     string
	Quick         bool
	LiveChannels  bool
	BrowserMCP    bool
	BrowserRender bool
	StudioLive    bool
}

func buildLaunchCertifyCmd() *cobra.Command {
	var opts launchCertifyOptions
	cmd := &cobra.Command{
		Use:   "certify",
		Short: "Run the production parity certification harness",
		Long: `Run Soulacy's production certification harness and write JSON/Markdown
reports under .cache/production-parity by default.

Use --quick for a daily confidence run. Omit it for the full release gate,
including full Go/GUI tests, vulnerability checks, docs, SDK checks, and race
testing. Optional live checks stay off unless explicitly enabled.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaunchCertify(opts)
		},
	}
	cmd.Flags().StringVar(&opts.ReportDir, "report-dir", "", "Directory where certification reports should be written")
	cmd.Flags().BoolVar(&opts.Quick, "quick", false, "Run the shorter daily certification profile")
	cmd.Flags().BoolVar(&opts.LiveChannels, "live-channels", false, "Run live Telegram/Slack/Discord delivery checks")
	cmd.Flags().BoolVar(&opts.BrowserMCP, "browser-mcp", false, "Run browser MCP sidecar checks")
	cmd.Flags().BoolVar(&opts.BrowserRender, "browser-render", false, "Run browser route screenshot/render checks")
	cmd.Flags().BoolVar(&opts.StudioLive, "studio-live", false, "Run optional Studio build/live workflow UAT")
	return cmd
}

func runLaunchCertify(opts launchCertifyOptions) error {
	root, err := findProjectRootForCertify()
	if err != nil {
		return err
	}
	script := filepath.Join(root, "scripts", "production-parity.sh")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("production certification script not found at %s; run this command from a Soulacy source checkout", script)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), launchCertifyEnv(opts)...)
	return cmd.Run()
}

func findProjectRootForCertify() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "scripts", "production-parity.sh")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", fmt.Errorf("could not find Soulacy project root from %s", wd)
}

func launchCertifyEnv(opts launchCertifyOptions) []string {
	var env []string
	if strings.TrimSpace(opts.ReportDir) != "" {
		env = append(env, "SOULACY_PARITY_REPORT_DIR="+opts.ReportDir)
	}
	if opts.Quick {
		env = append(env, "SOULACY_PARITY_QUICK=1")
	}
	if opts.LiveChannels {
		env = append(env, "SOULACY_PARITY_LIVE_CHANNELS=1")
	}
	if opts.BrowserMCP {
		env = append(env, "SOULACY_PARITY_BROWSER_MCP=1")
	}
	if opts.BrowserRender {
		env = append(env, "SOULACY_PARITY_BROWSER_RENDER=1")
	}
	if opts.StudioLive {
		env = append(env, "SOULACY_PARITY_STUDIO_LIVE=1")
	}
	return env
}

func runLaunchCheck(strict bool) error {
	data, err := apiCall("GET", "/readiness", nil)
	if err != nil {
		return err
	}
	if outputJSON {
		fmt.Println(string(data))
		var r launchReadiness
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		return launchStrictError(r.Summary.Status, strict)
	}
	var r launchReadiness
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	printLaunchReadiness(r)
	return launchStrictError(r.Summary.Status, strict)
}

func printLaunchReadiness(r launchReadiness) {
	status := strings.ReplaceAll(r.Summary.Status, "_", " ")
	if status == "" {
		status = "unknown"
	}
	fmt.Printf("Soulacy Launch Readiness: %d%% · %s\n", r.Summary.Score, status)
	fmt.Printf("Ready %d/%d · Blockers %d · Warnings %d\n",
		r.Summary.ReadyItems,
		r.Summary.TotalItems,
		r.Summary.BlockerItems,
		r.Summary.WarningItems,
	)
	fmt.Printf("Providers %d · Channels %d · Enabled agents %d/%d · Learning agents %d · Templates %d\n\n",
		r.Summary.ProvidersReady,
		r.Summary.ChannelsReady,
		r.Summary.EnabledAgents,
		r.Summary.Agents,
		r.Summary.LearningAgents,
		r.Summary.Templates,
	)
	if r.Deployment.Profile != "" || r.Deployment.Label != "" {
		mode := "advisory"
		if r.Deployment.Strict {
			mode = "strict"
		}
		label := valueOr(r.Deployment.Label, r.Deployment.Profile)
		fmt.Printf("Deployment: %s · %s · %s · %d/%d checks",
			valueOr(label, "local"),
			mode,
			valueOr(r.Deployment.Status, "unknown"),
			r.Deployment.Ready,
			r.Deployment.Total,
		)
		if r.Deployment.Owner != "" || r.Deployment.Region != "" {
			fmt.Printf(" · %s/%s", valueOr(r.Deployment.Owner, "unowned"), valueOr(r.Deployment.Region, "no-region"))
		}
		fmt.Println()
		fmt.Println()
	}
	if r.Release.Version != "" || r.Release.UpdateManifest != "" || r.Release.UpdateHint != "" {
		fmt.Printf("Release: %s", valueOr(r.Release.Version, "unknown"))
		if r.Release.UpdatesReady {
			fmt.Printf(" · update manifest configured")
		} else {
			fmt.Printf(" · no update manifest")
		}
		if r.Release.UpdateManifest != "" {
			fmt.Printf(" (%s)", r.Release.UpdateManifest)
		}
		fmt.Println()
		if r.Release.UpdateHint != "" {
			fmt.Println("Update hint:", r.Release.UpdateHint)
		}
		fmt.Println()
	}

	tw := tabwriter.NewWriter(stdoutWriter{}, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AREA\tSTATUS\tDETAIL")
	for _, item := range r.Journey {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", item.Label, item.Status, item.Detail)
	}
	_ = tw.Flush()

	if r.Parity.Score > 0 || len(r.Parity.TopGaps) > 0 {
		fmt.Printf("\nCompetitive parity: %d%%", r.Parity.Score)
		if len(r.Parity.TopGaps) == 0 {
			fmt.Println(" · no reported gaps")
		} else {
			fmt.Println()
			ptw := tabwriter.NewWriter(stdoutWriter{}, 0, 0, 2, ' ', 0)
			fmt.Fprintln(ptw, "GAP\tSCORE\tBENCHMARK\tNEXT")
			for _, gap := range r.Parity.TopGaps {
				fmt.Fprintf(ptw, "%s\t%d%%\t%s\t%s\n", gap.Label, gap.Score, gap.Benchmark, gap.Next)
			}
			_ = ptw.Flush()
		}
	}

	if len(r.NextActions) == 0 {
		fmt.Println("\nNext actions: none. Core launch path is ready.")
		return
	}
	fmt.Println("\nNext actions:")
	for i, item := range r.NextActions {
		target := item.Href
		if target != "" {
			target = " (" + target + ")"
		}
		fmt.Printf("%d. %s: %s%s\n", i+1, item.Label, item.Detail, target)
	}
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func launchStrictError(status string, strict bool) error {
	if strict && status != "ready" {
		if status == "" {
			status = "unknown"
		}
		return fmt.Errorf("launch readiness is %s", status)
	}
	return nil
}

type stdoutWriter struct{}

func (stdoutWriter) Write(p []byte) (int, error) {
	return fmt.Print(string(p))
}
