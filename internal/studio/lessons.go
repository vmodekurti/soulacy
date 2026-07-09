package studio

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// lessons.go closes the learning loop: every repair the user ACCEPTS after a Run
// Live is distilled into a durable Lesson (a real API shape that broke a node
// plus the guidance that fixed it), and relevant lessons are injected back into
// the generation prompt so Studio builds flows that work the first time. The
// store is a small JSON file — no external infra — so it's easy to test and to
// carry between sessions.

// Lesson is one thing Studio learned from a real run. Guidance is the
// prompt-ready sentence; Tool scopes relevance so a lesson about web_search only
// surfaces when web_search is in play. Count tracks how often the same lesson
// recurred (higher = more worth honoring).
type Lesson struct {
	ID           string    `json:"id"`
	Tool         string    `json:"tool,omitempty"`
	Class        string    `json:"class,omitempty"`
	ObservedKeys []string  `json:"observed_keys,omitempty"`
	Guidance     string    `json:"guidance"`
	Count        int       `json:"count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// LessonFromProposal derives a Lesson from an accepted repair. It only learns
// from shape/format classes (a tool_failure is an auth/network issue, not a
// reusable shape fact). tool is the failing node's Tool (may be empty for agent/
// python nodes). Returns ok=false when there's nothing durable to learn.
func LessonFromProposal(p RepairProposal, tool, intent string) (Lesson, bool) {
	if p.Class != RepairShapeDrift && p.Class != RepairTemplateError {
		return Lesson{}, false
	}
	guidance := strings.TrimSpace(p.Rationale)
	if guidance == "" {
		return Lesson{}, false
	}
	// Prefix with the tool so the injected block reads as scoped advice.
	if tool != "" {
		guidance = "When using `" + tool + "`: " + guidance
	}
	now := time.Now().UTC()
	l := Lesson{
		Tool:         tool,
		Class:        string(p.Class),
		ObservedKeys: p.ObservedKeys,
		Guidance:     guidance,
		Count:        1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	l.ID = lessonID(l)
	return l, true
}

// lessonID is a stable dedup key over tool + guidance so the same lesson learned
// twice merges (Count++) instead of duplicating.
func lessonID(l Lesson) string {
	h := sha1.Sum([]byte(strings.ToLower(l.Tool + "\x00" + l.Guidance)))
	return hex.EncodeToString(h[:8])
}

// LessonStore persists lessons as a single JSON array file, guarded by a mutex.
// Small by design; the whole file is loaded/merged/rewritten per Add.
type LessonStore struct {
	path string
	mu   sync.Mutex
}

// NewLessonStore returns a store backed by path (created lazily on first Add).
func NewLessonStore(path string) *LessonStore { return &LessonStore{path: path} }

// All returns every stored lesson (newest-updated first). A missing file is not
// an error — it just means nothing has been learned yet.
func (s *LessonStore) All() []Lesson {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *LessonStore) loadLocked() []Lesson {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var ls []Lesson
	if json.Unmarshal(b, &ls) != nil {
		return nil
	}
	return ls
}

// Add merges a lesson into the store: an existing lesson with the same ID has
// its Count incremented and UpdatedAt refreshed; otherwise it's appended.
func (s *LessonStore) Add(l Lesson) error {
	if strings.TrimSpace(l.Guidance) == "" {
		return nil
	}
	if l.ID == "" {
		l.ID = lessonID(l)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ls := s.loadLocked()
	for i := range ls {
		if ls[i].ID == l.ID {
			ls[i].Count++
			ls[i].UpdatedAt = time.Now().UTC()
			if len(l.ObservedKeys) > 0 {
				ls[i].ObservedKeys = l.ObservedKeys
			}
			return s.writeLocked(ls)
		}
	}
	ls = append(ls, l)
	return s.writeLocked(ls)
}

func (s *LessonStore) writeLocked(ls []Lesson) error {
	b, err := json.MarshalIndent(ls, "", "  ")
	if err != nil {
		return err
	}
	// Write-rename would be ideal, but the target dir may be a mount that blocks
	// rename/unlink; a direct rewrite is sufficient for this small file.
	return os.WriteFile(s.path, b, 0o644)
}

// Relevant returns lessons worth injecting for a generation that will use the
// given tools: tool-scoped lessons whose Tool is in use, plus general (Tool="")
// lessons, ranked by Count then recency, capped at limit.
func (s *LessonStore) Relevant(tools []string, limit int) []Lesson {
	all := s.All()
	if len(all) == 0 {
		return nil
	}
	inUse := map[string]bool{}
	for _, t := range tools {
		inUse[t] = true
	}
	var picked []Lesson
	for _, l := range all {
		if l.Tool == "" || inUse[l.Tool] {
			picked = append(picked, l)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool {
		if picked[i].Count != picked[j].Count {
			return picked[i].Count > picked[j].Count
		}
		return picked[i].UpdatedAt.After(picked[j].UpdatedAt)
	})
	if limit > 0 && len(picked) > limit {
		picked = picked[:limit]
	}
	return picked
}

// LessonsPromptBlock renders lessons as a prompt section, mirroring
// RulesPromptBlock. Empty input yields "" so the prompt is unchanged when
// nothing has been learned.
func LessonsPromptBlock(lessons []Lesson) string {
	if len(lessons) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nLESSONS FROM PAST RUNS — real API shapes and fixes observed before; apply them so the flow works the first time:\n")
	for _, l := range lessons {
		sb.WriteString("- ")
		sb.WriteString(strings.TrimSpace(l.Guidance))
		if len(l.ObservedKeys) > 0 {
			sb.WriteString(" (observed keys: " + strings.Join(l.ObservedKeys, ", ") + ")")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
