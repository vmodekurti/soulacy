package gateway

import (
	"net/http"
	"testing"
)

func TestReadinessEndpointReturnsProductJourney(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	createBody := `{
		"id": "ready-agent",
		"name": "Ready Agent",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "test", "model": "fake-model"},
		"system_prompt": "Help.",
		"enabled": true,
		"learning": {"enabled": true}
	}`
	if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody); status != http.StatusCreated {
		t.Fatalf("create status = %d body=%v", status, body)
	}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("readiness status = %d body=%v", status, body)
	}
	summary, ok := body["summary"].(map[string]any)
	if !ok {
		t.Fatalf("missing summary: %#v", body)
	}
	if got, _ := summary["enabled_agents"].(float64); got < 1 {
		t.Fatalf("enabled_agents = %v, want at least 1", summary["enabled_agents"])
	}
	if _, ok := summary["updates_ready"].(bool); !ok {
		t.Fatalf("missing updates_ready: %#v", summary)
	}
	release, ok := body["release"].(map[string]any)
	if !ok {
		t.Fatalf("missing release metadata: %#v", body)
	}
	if _, ok := release["update_hint"].(string); !ok {
		t.Fatalf("missing release update_hint: %#v", release)
	}
	journey, ok := body["journey"].([]any)
	if !ok || len(journey) < 6 {
		t.Fatalf("journey = %#v", body["journey"])
	}
	if _, ok := body["next_actions"].([]any); !ok {
		t.Fatalf("missing next_actions: %#v", body)
	}
	parity, ok := body["parity"].(map[string]any)
	if !ok {
		t.Fatalf("missing parity cockpit: %#v", body)
	}
	if got, _ := parity["score"].(float64); got <= 0 {
		t.Fatalf("parity score = %v, want positive", parity["score"])
	}
	if areas, ok := parity["areas"].([]any); !ok || len(areas) < 8 {
		t.Fatalf("parity areas = %#v", parity["areas"])
	}
	if _, ok := body["executors"].(map[string]any); !ok {
		t.Fatalf("missing executors readiness: %#v", body)
	}
	if gaps, ok := parity["top_gaps"].([]any); !ok || len(gaps) == 0 {
		t.Fatalf("parity top gaps = %#v", parity["top_gaps"])
	}
}

func TestReadinessUsesConfiguredUpdateManifest(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.cfg.Updates.ManifestURL = "https://releases.example.test/soulacy/manifest.json"

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("readiness status = %d body=%v", status, body)
	}
	summary := body["summary"].(map[string]any)
	if got, _ := summary["updates_ready"].(bool); !got {
		t.Fatalf("updates_ready = %v, want true", summary["updates_ready"])
	}
	release := body["release"].(map[string]any)
	if got := release["update_manifest"]; got != "https://releases.example.test/soulacy/manifest.json" {
		t.Fatalf("update_manifest = %#v", got)
	}
}

func TestReadinessScoreReachesHundredWhenEverythingIsReady(t *testing.T) {
	items := []readinessItem{
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
		{Status: "ok"},
	}
	if got := readinessScore(items); got != 100 {
		t.Fatalf("readinessScore(all ok) = %d, want 100", got)
	}
}

func TestReadinessScoreWeightsWarningsAndBlockers(t *testing.T) {
	items := []readinessItem{
		{Status: "ok"},
		{Status: "warn"},
		{Status: "fail"},
	}
	if got := readinessScore(items); got != 51 {
		t.Fatalf("readinessScore(mixed) = %d, want 51", got)
	}
}

func TestReadinessStatusCounts(t *testing.T) {
	items := []readinessItem{
		{Status: "ok"},
		{Status: "warn"},
		{Status: "fail"},
		{Status: "missing"},
	}
	ready, warnings, blockers := readinessStatusCounts(items)
	if ready != 1 || warnings != 1 || blockers != 2 {
		t.Fatalf("counts = ready:%d warnings:%d blockers:%d, want 1/1/2", ready, warnings, blockers)
	}
}

func TestParityGapsPrioritizeLowestScores(t *testing.T) {
	areas := []parityArea{
		{Key: "studio", Status: "ok", Score: 90},
		{Key: "enterprise", Status: "fail", Score: 25},
		{Key: "mobile", Status: "warn", Score: 50},
		{Key: "channels", Status: "warn", Score: 55},
	}
	gaps := topParityGaps(areas, 2)
	if len(gaps) != 2 {
		t.Fatalf("gaps len = %d, want 2", len(gaps))
	}
	if gaps[0].Key != "enterprise" || gaps[1].Key != "mobile" {
		t.Fatalf("gaps order = %#v", gaps)
	}
	if got := parityScore(areas); got != 55 {
		t.Fatalf("parityScore = %d, want 55", got)
	}
}

func TestParityEnterpriseReflectsConfiguredControls(t *testing.T) {
	area := parityEnterprise(enterpriseParityPosture{
		Controls: []string{"authenticated API", "RBAC policies", "managed API keys", "encrypted credential vault", "audit log directory"},
		Missing:  nil,
		Score:    78,
		Status:   "warn",
	})
	if area.Status != "warn" || area.Score != 78 {
		t.Fatalf("enterprise area = %#v, want warn/78", area)
	}
	if area.Detail == "" || area.Next == "" {
		t.Fatalf("enterprise area should explain current controls and next step: %#v", area)
	}
}

func TestParityEnterpriseFailsWhenControlsAreAbsent(t *testing.T) {
	area := parityEnterprise(enterpriseParityPosture{
		Missing: []string{"authenticated API", "RBAC policies"},
		Score:   25,
		Status:  "fail",
	})
	if area.Status != "fail" || area.Score != 25 {
		t.Fatalf("enterprise area = %#v, want fail/25", area)
	}
}
