package main

import "testing"

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
