package main

import (
	"encoding/json"
	"fmt"
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
	Release     struct {
		Version        string `json:"version"`
		UpdateManifest string `json:"update_manifest"`
		UpdatesReady   bool   `json:"updates_ready"`
		UpdateHint     string `json:"update_hint"`
		DryRunCommand  string `json:"dry_run_command"`
		InstallCommand string `json:"install_command"`
	} `json:"release"`
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
	return cmd
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
