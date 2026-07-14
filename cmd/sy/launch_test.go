package main

import (
	"encoding/json"
	"testing"
)

func TestLaunchStrictError(t *testing.T) {
	if err := launchStrictError("needs_setup", false); err != nil {
		t.Fatalf("advisory mode should not fail: %v", err)
	}
	if err := launchStrictError("ready", true); err != nil {
		t.Fatalf("ready strict mode should not fail: %v", err)
	}
	if err := launchStrictError("at_risk", true); err == nil {
		t.Fatal("strict mode should fail when launch readiness is not ready")
	}
}

func TestLaunchCertifyEnv(t *testing.T) {
	env := launchCertifyEnv(launchCertifyOptions{
		ReportDir:     "/tmp/soulacy-cert",
		Quick:         true,
		LiveChannels:  true,
		BrowserMCP:    true,
		BrowserRender: true,
		StudioLive:    true,
	})
	want := map[string]bool{
		"SOULACY_PARITY_REPORT_DIR=/tmp/soulacy-cert": true,
		"SOULACY_PARITY_QUICK=1":                      true,
		"SOULACY_PARITY_LIVE_CHANNELS=1":              true,
		"SOULACY_PARITY_BROWSER_MCP=1":                true,
		"SOULACY_PARITY_BROWSER_RENDER=1":             true,
		"SOULACY_PARITY_STUDIO_LIVE=1":                true,
	}
	for _, got := range env {
		delete(want, got)
	}
	for missing := range want {
		t.Fatalf("launchCertifyEnv missing %q in %#v", missing, env)
	}
}

func TestLaunchReadinessParsesParityPayload(t *testing.T) {
	var r launchReadiness
	payload := []byte(`{
		"summary": {"status":"at_risk","score":72},
		"deployment": {"profile":"production","label":"Production","status":"warn","score":78,"ready":7,"total":9,"strict":true,"owner":"platform","region":"us-central"},
		"parity": {
			"score": 64,
			"top_gaps": [
				{"key":"channels","label":"Channel Reach","status":"warn","score":55,"benchmark":"OpenClaw","next":"Add channel sidecars."}
			]
		}
	}`)
	if err := json.Unmarshal(payload, &r); err != nil {
		t.Fatalf("unmarshal launch readiness: %v", err)
	}
	if r.Parity.Score != 64 || len(r.Parity.TopGaps) != 1 {
		t.Fatalf("parity payload = %#v", r.Parity)
	}
	if r.Deployment.Profile != "production" || !r.Deployment.Strict || r.Deployment.Ready != 7 {
		t.Fatalf("deployment payload = %#v", r.Deployment)
	}
	if r.Parity.TopGaps[0].Benchmark != "OpenClaw" {
		t.Fatalf("benchmark = %q", r.Parity.TopGaps[0].Benchmark)
	}
}
