package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedDefaultsLoad guards the shipped templates: every embedded
// SOUL.yaml under embedded/ must parse cleanly and expose a non-empty
// display name and description. A typo in a template would otherwise only
// surface when a user opens the picker in the GUI.
func TestEmbeddedDefaultsLoad(t *testing.T) {
	cat := New("") // no user dir — pure embedded view
	entries, err := cat.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one embedded template, got 0")
	}
	for _, e := range entries {
		if e.Name == "" {
			t.Errorf("template missing Name (filename basename)")
		}
		if e.DisplayName == "" {
			t.Errorf("template %s missing display name (Definition.Name)", e.Name)
		}
		if strings.TrimSpace(e.Description) == "" {
			t.Errorf("template %s missing description", e.Name)
		}
		if e.Definition == nil {
			t.Errorf("template %s did not parse to a Definition", e.Name)
			continue
		}
		// Every template must declare a system_prompt; an empty one means
		// the agent will silently misbehave when instantiated.
		if strings.TrimSpace(e.Definition.SystemPrompt) == "" {
			t.Errorf("template %s has empty system_prompt", e.Name)
		}
		// Source must be embedded for these.
		if e.Source != "embedded" {
			t.Errorf("template %s: source=%q, want embedded", e.Name, e.Source)
		}
	}
}

// TestUserDirOverridesEmbedded confirms the precedence rule: a user-supplied
// template with the same basename as an embedded one wins. Without this, a
// user couldn't customise a shipped template by dropping a replacement.
func TestUserDirOverridesEmbedded(t *testing.T) {
	dir := t.TempDir()
	// Pick the first embedded name dynamically so this test doesn't break
	// when we add or rename a default template.
	embCat := New("")
	embAll, err := embCat.List()
	if err != nil || len(embAll) == 0 {
		t.Fatalf("need at least one embedded template; got err=%v len=%d", err, len(embAll))
	}
	target := embAll[0].Name
	userYAML := []byte("id: user-override\nname: User Override\ndescription: replaced\nsystem_prompt: hi\n")
	if err := os.WriteFile(filepath.Join(dir, target+".yaml"), userYAML, 0644); err != nil {
		t.Fatal(err)
	}

	cat := New(dir)
	entry, err := cat.Get(target)
	if err != nil {
		t.Fatalf("Get(%s): %v", target, err)
	}
	if entry.Source != "user" {
		t.Errorf("user file should shadow embedded; source=%q", entry.Source)
	}
	if entry.DisplayName != "User Override" {
		t.Errorf("user definition should win; got DisplayName=%q", entry.DisplayName)
	}
}

// TestInstantiateUniquesID confirms the uniqueness predicate is honoured —
// if "rag-over-docs" already exists, the next instantiation should land at
// "rag-over-docs-2", not collide.
func TestInstantiateUniquesID(t *testing.T) {
	cat := New("")
	taken := map[string]bool{"rag-over-docs": true, "rag-over-docs-2": true}
	mustBeUnique := func(id string) bool { return !taken[id] }

	def, err := cat.Instantiate("rag-over-docs", "", mustBeUnique)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if def.ID != "rag-over-docs-3" {
		t.Errorf("expected suffix-bumped ID rag-over-docs-3, got %q", def.ID)
	}
	if def.SourcePath != "" {
		t.Errorf("SourcePath should be cleared so Loader writes a fresh file; got %q", def.SourcePath)
	}
}

// TestInstantiateDesiredIDWins confirms a caller-supplied ID is respected
// when free.
func TestInstantiateDesiredIDWins(t *testing.T) {
	cat := New("")
	def, err := cat.Instantiate("basic-chat", "my-bot", func(string) bool { return true })
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if def.ID != "my-bot" {
		t.Errorf("desired ID should win; got %q", def.ID)
	}
}
