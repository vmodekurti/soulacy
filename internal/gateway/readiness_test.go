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
