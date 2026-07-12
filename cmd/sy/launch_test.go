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
