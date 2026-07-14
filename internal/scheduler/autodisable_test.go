package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
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
	if disabled, count := s.recordFireResult("flaky", false); disabled || count != 2 {
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
	if disabled, count := s.recordFireResult("flaky", false); !disabled || count != 3 {
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
		if disabled, count := s.recordFireResult("noisy", false); disabled || count != i+1 {
			t.Fatal("auto-disable should be off when limit <= 0")
		}
	}
	if d := loader.Get("noisy"); d == nil || !d.Enabled {
		t.Fatal("agent must remain enabled when feature is off")
	}
}

func TestReportRunFailureEmitsActionableScheduleEvents(t *testing.T) {
	sink := &captureSink{}
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetEventSink(sink)

	def := &agent.Definition{ID: "daily-report", Name: "Daily Report"}
	msg := message.Message{SessionID: "sched-daily-report"}
	s.reportRunFailure(def, msg, "cron", errors.New("provider timeout"), 1500*time.Millisecond, 2, false)

	if len(sink.events) != 1 {
		t.Fatalf("expected one failure event, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Type != "schedule.run_failed" || ev.AgentID != "daily-report" || ev.SessionID != "sched-daily-report" {
		t.Fatalf("unexpected failure event: %+v", ev)
	}
	payload, ok := ev.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload should be map, got %T", ev.Payload)
	}
	if payload["consecutive_failures"] != 2 || payload["auto_disabled"] != false {
		t.Fatalf("failure payload should include count and disabled state, got %v", payload)
	}
	if payload["runbook"] == "" {
		t.Fatalf("failure event should include a runbook hint: %v", payload)
	}

	s.reportRunFailure(def, msg, "cron", errors.New("still broken"), 2*time.Second, 3, true)
	if len(sink.events) != 3 {
		t.Fatalf("expected failure + auto-disable events, got %d", len(sink.events))
	}
	if sink.events[2].Type != "schedule.auto_disabled" {
		t.Fatalf("expected auto-disable event, got %+v", sink.events[2])
	}
}
