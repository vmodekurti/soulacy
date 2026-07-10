package gateway

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/skill"
)

func (s *Server) handleListLearningProposals(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return c.JSON(fiber.Map{"enabled": false, "proposals": []learning.Proposal{}})
	}
	limit, _ := strconv.Atoi(c.Query("limit", "100"))
	proposals, err := store.List(c.Query("agent_id"), c.Query("status", learning.StatusPending), limit)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	// Augment each proposal with the explicit "affected agent" and a derived
	// "why it matters" so the UI can show trust context without recomputing it.
	views := make([]map[string]any, 0, len(proposals))
	for _, p := range proposals {
		views = append(views, learningProposalView(p))
	}
	return c.JSON(fiber.Map{"enabled": true, "proposals": views})
}

// learningProposalView renders a proposal as JSON with the extra trust fields.
func learningProposalView(p learning.Proposal) map[string]any {
	return map[string]any{
		"id":             p.ID,
		"agent_id":       p.AgentID,
		"affected_agent": p.AgentID,
		"session_id":     p.SessionID,
		"kind":           p.Kind,
		"title":          p.Title,
		"content":        p.Content,
		"status":         p.Status,
		"confidence":     p.Confidence,
		"source":         p.Source,
		"meta":           p.Meta,
		"created_at":     p.CreatedAt,
		"updated_at":     p.UpdatedAt,
		"disabled":       p.Disabled,
		"why":            p.Why(),
	}
}

// handleDisableLearningProposal toggles an accepted learning on/off without
// deleting it. Body: {"disabled": true|false} (defaults to true).
func (s *Server) handleDisableLearningProposal(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "learning is not enabled")
	}
	id := strings.TrimSpace(c.Params("id"))
	if id == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "id is required")
	}
	var req struct {
		Disabled *bool `json:"disabled"`
	}
	if len(c.Body()) > 0 {
		_ = c.BodyParser(&req)
	}
	disabled := true
	if req.Disabled != nil {
		disabled = *req.Disabled
	}
	p, err := store.SetDisabled(id, disabled)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.errMsg(c, fiber.StatusNotFound, "learning not found")
		}
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true, "proposal": learningProposalView(p)})
}

func (s *Server) handleLearningSummary(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return c.JSON(fiber.Map{
			"enabled": false,
			"summary": learning.Summary{AgentID: c.Query("agent_id")},
		})
	}
	summary, err := store.Summary(c.Query("agent_id"))
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(fiber.Map{"enabled": true, "summary": summary})
}

// handleLearningEvidence returns longitudinal proof that accepted learnings are
// paying off: how often each accepted learned skill was reused in real runs, and
// whether recurring errors happen less after learning was switched on. This is
// pure aggregation over the action log plus accepted proposals, so it is safe to
// call on demand from the Brain Memory UI.
func (s *Server) handleLearningEvidence(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return c.JSON(fiber.Map{
			"enabled":  false,
			"evidence": learning.Evidence{AgentID: c.Query("agent_id"), SkillReuse: []learning.SkillReuse{}, RepeatedErrors: []learning.ErrorTrend{}},
		})
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	accepted, err := store.List(agentID, learning.StatusAccepted, 0)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	var events []message.Event
	if s.actions != nil {
		limit, _ := strconv.Atoi(c.Query("limit", "5000"))
		if limit <= 0 {
			limit = 5000
		}
		// Tail scoped to the agent when one is given; otherwise pull a broad
		// recent slice and let BuildEvidence filter.
		events, err = s.actions.Tail(agentID, limit)
		if err != nil {
			return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
		}
	}
	evidence := learning.BuildEvidence(agentID, events, accepted)
	return c.JSON(fiber.Map{"enabled": true, "evidence": evidence})
}

func (s *Server) handleProposeLearningFromRun(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "learning proposal store not configured")
	}
	if s.actions == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "action logging disabled")
	}
	var body struct {
		AgentID      string `json:"agent_id"`
		SessionID    string `json:"session_id"`
		MaxProposals int    `json:"max_proposals"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	body.AgentID = strings.TrimSpace(body.AgentID)
	body.SessionID = strings.TrimSpace(body.SessionID)
	if body.AgentID == "" || body.SessionID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id and session_id are required")
	}
	def := s.loader.Get(body.AgentID)
	agentName := body.AgentID
	minChars := 80
	maxProposals := body.MaxProposals
	if def != nil {
		agentName = def.Name
		if def.Learning.MinChars > 0 {
			minChars = def.Learning.MinChars
		}
		if maxProposals <= 0 {
			maxProposals = def.Learning.MaxProposals
		}
	}
	if maxProposals <= 0 {
		maxProposals = 3
	}

	events, err := s.actions.Tail(body.AgentID, 5000)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	run := learning.RunFromEvents(events, body.AgentID, body.SessionID)
	if !run.FoundIn || !run.FoundOut {
		return s.errMsg(c, fiber.StatusBadRequest, "that session does not have both message.in and message.out events")
	}
	proposals := learning.BuildProposals(learning.BuildInput{
		AgentID:      body.AgentID,
		AgentName:    agentName,
		SessionID:    body.SessionID,
		Channel:      run.Channel,
		UserText:     run.UserText,
		ReplyText:    run.ReplyText,
		ToolsUsed:    run.Tools,
		Source:       "manual_run_review",
		MinChars:     minChars,
		MaxProposals: maxProposals,
	})
	if len(proposals) == 0 {
		return c.JSON(fiber.Map{"proposals": []learning.Proposal{}, "created": 0})
	}
	created := make([]learning.Proposal, 0, len(proposals))
	for _, p := range proposals {
		p.Meta["reviewed_from_activity"] = "true"
		added, err := store.Add(p)
		if err != nil {
			return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
		}
		created = append(created, added)
	}
	return c.JSON(fiber.Map{"proposals": created, "created": len(created)})
}

func (s *Server) handleReflectLearningFromRecentRuns(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "learning proposal store not configured")
	}
	if s.actions == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "action logging disabled")
	}
	var body struct {
		AgentID      string `json:"agent_id"`
		Limit        int    `json:"limit"`
		MaxRuns      int    `json:"max_runs"`
		MaxProposals int    `json:"max_proposals"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	body.AgentID = strings.TrimSpace(body.AgentID)
	if body.AgentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id is required")
	}
	if body.Limit <= 0 {
		body.Limit = 5000
	}
	if body.MaxRuns <= 0 {
		body.MaxRuns = 20
	}

	def := s.loader.Get(body.AgentID)
	agentName := body.AgentID
	minChars := 80
	maxProposals := body.MaxProposals
	if def != nil {
		agentName = def.Name
		if def.Learning.MinChars > 0 {
			minChars = def.Learning.MinChars
		}
		if maxProposals <= 0 {
			maxProposals = def.Learning.MaxProposals
		}
	}
	if maxProposals <= 0 {
		maxProposals = 3
	}

	events, err := s.actions.Tail(body.AgentID, body.Limit)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	runs := learning.RunsFromRecentEvents(events, body.AgentID, body.MaxRuns)
	created := make([]learning.Proposal, 0)
	reviewed := 0
	for _, run := range runs {
		if !run.FoundIn || !run.FoundOut {
			continue
		}
		reviewed++
		proposals := learning.BuildProposals(learning.BuildInput{
			AgentID:      body.AgentID,
			AgentName:    agentName,
			SessionID:    run.SessionID,
			Channel:      run.Channel,
			UserText:     run.UserText,
			ReplyText:    run.ReplyText,
			ToolsUsed:    run.Tools,
			Source:       "reflection_sweep",
			MinChars:     minChars,
			MaxProposals: maxProposals,
		})
		for _, p := range proposals {
			p.Meta["reviewed_from_activity"] = "true"
			p.Meta["reflection_sweep"] = "true"
			added, err := store.Add(p)
			if err != nil {
				return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
			}
			created = append(created, added)
		}
	}
	return c.JSON(fiber.Map{
		"proposals": created,
		"created":   len(created),
		"reviewed":  reviewed,
		"runs":      len(runs),
	})
}

func (s *Server) handleAcceptLearningProposal(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "learning proposal store not configured")
	}
	pending, err := findLearningProposal(store, c.Params("id"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.errMsg(c, fiber.StatusNotFound, "proposal not found")
		}
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	meta, err := s.applyLearningProposal(pending)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	p, err := store.UpdateStatusMeta(pending.ID, learning.StatusAccepted, meta)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(fiber.Map{"proposal": p})
}

func (s *Server) handleUpdateLearningProposal(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "learning proposal store not configured")
	}
	var body struct {
		Title   string            `json:"title"`
		Content string            `json:"content"`
		Meta    map[string]string `json:"meta"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	p, err := store.UpdateDraft(c.Params("id"), body.Title, body.Content, body.Meta)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.errMsg(c, fiber.StatusNotFound, "proposal not found")
		}
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(fiber.Map{"proposal": p})
}

func (s *Server) handleRejectLearningProposal(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "learning proposal store not configured")
	}
	p, err := store.UpdateStatus(c.Params("id"), learning.StatusRejected)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.errMsg(c, fiber.StatusNotFound, "proposal not found")
		}
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(fiber.Map{"proposal": p})
}

func findLearningProposal(store *learning.Store, id string) (learning.Proposal, error) {
	proposals, err := store.List("", "", 0)
	if err != nil {
		return learning.Proposal{}, err
	}
	for _, p := range proposals {
		if p.ID == id {
			return p, nil
		}
	}
	return learning.Proposal{}, os.ErrNotExist
}

func (s *Server) applyLearningProposal(p learning.Proposal) (map[string]string, error) {
	switch strings.ToLower(p.Kind) {
	case "skill":
		return s.installLearningSkill(p)
	case "procedure":
		brain := s.engine.BrainStore()
		if brain == nil {
			return nil, errors.New("brain memory not configured")
		}
		existing := strings.TrimSpace(brain.ProceduralRules(p.AgentID))
		next := strings.TrimSpace(p.Content)
		if existing != "" {
			next = existing + "\n\n" + next
		}
		if _, err := brain.UpdateProceduralVersioned(p.AgentID, next, "learning_accept"); err != nil {
			if errors.Is(err, agentmemory.ErrRulebookLocked) {
				return nil, errors.New("rulebook is locked; unlock it before accepting procedure proposals")
			}
			return nil, err
		}
	default:
		brain := s.engine.BrainStore()
		if brain == nil {
			return nil, errors.New("brain memory not configured")
		}
		rec := agentmemory.Record{
			AgentID:   p.AgentID,
			Type:      agentmemory.MemoryTypeSemantic,
			Timestamp: time.Now().UTC(),
			Content:   p.Content,
			Tags:      []string{"learning", p.Kind},
			Meta: map[string]string{
				"proposal_id": p.ID,
				"session_id":  p.SessionID,
				"source":      p.Source,
			},
		}
		if err := brain.Write(rec); err != nil {
			return nil, err
		}
	}
	s.log.Info("learning proposal accepted",
		zap.String("agent", p.AgentID), zap.String("proposal", p.ID), zap.String("kind", p.Kind))
	return nil, nil
}

func (s *Server) installLearningSkill(p learning.Proposal) (map[string]string, error) {
	if s.skillLoader == nil {
		return nil, errors.New("skill loader not configured")
	}
	content := strings.TrimSpace(p.Content)
	if content == "" {
		return nil, errors.New("skill proposal is empty")
	}
	wsPaths, err := config.ResolveWorkspace()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve workspace: %w", err)
	}
	name := learningSkillName(p)
	if name == "" {
		return nil, errors.New("skill proposal is missing a usable skill name")
	}
	dir := uniqueLearningSkillDir(wsPaths.Skills, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create skill dir: %w", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write SKILL.md: %w", err)
	}
	if parsed, err := skill.ParseFile(path); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("generated skill is invalid: %w", err)
	} else if warnings, fatal := parsed.Validate(); fatal != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("generated skill is invalid: %w", fatal)
	} else if len(warnings) > 0 {
		s.log.Debug("learning skill installed with warnings", zap.Strings("warnings", warnings))
	}
	if scanner, ok := s.skillLoader.(interface{ Scan() []error }); ok {
		_ = scanner.Scan()
	}
	s.log.Info("learning skill installed",
		zap.String("agent", p.AgentID), zap.String("proposal", p.ID), zap.String("skill", filepath.Base(dir)))
	return map[string]string{
		"skill_name":     filepath.Base(dir),
		"installed_path": path,
	}, nil
}

var learningSkillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$`)

func learningSkillName(p learning.Proposal) string {
	if p.Meta != nil {
		if name := normalizeLearningSkillName(p.Meta["skill_name"]); name != "" {
			return name
		}
	}
	return normalizeLearningSkillName(p.Title)
}

func normalizeLearningSkillName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "installable skill draft:")
	replacer := regexp.MustCompile(`[^a-z0-9]+`)
	s = replacer.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 64 {
		s = strings.Trim(s[:64], "-")
	}
	if !learningSkillNameRe.MatchString(s) {
		return ""
	}
	return s
}

func uniqueLearningSkillDir(root, name string) string {
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return dir
	}
	for i := 2; i < 1000; i++ {
		candidate := filepath.Join(root, fmt.Sprintf("%s-%d", name, i))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return filepath.Join(root, fmt.Sprintf("%s-%d", name, time.Now().UnixNano()))
}
