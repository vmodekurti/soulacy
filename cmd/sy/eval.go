package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/soulacy/soulacy/internal/eval"
	"github.com/spf13/cobra"
)

func buildEvalCmd() *cobra.Command {
	var agentID string
	var suiteFile string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run an evaluation suite against an agent",
		Long: `Run a structured eval suite against a live agent and report pass/fail.

Suite file format (JSON):
  {
    "name": "smoke test",
    "cases": [
      {"name":"greeting","input":"Hello!","expected_contains":["hello","hi"]},
      {"name":"math","input":"What is 2+2?","expected_contains":["4"]}
    ]
  }

Examples:
  sy eval --agent my-agent --suite tests/smoke.json
  sy eval --agent my-agent --suite tests/smoke.json --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentID == "" {
				return fmt.Errorf("--agent is required")
			}
			if suiteFile == "" {
				return fmt.Errorf("--suite is required")
			}
			return runEval(agentID, suiteFile, jsonOut)
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent ID to evaluate")
	cmd.Flags().StringVarP(&suiteFile, "suite", "s", "", "Path to eval suite JSON file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output results as JSON")
	return cmd
}

func runEval(agentID, suiteFile string, jsonOut bool) error {
	data, err := os.ReadFile(suiteFile)
	if err != nil {
		return fmt.Errorf("failed to read suite file %s: %w", suiteFile, err)
	}

	suite, err := eval.LoadSuiteFromJSON(data)
	if err != nil {
		return err
	}

	runner := eval.NewRunner(gatewayURL, apiKey, agentID)
	results, err := runner.Run(context.Background(), suite)
	if err != nil {
		return fmt.Errorf("eval run failed: %w", err)
	}

	if jsonOut {
		out, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal results: %w", err)
		}
		fmt.Println(string(out))
	} else {
		eval.PrintReport(results, os.Stdout)
	}

	failures := 0
	for _, r := range results {
		if !r.Passed || r.Error != nil {
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d case(s) failed", failures)
	}
	return nil
}
