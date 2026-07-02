package learning

import (
	"path/filepath"
	"testing"
)

func TestStoreAddListAndDedupe(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "learning.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p, err := store.Add(Proposal{
		AgentID:   "agent-a",
		SessionID: "session-a",
		Kind:      "memory",
		Title:     "remember this",
		Content:   "Task: hello\n\nOutcome: world",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	dup, err := store.Add(Proposal{
		AgentID:   "agent-a",
		SessionID: "session-a",
		Kind:      "memory",
		Content:   "Task: hello\n\nOutcome: world",
	})
	if err != nil {
		t.Fatalf("Add duplicate: %v", err)
	}
	if dup.ID != p.ID {
		t.Fatalf("duplicate ID = %q, want %q", dup.ID, p.ID)
	}
	proposals, err := store.List("agent-a", StatusPending, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(proposals) != 1 || proposals[0].Status != StatusPending {
		t.Fatalf("proposals = %#v, want one pending", proposals)
	}
}

func TestStoreUpdateStatusPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p, err := store.Add(Proposal{AgentID: "agent-a", Content: "learned thing"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.UpdateStatus(p.ID, StatusAccepted); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.List("agent-a", StatusAccepted, 10)
	if err != nil {
		t.Fatalf("List reopened: %v", err)
	}
	if len(got) != 1 || got[0].ID != p.ID {
		t.Fatalf("accepted proposals = %#v, want %s", got, p.ID)
	}
}

func TestStoreUpdateStatusMetaPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p, err := store.Add(Proposal{
		AgentID: "agent-a",
		Kind:    "skill",
		Content: "skill draft",
		Meta:    map[string]string{"skill_name": "draft-skill"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	updated, err := store.UpdateStatusMeta(p.ID, StatusAccepted, map[string]string{
		"installed_path": "/tmp/draft-skill/SKILL.md",
	})
	if err != nil {
		t.Fatalf("UpdateStatusMeta: %v", err)
	}
	if updated.Meta["skill_name"] != "draft-skill" || updated.Meta["installed_path"] == "" {
		t.Fatalf("meta not merged: %#v", updated.Meta)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.List("agent-a", StatusAccepted, 10)
	if err != nil {
		t.Fatalf("List reopened: %v", err)
	}
	if len(got) != 1 || got[0].Meta["installed_path"] == "" {
		t.Fatalf("accepted proposals = %#v, want installed_path", got)
	}
}

func TestStoreUpdateDraftPersistsAndLocksReviewed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p, err := store.Add(Proposal{
		AgentID: "agent-a",
		Title:   "original",
		Content: "old content",
		Meta:    map[string]string{"skill_name": "old-skill"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	updated, err := store.UpdateDraft(p.ID, "edited", "new content", map[string]string{"skill_name": "new-skill"})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if updated.Title != "edited" || updated.Content != "new content" || updated.Meta["skill_name"] != "new-skill" {
		t.Fatalf("updated = %#v", updated)
	}
	if _, err := store.UpdateStatus(p.ID, StatusAccepted); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if _, err := store.UpdateDraft(p.ID, "late edit", "should fail", nil); err == nil {
		t.Fatal("expected reviewed proposal edit to fail")
	}
}
