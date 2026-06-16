package scheduler

import (
	"context"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

// TestCronAutoDisableAfterConsecutiveFailures verifies Story 2 / S7.2: a cron
// agent that fails repeatedly is quarantined (disabled in memory) once it hits
// the consecutive-failure limit, and a success resets the streak.
func TestCronAutoDisableAfterConsecutiveFailures(t *testing.T) {
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})
	def := &agent.Definition{ID: "flaky", Name: "Flaky", Enabled: true,
		LLM: agent.LLMConfig{Provider: "ollama", Model: "llama3"}}
	if err := loader.Upsert(filepath.Join(dir), def); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	s := New(nil, loader, zap.NewNop(), context.Background())
	s.SetConsecutiveFailLimit(3)

	// Two failures: not yet at the limit, still enabled.
	s.recordFireResult("flaky", false)
	if disabled := s.recordFireResult("flaky", false); disabled {
		t.Fatal("agent disabled too early (before limit)")
	}
	if d := loader.Get("flaky"); d == nil || !d.Enabled {
		t.Fatal("agent should still be enabled after 2 failures")
	}

	// A success resets the streak.
	s.recordFireResult("flaky", true)

	// Now three fresh consecutive failures should trip the limit.
	s.recordFireResult("flaky", false)
	s.recordFireResult("flaky", false)
	if disabled := s.recordFireResult("flaky", false); !disabled {
		t.Fatal("agent should be auto-disabled at the 3rd consecutive failure")
	}
	if d := loader.Get("flaky"); d == nil || d.Enabled {
		t.Fatal("agent should be disabled in memory after hitting the limit")
	}
}

// TestCronAutoDisableOff verifies a non-positive limit turns the feature off.
func TestCronAutoDisableOff(t *testing.T) {
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})
	def := &agent.Definition{ID: "noisy", Enabled: true}
	if err := loader.Upsert(dir, def); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	s := New(nil, loader, zap.NewNop(), context.Background())
	s.SetConsecutiveFailLimit(0) // off
	for i := 0; i < 50; i++ {
		if s.recordFireResult("noisy", false) {
			t.Fatal("auto-disable should be off when limit <= 0")
		}
	}
	if d := loader.Get("noisy"); d == nil || !d.Enabled {
		t.Fatal("agent must remain enabled when feature is off")
	}
}
