package gateway

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
)

type costReadinessCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type costReadiness struct {
	Status           string               `json:"status"`
	Score            int                  `json:"score"`
	Ready            int                  `json:"ready"`
	Total            int                  `json:"total"`
	PricingRules     int                  `json:"pricing_rules"`
	DailyBudgetUSD   float64              `json:"daily_budget_usd"`
	MonthlyBudgetUSD float64              `json:"monthly_budget_usd"`
	AlertThreshold   float64              `json:"alert_threshold"`
	Last24hUSD       float64              `json:"last_24h_usd"`
	Last30dUSD       float64              `json:"last_30d_usd"`
	Checks           []costReadinessCheck `json:"checks"`
	NextActions      []string             `json:"next_actions"`
}

func (s *Server) handleCostStatus(c *fiber.Ctx) error {
	return c.JSON(s.costReadiness(c))
}

func (s *Server) costReadiness(c *fiber.Ctx) costReadiness {
	pricingRules := 0
	dailyBudget := 0.0
	monthlyBudget := 0.0
	threshold := 0.8
	if s != nil && s.cfg != nil {
		pricingRules = len(s.cfg.Costs.Pricing)
		dailyBudget = s.cfg.Costs.DailyBudgetUSD
		monthlyBudget = s.cfg.Costs.MonthlyBudgetUSD
		if s.cfg.Costs.AlertThreshold > 0 {
			threshold = s.cfg.Costs.AlertThreshold
		}
	}

	last24h := 0.0
	last30d := 0.0
	tracking := s != nil && s.costStore != nil
	if tracking {
		now := time.Now()
		last24h = s.sumCostSince(c, now.Add(-24*time.Hour))
		last30d = s.sumCostSince(c, now.Add(-30*24*time.Hour))
	}
	budgetsConfigured := dailyBudget > 0 || monthlyBudget > 0

	checks := []costReadinessCheck{
		{
			Key:    "tracking",
			Label:  "Usage Tracking",
			Status: statusIf(tracking, "ok", "warn"),
			Detail: statusDetail(tracking, "Token and estimated cost usage are persisted.", "Cost store is not enabled; runs cannot be budgeted from usage."),
		},
		{
			Key:    "pricing",
			Label:  "Pricing Rules",
			Status: statusIf(pricingRules > 0, "ok", "warn"),
			Detail: countDetail(pricingRules, "pricing rule", "No pricing rules are configured; usage records will show $0 for unknown models."),
		},
		{
			Key:    "budgets",
			Label:  "Budgets",
			Status: statusIf(budgetsConfigured, "ok", "warn"),
			Detail: costBudgetDetail(dailyBudget, monthlyBudget, threshold),
		},
		{
			Key:    "daily_threshold",
			Label:  "Daily Guardrail",
			Status: budgetThresholdStatus(dailyBudget, last24h, threshold, tracking),
			Detail: budgetThresholdDetail("24h", dailyBudget, last24h, threshold, tracking),
		},
		{
			Key:    "monthly_threshold",
			Label:  "Monthly Guardrail",
			Status: budgetThresholdStatus(monthlyBudget, last30d, threshold, tracking),
			Detail: budgetThresholdDetail("30d", monthlyBudget, last30d, threshold, tracking),
		},
	}

	ready := 0
	points := 0
	next := make([]string, 0, 4)
	for _, check := range checks {
		switch check.Status {
		case "ok":
			ready++
			points += 100
		case "warn":
			points += 60
			next = append(next, costReadinessNextAction(check.Key))
		default:
			next = append(next, costReadinessNextAction(check.Key))
		}
	}
	score := 0
	if len(checks) > 0 {
		score = points / len(checks)
	}
	if len(next) > 4 {
		next = next[:4]
	}

	return costReadiness{
		Status:           statusFromScore(score),
		Score:            score,
		Ready:            ready,
		Total:            len(checks),
		PricingRules:     pricingRules,
		DailyBudgetUSD:   dailyBudget,
		MonthlyBudgetUSD: monthlyBudget,
		AlertThreshold:   threshold,
		Last24hUSD:       last24h,
		Last30dUSD:       last30d,
		Checks:           checks,
		NextActions:      compactStrings(next),
	}
}

func (s *Server) sumCostSince(c *fiber.Ctx, since time.Time) float64 {
	if s == nil || s.costStore == nil {
		return 0
	}
	ctx := c.Context()
	rows, err := s.costStore.SumByAgent(ctx, since)
	if err != nil {
		return 0
	}
	total := 0.0
	for _, row := range rows {
		total += row.CostUSD
	}
	return total
}

func parityOps(providersReady, enabledAgents int, updateManifest string, cost costReadiness, slo sloReadiness) parityArea {
	if providersReady > 0 && enabledAgents > 0 && updateManifest != "" && cost.Status == "ok" && slo.Status == "ok" {
		return parityArea{Key: "ops", Label: "Ops & Release Confidence", Status: "ok", Score: 92, Detail: "Readiness, doctor, support bundles, action logs, parity harness, updates, cost guardrails, deployment profiles, and SLO checks are wired.", Next: "Wire alert delivery for SLO breaches and budget threshold changes.", Benchmark: "Commercial launch", Href: "#dashboard"}
	}
	status := "warn"
	score := 62
	if providersReady == 0 || enabledAgents == 0 {
		status = "fail"
		score = 38
	}
	if cost.Status == "fail" || slo.Status == "fail" {
		status = "fail"
		score = 45
	}
	if cost.Status == "ok" && slo.Status == "ok" && providersReady > 0 && enabledAgents > 0 {
		score = 72
	}
	next := "Run launch checks, configure update manifest, and keep support-bundle download visible."
	if len(cost.NextActions) > 0 {
		next = cost.NextActions[0]
	}
	if len(slo.NextActions) > 0 && (slo.Status == "fail" || cost.Status == "ok") {
		next = slo.NextActions[0]
	}
	return parityArea{Key: "ops", Label: "Ops & Release Confidence", Status: status, Score: score, Detail: fmt.Sprintf("Diagnostics, run metrics, and support bundles exist; cost guardrails are %s and SLO status is %s.", cost.Status, slo.Status), Next: next, Benchmark: "Commercial launch", Href: "#dashboard"}
}

func costBudgetDetail(daily, monthly, threshold float64) string {
	if daily <= 0 && monthly <= 0 {
		return "No daily or monthly cost budget configured."
	}
	return fmt.Sprintf("Budgets configured: daily $%.2f, monthly $%.2f, alert at %.0f%%.", daily, monthly, threshold*100)
}

func budgetThresholdStatus(limit, spent, threshold float64, tracking bool) string {
	if limit <= 0 {
		return "warn"
	}
	if !tracking {
		return "warn"
	}
	if spent >= limit {
		return "fail"
	}
	if spent >= limit*threshold {
		return "warn"
	}
	return "ok"
}

func budgetThresholdDetail(label string, limit, spent, threshold float64, tracking bool) string {
	if limit <= 0 {
		return fmt.Sprintf("No %s budget configured.", label)
	}
	if !tracking {
		return fmt.Sprintf("%s budget is $%.2f, but usage tracking is unavailable.", label, limit)
	}
	return fmt.Sprintf("%s spend is $%.2f of $%.2f (alert at %.0f%%).", label, spent, limit, threshold*100)
}

func costReadinessNextAction(key string) string {
	switch key {
	case "tracking":
		return "Enable the cost store so token and dollar usage are persisted."
	case "pricing":
		return "Add provider/model pricing rules in Config."
	case "budgets":
		return "Set daily and monthly cost budgets in Config."
	case "daily_threshold":
		return "Set a daily budget and alert threshold for run-away agents."
	case "monthly_threshold":
		return "Set a monthly budget and review top-cost agents weekly."
	default:
		return "Review cost controls in Config."
	}
}
