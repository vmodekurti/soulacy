// loader_test.go — round-trip and edge-case tests for the agent loader.
// Verifies Upsert/Delete behaviour and the legacy-flat-file migration so the
// agent registry doesn't regress on the fixed-issues list (phantom agents,
// missing IDs, double-loading).
package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

func timeFromSec(s int64) time.Duration { return time.Duration(s) * time.Second }

func TestLoader_UpsertCreatesFolderLayout(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	def := &agent.Definition{
		ID:           "test-agent",
		Name:         "Test Agent",
		Description:  "for tests",
		Trigger:      agent.TriggerChannel,
		SystemPrompt: "you are a tester",
		Enabled:      true,
	}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	expected := filepath.Join(dir, "test-agent", "SOUL.yaml")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected agent file at %s, got error %v", expected, err)
	}
	if got := l.Get("test-agent"); got == nil {
		t.Error("Get after Upsert returned nil")
	} else if got.Name != "Test Agent" {
		t.Errorf("agent name: got %q, want %q", got.Name, "Test Agent")
	}
}

func TestLoader_DeleteRemovesFileAndEntry(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	def := &agent.Definition{ID: "deletable", Trigger: agent.TriggerChannel, Enabled: true}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := l.Delete("deletable"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := l.Get("deletable"); got != nil {
		t.Error("Delete should remove from registry; Get returned non-nil")
	}
	// Folder should also be gone (empty folder cleanup happens in Delete).
	if _, err := os.Stat(filepath.Join(dir, "deletable")); !os.IsNotExist(err) {
		t.Errorf("Delete should remove the agent folder, stat err: %v", err)
	}
}

func TestLoader_DeleteIsIdempotent(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	if err := l.Delete("ghost"); err != nil {
		t.Errorf("Delete on absent agent should be nil, got %v", err)
	}
}

func TestLoader_LoadAllParsesValidSOUL(t *testing.T) {
	dir := t.TempDir()
	soul := []byte(`id: parse-test
name: Parse Test
trigger: channel
system_prompt: test
llm:
  provider: ollama
  model: llama3
enabled: true
agents:
  - peer-one
  - peer-two
knowledge:
  - kb-x
`)
	agentDir := filepath.Join(dir, "parse-test")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "SOUL.yaml"), soul, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	l := NewLoader([]string{dir})
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll errors: %v", errs)
	}
	def := l.Get("parse-test")
	if def == nil {
		t.Fatal("Get returned nil for loaded agent")
	}
	if len(def.Agents) != 2 || def.Agents[0] != "peer-one" {
		t.Errorf("agents list: got %v", def.Agents)
	}
	if len(def.Knowledge) != 1 || def.Knowledge[0] != "kb-x" {
		t.Errorf("knowledge list: got %v", def.Knowledge)
	}
}

func TestLoader_RejectsMissingID(t *testing.T) {
	dir := t.TempDir()
	soul := []byte(`name: NoID Agent
trigger: channel
`)
	path := filepath.Join(dir, "noid.yaml")
	if err := os.WriteFile(path, soul, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	l := NewLoader([]string{dir})
	errs := l.LoadAll()
	if len(errs) == 0 {
		t.Error("expected LoadAll to surface missing-id error, got none")
	}
}

func TestLoader_UpsertRequiresID(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	if err := l.Upsert(t.TempDir(), &agent.Definition{Name: "no-id"}); err == nil {
		t.Error("Upsert without ID should fail")
	}
}

func TestDefinition_ResolvedRunTimeout(t *testing.T) {
	tests := []struct {
		name     string
		def      *agent.Definition
		fallback int64
		want     int64
	}{
		{"nil def returns fallback", nil, 60, 60},
		{"empty run_timeout returns fallback", &agent.Definition{}, 60, 60},
		{"valid duration parsed", &agent.Definition{RunTimeout: "30s"}, 60, 30},
		{"invalid duration returns fallback", &agent.Definition{RunTimeout: "garbage"}, 60, 60},
		{"zero duration returns fallback", &agent.Definition{RunTimeout: "0s"}, 60, 60},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.def.ResolvedRunTimeout(timeFromSec(tc.fallback))
			if got != timeFromSec(tc.want) {
				t.Errorf("got %v, want %v", got, timeFromSec(tc.want))
			}
		})
	}
}
