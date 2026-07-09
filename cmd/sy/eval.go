package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/soulacy/soulacy/internal/eval"
	"github.com/soulacy/soulacy/internal/studio"
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
	cmd.AddCommand(buildEvalGenerationCmd())
	return cmd
}

// buildEvalGenerationCmd runs the deterministic Studio generation-robustness
// corpus (offline, no gateway/LLM): each sample is a raw generated draft that
// the normalize+validate pipeline should repair (or, for the controls, flag).
// Exits non-zero on any regression so it runs in CI.
func buildEvalGenerationCmd() *cobra.Command {
	var extraCorpus string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "generation",
		Short: "Score Studio workflow-generation robustness (deterministic, offline)",
		Long: `Run the generation-robustness corpus: raw generated drafts covering known
failure classes are pushed through the deterministic normalize+validate pipeline
and scored on whether they become valid, renderable flows — no gateway or LLM.

Add cases distilled from accepted repairs with --corpus (a JSON array of
{"name","raw","recoverable"}).

Examples:
  sy eval generation
  sy eval generation --corpus ~/.soulacy/studio-corpus.json --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			samples := studio.BuiltinGenerationCorpus()
			if extraCorpus != "" {
				data, err := os.ReadFile(extraCorpus)
				if err != nil {
					return fmt.Errorf("read corpus %s: %w", extraCorpus, err)
				}
				extra, err := studio.LoadGenSamples(data)
				if err != nil {
					return fmt.Errorf("parse corpus %s: %w", extraCorpus, err)
				}
				samples = append(samples, extra...)
			}
			rep := studio.RunGenerationCorpus(samples)
			if jsonOut {
				out, _ := json.MarshalIndent(rep, "", "  ")
				fmt.Println(string(out))
			} else {
				fmt.Printf("Generation robustness: %d/%d recoverable drafts repaired deterministically (%.0f%%)\n",
					rep.Recovered, rep.RecoverableTotal, rep.Rate())
				for _, f := range rep.Failures {
					if f.Expected {
						fmt.Printf("  ✗ %s — should have become valid: %v\n", f.Name, f.Errors)
					} else {
						fmt.Printf("  ✗ %s — invalid draft slipped through as valid\n", f.Name)
					}
				}
			}
			if len(rep.Failures) > 0 {
				return fmt.Errorf("%d generation-robustness regression(s)", len(rep.Failures))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&extraCorpus, "corpus", "", "Path to an extra corpus JSON file (array of {name,raw,recoverable})")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output the report as JSON")
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
