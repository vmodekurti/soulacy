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
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// Scheduler manages cron and one-shot agent triggers.
type Scheduler struct {
	cron    *cron.Cron
	engine  *runtime.Engine
	loader  *runtime.Loader // for per-agent run_timeout lookup
	log     *zap.Logger
	mu      sync.Mutex
	entries map[string]cron.EntryID // agentID → cron entry
	oneshot map[string]context.CancelFunc

	// appCtx is the gateway's app-wide context. Every fired run derives its
	// own context from this one so SIGTERM cancellation propagates through
	// engine.Handle → provider HTTP → tool subprocess.
	// (PRODUCTION_AUDIT → HIGH/Concurrency)
	appCtx context.Context

	runMu   sync.Mutex
	running map[string]time.Time // agentID → run start time (currently executing)
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
	}
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
		s.fire(agentID, "cron")
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
	timeout := def.ResolvedRunTimeout(15 * time.Minute)
	ctx, cancel := context.WithTimeout(s.appCtx, timeout)
	defer cancel()

	if _, err := s.engine.Handle(ctx, msg); err != nil {
		s.log.Error("scheduled agent execution failed",
			zap.String("agent", agentID),
			zap.Error(err),
		)
	}
}

// Entries returns a snapshot of all active cron schedules.
func (s *Scheduler) Entries() []ScheduleEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []ScheduleEntry
	for agentID, entryID := range s.entries {
		e := s.cron.Entry(entryID)
		entries = append(entries, ScheduleEntry{
			AgentID: agentID,
			Next:    e.Next,
			Prev:    e.Prev,
		})
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
}
