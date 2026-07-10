package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/soulacy/soulacy/internal/studio"
)

// studio_lessons.go wires the Studio learning loop into the gateway: it locates
// the lessons file, records a lesson when a user ACCEPTS a repair, and grounds
// the generation catalog with the lessons relevant to the tools in play. Gated
// by cfg.LLM.Studio.Learning (default on).

// studioLearningEnabled reports whether Studio should record + inject lessons.
// nil (unset) means enabled; operators opt out with `llm.studio.learning: false`.
func (s *Server) studioLearningEnabled() bool {
	l := s.cfg.LLM.Studio.Learning
	return l == nil || *l
}

// lessonsPath resolves where learned lessons are stored, mirroring the studio
// run-trace resolution: explicit env → workspace → ~/.soulacy. Returns "" when
// no home/workspace is resolvable (learning then silently no-ops).
func lessonsPath() string {
	if p := os.Getenv("SOULACY_STUDIO_LESSONS"); p != "" {
		return p
	}
	ws := os.Getenv("SOULACY_WORKSPACE")
	if ws == "" {
		if home, err := os.UserHomeDir(); err == nil {
			ws = filepath.Join(home, ".soulacy")
		}
	}
	if ws == "" {
		return ""
	}
	return filepath.Join(ws, "studio-lessons.json")
}

// lessonStore returns the lesson store, or nil when learning is disabled or no
// path is resolvable. The store is cheap (just a path + mutex), so it's built
// per call rather than cached on the Server.
func (s *Server) lessonStore() *studio.LessonStore {
	if !s.studioLearningEnabled() {
		return nil
	}
	p := lessonsPath()
	if p == "" {
		return nil
	}
	return studio.NewLessonStore(p)
}

// recordLessonFromRepair distills an accepted repair into a durable lesson so
// future generations avoid the same shape mistake. Best-effort: any failure
// (learning off, no path, write error) is swallowed — learning must never break
// the apply flow.
func (s *Server) recordLessonFromRepair(wf studio.Draft, p studio.RepairProposal) {
	store := s.lessonStore()
	if store == nil {
		return
	}
	tool := ""
	for _, n := range wf.Flow.Nodes {
		if n.ID == p.NodeID {
			tool = n.Tool
			break
		}
	}
	if l, ok := studio.LessonFromProposal(p, tool, wf.Intent); ok {
		_ = store.Add(l)
	}
}

// corpusPath resolves where accepted-repair regression cases are stored, mirroring
// the lessons/runs path resolution. "" disables corpus capture.
func corpusPath() string {
	if p := os.Getenv("SOULACY_STUDIO_CORPUS"); p != "" {
		return p
	}
	ws := os.Getenv("SOULACY_WORKSPACE")
	if ws == "" {
		if home, err := os.UserHomeDir(); err == nil {
			ws = filepath.Join(home, ".soulacy")
		}
	}
	if ws == "" {
		return ""
	}
	return filepath.Join(ws, "studio-corpus.json")
}

// recordCorpusCase persists the FIXED draft as a recoverable generation-eval case
// so a future normalizer regression that breaks this now-correct shape is caught
// by `sy eval generation --corpus`. Best-effort.
func (s *Server) recordCorpusCase(fixed studio.Draft, nodeID string) {
	if !s.studioLearningEnabled() {
		return
	}
	p := corpusPath()
	if p == "" {
		return
	}
	raw, err := json.Marshal(fixed)
	if err != nil {
		return
	}
	name := strings.TrimSpace(fixed.Name)
	if name == "" {
		name = "workflow"
	}
	_ = studio.AppendGenSample(p, studio.GenSample{
		Name:        "repair:" + name + ":" + nodeID,
		Raw:         string(raw),
		Recoverable: true,
	}, 200)
}

// groundLessons populates the catalog with lessons relevant to the tools it can
// use (builtin tools + connected MCP tools), so BuildPrompt can inject them.
func (s *Server) groundLessons(cat *studio.Catalog) {
	store := s.lessonStore()
	if store == nil {
		return
	}
	tools := append([]string{}, cat.Tools...)
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			tools = append(tools, t.Name)
		}
	}
	cat.Lessons = store.Relevant(tools, 8)
}
