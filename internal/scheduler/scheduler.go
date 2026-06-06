// Package scheduler runs agents on a time-based schedule.
// Two trigger types are supported:
//   - Cron: agents triggered on recurring schedules (standard 5-field cron expressions)
//   - OneShot: agents triggered once at a specific UTC time
//
// The Scheduler integrates with the Engine: when a trigger fires, it synthesises a
// message.Message and passes it to engine.Handle(), just like a channel message would.
// This means scheduled agents have full access to memory, tools, and LLM routing.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// Scheduler manages cron and one-shot agent triggers.
type Scheduler struct {
	cron     *cron.Cron
	engine   *runtime.Engine
	loader   *runtime.Loader // for per-agent run_timeout lookup
	channels *channels.Registry
	log      *zap.Logger
	mu       sync.Mutex
	entries  map[string]cron.EntryID // agentID → cron entry
	oneshot  map[string]context.CancelFunc

	// appCtx is the gateway's app-wide context. Every fired run derives its
	// own context from this one so SIGTERM cancellation propagates through
	// engine.Handle → provider HTTP → tool subprocess.
	// (PRODUCTION_AUDIT → HIGH/Concurrency)
	appCtx context.Context

	runMu   sync.Mutex
	running map[string]time.Time // agentID → run start time (currently executing)

	stateMu   sync.Mutex
	statePath string
	state     scheduleState
}

type scheduleState struct {
	LastCompleted map[string]time.Time `json:"last_completed,omitempty"`
}

// cronParser accepts standard 5-field cron expressions ("0 7 * * *") AND
// optional-seconds 6-field expressions ("*/30 * * * * *") plus @descriptors
// (@daily, @hourly). Without SecondOptional, WithSeconds would reject the
// 5-field expressions the GUI and example agents use, so nothing would schedule.
var cronParser = cron.NewParser(
	cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// New creates a new Scheduler. Call Start() to begin processing.
//
// appCtx is the gateway's lifetime context — fired runs derive from it so
// the gateway's SIGTERM handler cancels them all. Pass context.Background()
// only in tests.
func New(engine *runtime.Engine, loader *runtime.Loader, log *zap.Logger, appCtx context.Context) *Scheduler {
	if appCtx == nil {
		appCtx = context.Background()
	}
	return &Scheduler{
		cron:    cron.New(cron.WithParser(cronParser)),
		engine:  engine,
		loader:  loader,
		log:     log,
		appCtx:  appCtx,
		entries: make(map[string]cron.EntryID),
		oneshot: make(map[string]context.CancelFunc),
		running: make(map[string]time.Time),
		state:   scheduleState{LastCompleted: make(map[string]time.Time)},
	}
}

// SetStatePath enables durable scheduler bookkeeping. The scheduler uses this
// to remember completed cron fires across host restarts so opt-in agents can
// catch up when a shutdown overlapped their scheduled time.
func (s *Scheduler) SetStatePath(path string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.statePath = strings.TrimSpace(path)
}

// SetChannelRegistry enables scheduled runs to send successful replies to a
// configured channel output target. It is optional so scheduler tests and
// embedded uses can run without channel adapters.
func (s *Scheduler) SetChannelRegistry(reg *channels.Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels = reg
}

// maxRunDuration is the safety cap on the run-lock staleness check. It needs
// to be at least as long as the slowest agent's run_timeout, otherwise a
// legitimately long run would be treated as "stale" and a concurrent run
// would start while it's still active. 1 hour covers the audio-generation
// chains; individual agents can still declare shorter run_timeout values.
const maxRunDuration = 1 * time.Hour

// TryStartRun marks an agent as running. Returns false if it is already running
// (within maxRunDuration), so callers can prevent overlapping/duplicate executions.
// A stale run past maxRunDuration is overwritten so the agent isn't locked forever.
func (s *Scheduler) TryStartRun(agentID string) bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if started, ok := s.running[agentID]; ok && time.Since(started) < maxRunDuration {
		return false
	}
	s.running[agentID] = time.Now()
	return true
}

// FinishRun clears an agent's running state.
func (s *Scheduler) FinishRun(agentID string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	delete(s.running, agentID)
}

// IsRunning reports whether an agent is currently executing (and not stale).
func (s *Scheduler) IsRunning(agentID string) bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	started, ok := s.running[agentID]
	return ok && time.Since(started) < maxRunDuration
}

// RunningSnapshot returns a copy of currently-running agents and their start
// times, excluding stale entries past maxRunDuration.
func (s *Scheduler) RunningSnapshot() map[string]time.Time {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	out := make(map[string]time.Time, len(s.running))
	for k, v := range s.running {
		if time.Since(v) < maxRunDuration {
			out[k] = v
		}
	}
	return out
}

// Start begins the cron daemon.
func (s *Scheduler) Start() {
	s.cron.Start()
	s.log.Info("scheduler started")
	s.runMissedOnStartup()
}

// Stop gracefully halts the cron daemon and cancels pending one-shots.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.oneshot {
		cancel()
	}
	s.log.Info("scheduler stopped")
}

// RegisterAgent adds the agent's schedule to the scheduler.
// Call this after LoadAll() and whenever an agent definition is upserted.
func (s *Scheduler) RegisterAgent(def *agent.Definition) error {
	if def.Schedule == nil || !def.Enabled {
		return nil
	}
	switch def.Trigger {
	case agent.TriggerCron:
		return s.addCron(def)
	case agent.TriggerOneShot:
		return s.addOneShot(def)
	}
	return nil
}

// DeregisterAgent removes a scheduled agent. Safe to call if not registered.
func (s *Scheduler) DeregisterAgent(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.entries[agentID]; ok {
		s.cron.Remove(id)
		delete(s.entries, agentID)
	}
	if cancel, ok := s.oneshot[agentID]; ok {
		cancel()
		delete(s.oneshot, agentID)
	}
}

func (s *Scheduler) addCron(def *agent.Definition) error {
	if def.Schedule.Cron == "" {
		return fmt.Errorf("scheduler: cron expression is empty for agent %s", def.ID)
	}

	agentID := def.ID
	entryID, err := s.cron.AddFunc(def.Schedule.Cron, func() {
		s.fireAt(agentID, "cron", time.Now().UTC())
	})
	if err != nil {
		return fmt.Errorf("scheduler: invalid cron expression %q: %w", def.Schedule.Cron, err)
	}

	s.mu.Lock()
	// Remove previous entry if re-registering
	if old, ok := s.entries[agentID]; ok {
		s.cron.Remove(old)
	}
	s.entries[agentID] = entryID
	s.mu.Unlock()

	s.log.Info("cron agent registered",
		zap.String("agent", agentID),
		zap.String("expr", def.Schedule.Cron),
	)
	return nil
}

func (s *Scheduler) addOneShot(def *agent.Definition) error {
	if def.Schedule.At.IsZero() {
		return fmt.Errorf("scheduler: one-shot time is zero for agent %s", def.ID)
	}

	delay := time.Until(def.Schedule.At)
	if delay <= 0 {
		s.log.Warn("one-shot trigger is in the past, firing immediately",
			zap.String("agent", def.ID))
		delay = 0
	}

	// Derived from s.appCtx so SIGTERM cancels pending one-shots.
	// (PRODUCTION_AUDIT → HIGH/Concurrency)
	ctx, cancel := context.WithCancel(s.appCtx)
	s.mu.Lock()
	if oldCancel, ok := s.oneshot[def.ID]; ok {
		oldCancel()
	}
	s.oneshot[def.ID] = cancel
	s.mu.Unlock()

	agentID := def.ID
	go func() {
		select {
		case <-time.After(delay):
			s.fire(agentID, "oneshot")
			s.mu.Lock()
			delete(s.oneshot, agentID)
			s.mu.Unlock()
		case <-ctx.Done():
		}
	}()

	s.log.Info("one-shot agent scheduled",
		zap.String("agent", def.ID),
		zap.Time("at", def.Schedule.At),
	)
	return nil
}

// fire synthesises a trigger message and dispatches it to the engine.
func (s *Scheduler) fire(agentID, triggerType string) {
	s.fireAt(agentID, triggerType, time.Now().UTC())
}

// fire synthesises a trigger message and dispatches it to the engine.
func (s *Scheduler) fireAt(agentID, triggerType string, scheduledAt time.Time) {
	// Prevent overlapping runs: if a manual or previous scheduled run is still
	// executing, skip this fire rather than running the agent twice concurrently.
	if !s.TryStartRun(agentID) {
		s.log.Warn("skipping scheduled run — agent already running",
			zap.String("agent", agentID), zap.String("trigger", triggerType))
		return
	}
	defer s.FinishRun(agentID)

	s.log.Info("firing scheduled agent",
		zap.String("agent", agentID),
		zap.String("trigger", triggerType),
	)

	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: fmt.Sprintf("sched-%s-%d", agentID, time.Now().UnixNano()),
		AgentID:   agentID,
		Channel:   "internal",
		ThreadID:  "scheduler",
		UserID:    "scheduler",
		Username:  "scheduler",
		Role:      message.RoleUser,
		Parts:     message.Text(fmt.Sprintf("__trigger:%s__", triggerType)),
		Metadata:  map[string]string{"trigger": triggerType},
		CreatedAt: time.Now().UTC(),
	}

	// Honor the agent's declared run_timeout (e.g. NotebookLM pipelines need
	// 30-45m). Fall back to a generous 15-min default for agents that don't
	// override it. Derived from s.appCtx so SIGTERM cancels in-flight runs
	// (PRODUCTION_AUDIT → HIGH/Concurrency: previously context.Background()
	// here meant graceful shutdown could hang for the full run_timeout).
	def := s.loader.Get(agentID)
	if def == nil {
		s.log.Error("scheduled agent definition missing", zap.String("agent", agentID))
		return
	}
	timeout := def.ResolvedRunTimeout(15 * time.Minute)
	ctx, cancel := context.WithTimeout(s.appCtx, timeout)
	defer cancel()

	runStart := time.Now()
	reply, err := s.engine.Handle(ctx, msg)
	elapsed := time.Since(runStart).Round(time.Millisecond)
	if err != nil {
		s.log.Error("scheduled agent execution failed",
			zap.String("agent", agentID),
			zap.String("trigger", triggerType),
			zap.Duration("elapsed", elapsed),
			zap.Error(err),
		)
		return
	}
	if triggerType == "cron" || triggerType == "cron_missed_startup" {
		s.markScheduleCompleted(agentID, scheduledAt)
	}
	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	s.sendScheduledOutput(ctx, def, msg, replyText, triggerType)
	s.log.Info("scheduled agent completed",
		zap.String("agent", agentID),
		zap.String("trigger", triggerType),
		zap.Duration("elapsed", elapsed),
		zap.Int("reply_len", len(replyText)),
		zap.String("reply_preview", func() string {
			if len(replyText) > 200 {
				return replyText[:200] + "…"
			}
			return replyText
		}()),
	)
}

func (s *Scheduler) runMissedOnStartup() {
	if s.loader == nil {
		return
	}
	now := time.Now().UTC()
	for _, def := range s.loader.All() {
		missedAt, ok := s.missedCronFire(def, now)
		if !ok {
			continue
		}
		s.log.Warn("running missed cron from startup catch-up",
			zap.String("agent", def.ID),
			zap.Time("missed_at", missedAt))
		go s.fireAt(def.ID, "cron_missed_startup", missedAt)
	}
}

func (s *Scheduler) missedCronFire(def *agent.Definition, now time.Time) (time.Time, bool) {
	if def == nil || !def.Enabled || def.Trigger != agent.TriggerCron || def.Schedule == nil {
		return time.Time{}, false
	}
	if !def.Schedule.RunMissedOnStartup || strings.TrimSpace(def.Schedule.Cron) == "" {
		return time.Time{}, false
	}
	sched, err := cronParser.Parse(def.Schedule.Cron)
	if err != nil {
		s.log.Warn("missed cron check skipped invalid expression",
			zap.String("agent", def.ID),
			zap.String("expr", def.Schedule.Cron),
			zap.Error(err))
		return time.Time{}, false
	}
	window := 24 * time.Hour
	if raw := strings.TrimSpace(def.Schedule.MissedStartupWindow); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			s.log.Warn("missed cron check using default window after invalid duration",
				zap.String("agent", def.ID),
				zap.String("missed_startup_window", raw))
		} else {
			window = parsed
		}
	}

	s.stateMu.Lock()
	if err := s.loadStateLocked(); err != nil {
		s.log.Warn("scheduler state load failed; missed cron check will use empty state", zap.Error(err))
	}
	lastCompleted := s.state.LastCompleted[def.ID]
	s.stateMu.Unlock()

	from := now.Add(-window)
	if lastCompleted.After(from) {
		from = lastCompleted
	}
	next := sched.Next(from)
	var latest time.Time
	for next.After(from) && !next.After(now) {
		latest = next.UTC()
		next = sched.Next(next)
	}
	if latest.IsZero() {
		return time.Time{}, false
	}
	if !lastCompleted.IsZero() && !latest.After(lastCompleted) {
		return time.Time{}, false
	}
	return latest, true
}

func (s *Scheduler) markScheduleCompleted(agentID string, completedAt time.Time) {
	completedAt = completedAt.UTC()
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err := s.loadStateLocked(); err != nil {
		s.log.Warn("scheduler state load failed before update", zap.Error(err))
	}
	if s.state.LastCompleted == nil {
		s.state.LastCompleted = make(map[string]time.Time)
	}
	if prev := s.state.LastCompleted[agentID]; prev.After(completedAt) {
		return
	}
	s.state.LastCompleted[agentID] = completedAt
	if err := s.saveStateLocked(); err != nil {
		s.log.Warn("scheduler state save failed", zap.String("agent", agentID), zap.Error(err))
	}
}

func (s *Scheduler) loadStateLocked() error {
	if s.state.LastCompleted == nil {
		s.state.LastCompleted = make(map[string]time.Time)
	}
	if s.statePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var loaded scheduleState
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	if loaded.LastCompleted == nil {
		loaded.LastCompleted = make(map[string]time.Time)
	}
	s.state = loaded
	return nil
}

func (s *Scheduler) saveStateLocked() error {
	if s.statePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath)
}

func (s *Scheduler) sendScheduledOutput(ctx context.Context, def *agent.Definition, source message.Message, replyText, triggerType string) {
	if def == nil || def.Schedule == nil || def.Schedule.Output == nil || strings.TrimSpace(replyText) == "" {
		return
	}
	outCfg := def.Schedule.Output
	channelID := strings.TrimSpace(outCfg.Channel)
	to := strings.TrimSpace(outCfg.To)
	if channelID == "" || to == "" {
		return
	}

	s.mu.Lock()
	reg := s.channels
	s.mu.Unlock()
	if reg == nil {
		s.log.Warn("scheduled output configured but channel registry is unavailable",
			zap.String("agent", def.ID),
			zap.String("channel", channelID),
			zap.String("to", to),
			zap.String("bot_name", outCfg.BotName))
		return
	}
	if _, ok := reg.Statuses()[channelID]; !ok {
		s.log.Error("scheduled output adapter not registered",
			zap.String("agent", def.ID),
			zap.String("channel", channelID),
			zap.String("to", to),
			zap.String("bot_name", outCfg.BotName))
		return
	}

	text := renderScheduledOutput(outCfg.Template, def, replyText, triggerType)
	out := message.Message{
		ID:        uuid.New().String(),
		SessionID: source.SessionID,
		AgentID:   def.ID,
		Channel:   channelID,
		ThreadID:  to,
		UserID:    "scheduler",
		Username:  "scheduler",
		Role:      message.RoleAssistant,
		Parts:     message.Text(text),
		Metadata: map[string]string{
			"trigger":  triggerType,
			"bot_name": outCfg.BotName,
		},
		CreatedAt: time.Now().UTC(),
	}
	if err := reg.Send(ctx, out); err != nil {
		s.log.Error("scheduled output send failed",
			zap.String("agent", def.ID),
			zap.String("channel", channelID),
			zap.String("to", to),
			zap.String("bot_name", outCfg.BotName),
			zap.Error(err))
		return
	}
	s.log.Info("scheduled output sent",
		zap.String("agent", def.ID),
		zap.String("channel", channelID),
		zap.String("to", to),
		zap.String("bot_name", outCfg.BotName))
}

func renderScheduledOutput(tpl string, def *agent.Definition, replyText, triggerType string) string {
	if strings.TrimSpace(tpl) == "" {
		return replyText
	}
	replacements := map[string]string{
		"{reply}":      replyText,
		"{agent_id}":   def.ID,
		"{agent_name}": def.Name,
		"{trigger}":    triggerType,
		"{timestamp}":  time.Now().UTC().Format(time.RFC3339),
	}
	out := tpl
	for k, v := range replacements {
		out = strings.ReplaceAll(out, k, v)
	}
	return out
}

// Entries returns a snapshot of all active cron schedules.
func (s *Scheduler) Entries() []ScheduleEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []ScheduleEntry
	for agentID, entryID := range s.entries {
		e := s.cron.Entry(entryID)
		se := ScheduleEntry{
			AgentID: agentID,
			Next:    e.Next,
			Prev:    e.Prev,
		}
		// Surface missed-run catch-up settings (Story 12) so the Schedule
		// GUI can explain restart behaviour per agent.
		if s.loader != nil {
			if def := s.loader.Get(agentID); def != nil && def.Schedule != nil && def.Schedule.RunMissedOnStartup {
				se.CatchUp = true
				se.CatchUpWindow = strings.TrimSpace(def.Schedule.MissedStartupWindow)
				if se.CatchUpWindow == "" {
					se.CatchUpWindow = "24h" // documented default
				}
			}
		}
		entries = append(entries, se)
	}
	for agentID := range s.oneshot {
		entries = append(entries, ScheduleEntry{
			AgentID: agentID,
			Type:    "oneshot",
		})
	}
	return entries
}

// ScheduleEntry is a summary of one scheduled agent for the admin API.
type ScheduleEntry struct {
	AgentID string    `json:"agent_id"`
	Type    string    `json:"type,omitempty"` // "cron" or "oneshot"
	Next    time.Time `json:"next,omitempty"`
	Prev    time.Time `json:"prev,omitempty"`

	// CatchUp reports run_missed_on_startup; CatchUpWindow is the
	// missed_startup_window ("24h" default). Story 12: lets the GUI show
	// which agents recover missed fires after a restart.
	CatchUp       bool   `json:"catch_up,omitempty"`
	CatchUpWindow string `json:"catch_up_window,omitempty"`
}
