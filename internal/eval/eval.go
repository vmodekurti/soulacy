package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Suite is a collection of test cases for an agent.
type Suite struct {
	Name  string `yaml:"name" json:"name"`
	Cases []Case `yaml:"cases" json:"cases"`
}

// Case is one evaluation test case.
type Case struct {
	Name                string   `yaml:"name" json:"name"`
	Input               string   `yaml:"input" json:"input"`
	ExpectedContains    []string `yaml:"expected_contains" json:"expected_contains"`    // all must appear in reply (case-insensitive)
	ExpectedNotContains []string `yaml:"expected_not_contains" json:"expected_not_contains"` // none must appear
	MaxTokens           int      `yaml:"max_tokens" json:"max_tokens"`                  // 0 = no limit check
	TimeoutSec          int      `yaml:"timeout_sec" json:"timeout_sec"`                // default 30
}

// Result is the outcome of running one Case.
type Result struct {
	Case    Case
	Passed  bool
	Reply   string
	Latency time.Duration
	Tokens  int
	Error   error
	Reasons []string // why it failed
}

// Runner executes a Suite against a live gateway.
type Runner struct {
	GatewayURL string
	APIKey     string
	AgentID    string
	UserID     string // default "eval-runner"
}

// NewRunner creates a new Runner with the given gateway URL, API key, and agent ID.
func NewRunner(gatewayURL, apiKey, agentID string) *Runner {
	return &Runner{
		GatewayURL: gatewayURL,
		APIKey:     apiKey,
		AgentID:    agentID,
		UserID:     "eval-runner",
	}
}

// Run executes all cases in a suite sequentially and returns results.
func (r *Runner) Run(ctx context.Context, suite Suite) ([]Result, error) {
	results := make([]Result, 0, len(suite.Cases))
	for _, c := range suite.Cases {
		result := r.runCase(ctx, c)
		results = append(results, result)
	}
	return results, nil
}

// runCase executes a single test case and returns its result.
func (r *Runner) runCase(ctx context.Context, c Case) Result {
	timeoutSec := c.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	caseCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	userID := r.UserID
	if userID == "" {
		userID = "eval-runner"
	}

	body, err := json.Marshal(map[string]string{
		"agent_id": r.AgentID,
		"user_id":  userID,
		"text":     c.Input,
	})
	if err != nil {
		return Result{Case: c, Error: fmt.Errorf("failed to marshal request: %w", err)}
	}

	req, err := http.NewRequestWithContext(caseCtx, http.MethodPost, r.GatewayURL+"/api/v1/chat", bytes.NewReader(body))
	if err != nil {
		return Result{Case: c, Error: fmt.Errorf("failed to create request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	start := time.Now()
	client := &http.Client{}
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return Result{Case: c, Latency: latency, Error: fmt.Errorf("request failed: %w", err)}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{Case: c, Latency: latency, Error: fmt.Errorf("failed to read response: %w", err)}
	}

	if resp.StatusCode >= 400 {
		return Result{Case: c, Latency: latency, Error: fmt.Errorf("gateway error %d: %s", resp.StatusCode, string(data))}
	}

	var parsed struct {
		Reply  string `json:"reply"`
		Tokens int    `json:"tokens"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Result{Case: c, Latency: latency, Error: fmt.Errorf("failed to parse response: %w", err)}
	}

	var reasons []string
	replyLower := strings.ToLower(parsed.Reply)

	for _, expected := range c.ExpectedContains {
		if !strings.Contains(replyLower, strings.ToLower(expected)) {
			reasons = append(reasons, fmt.Sprintf("missing expected: %q", expected))
		}
	}

	for _, forbidden := range c.ExpectedNotContains {
		if strings.Contains(replyLower, strings.ToLower(forbidden)) {
			reasons = append(reasons, fmt.Sprintf("found forbidden: %q", forbidden))
		}
	}

	if c.MaxTokens > 0 && parsed.Tokens > c.MaxTokens {
		reasons = append(reasons, fmt.Sprintf("token limit exceeded: %d > %d", parsed.Tokens, c.MaxTokens))
	}

	return Result{
		Case:    c,
		Passed:  len(reasons) == 0,
		Reply:   parsed.Reply,
		Latency: latency,
		Tokens:  parsed.Tokens,
		Reasons: reasons,
	}
}

// PrintReport writes a formatted report of eval results to w.
func PrintReport(results []Result, w io.Writer) {
	passed := 0
	total := len(results)

	// Header
	fmt.Fprintf(w, "%-30s  %-6s  %-10s  %-6s  %s\n", "CASE", "RESULT", "LATENCY", "TOKENS", "REASON")
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 80))

	for _, r := range results {
		status := "PASS"
		reason := ""
		if r.Error != nil {
			status = "ERROR"
			reason = r.Error.Error()
		} else if !r.Passed {
			status = "FAIL"
			reason = strings.Join(r.Reasons, "; ")
		} else {
			passed++
		}

		latency := "-"
		if r.Latency > 0 {
			latency = r.Latency.Round(time.Millisecond).String()
		}

		tokens := "-"
		if r.Tokens > 0 {
			tokens = fmt.Sprintf("%d", r.Tokens)
		}

		name := r.Case.Name
		if len(name) > 29 {
			name = name[:26] + "..."
		}
		if len(reason) > 40 {
			reason = reason[:37] + "..."
		}

		fmt.Fprintf(w, "%-30s  %-6s  %-10s  %-6s  %s\n", name, status, latency, tokens, reason)
	}

	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 80))
	fmt.Fprintf(w, "%d/%d passed\n", passed, total)
}

// LoadSuiteFromJSON parses a Suite from JSON bytes.
func LoadSuiteFromJSON(data []byte) (Suite, error) {
	var suite Suite
	if err := json.Unmarshal(data, &suite); err != nil {
		return Suite{}, fmt.Errorf("failed to parse eval suite: %w", err)
	}
	return suite, nil
}
