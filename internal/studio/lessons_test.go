package studio

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLessonFromProposal(t *testing.T) {
	// A shape drift with a rationale becomes a tool-scoped lesson.
	p := RepairProposal{
		NodeID: "fmt", Field: "input", Class: RepairShapeDrift,
		Rationale: "The API response has no \"results\"; the list is under \"items\".",
		ObservedKeys: []string{"items", "meta"},
	}
	l, ok := LessonFromProposal(p, "web_search", "news digest")
	if !ok {
		t.Fatal("expected a lesson")
	}
	if !strings.Contains(l.Guidance, "web_search") || !strings.Contains(l.Guidance, "items") {
		t.Fatalf("guidance missing tool/shape: %q", l.Guidance)
	}
	if l.Count != 1 || l.ID == "" {
		t.Fatalf("bad lesson: %+v", l)
	}

	// A tool_failure teaches nothing durable.
	if _, ok := LessonFromProposal(RepairProposal{Class: RepairToolFailure, Rationale: "401"}, "web_search", ""); ok {
		t.Error("tool_failure should not produce a lesson")
	}
}

func TestLessonStore_AddMergeAndRelevant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lessons.json")
	st := NewLessonStore(path)

	l, _ := LessonFromProposal(RepairProposal{
		Class: RepairShapeDrift, Rationale: "list is under items", ObservedKeys: []string{"items"},
	}, "web_search", "")
	if err := st.Add(l); err != nil {
		t.Fatal(err)
	}
	// Same lesson again → merge (Count++), not a duplicate.
	if err := st.Add(l); err != nil {
		t.Fatal(err)
	}
	all := st.All()
	if len(all) != 1 || all[0].Count != 2 {
		t.Fatalf("expected 1 merged lesson count=2, got %+v", all)
	}

	// A different tool's lesson.
	l2, _ := LessonFromProposal(RepairProposal{
		Class: RepairShapeDrift, Rationale: "response is a JSON string; parse it",
	}, "http_get", "")
	_ = st.Add(l2)

	// Relevant to web_search only → the http_get lesson is excluded.
	rel := st.Relevant([]string{"web_search"}, 8)
	if len(rel) != 1 || rel[0].Tool != "web_search" {
		t.Fatalf("relevance filter failed: %+v", rel)
	}
	// Relevant to both → both.
	if got := st.Relevant([]string{"web_search", "http_get"}, 8); len(got) != 2 {
		t.Fatalf("expected 2 relevant, got %d", len(got))
	}
	// Ranked by Count: web_search (2) before http_get (1).
	if got := st.Relevant([]string{"web_search", "http_get"}, 8); got[0].Tool != "web_search" {
		t.Fatalf("expected count ranking, got %+v", got)
	}
}

// A general (tool-less) lesson surfaces regardless of tools in use.
func TestLessonStore_GeneralLessonAlwaysRelevant(t *testing.T) {
	st := NewLessonStore(filepath.Join(t.TempDir(), "l.json"))
	l, _ := LessonFromProposal(RepairProposal{Class: RepairShapeDrift, Rationale: "always wrap with toJson"}, "", "")
	_ = st.Add(l)
	if got := st.Relevant([]string{"anything"}, 8); len(got) != 1 {
		t.Fatalf("general lesson should always be relevant, got %+v", got)
	}
}

func TestLessonsPromptBlock(t *testing.T) {
	if LessonsPromptBlock(nil) != "" {
		t.Error("empty lessons should yield empty block")
	}
	block := LessonsPromptBlock([]Lesson{
		{Guidance: "When using `web_search`: list is under items", ObservedKeys: []string{"items", "meta"}},
	})
	if !strings.Contains(block, "LESSONS FROM PAST RUNS") || !strings.Contains(block, "web_search") {
		t.Fatalf("block missing content: %q", block)
	}
	if !strings.Contains(block, "observed keys: items, meta") {
		t.Fatalf("block missing observed keys: %q", block)
	}
}

// BuildPrompt injects the lessons block when the catalog carries lessons.
func TestBuildPrompt_InjectsLessons(t *testing.T) {
	cat := Catalog{
		Tools:   []string{"web_search"},
		Lessons: []Lesson{{Guidance: "When using `web_search`: results are under items"}},
	}
	prompt := BuildPrompt("build a news digest", cat, nil)
	if !strings.Contains(prompt, "LESSONS FROM PAST RUNS") || !strings.Contains(prompt, "results are under items") {
		t.Fatal("BuildPrompt did not inject lessons")
	}
}
