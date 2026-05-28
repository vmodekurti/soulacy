// Package channels defines the channel adapter interface and registry.
// A channel adapter is responsible for:
//   1. Connecting to a messaging platform.
//   2. Translating inbound platform events into canonical message.Message values.
//   3. Translating outbound message.Message values back into platform-specific sends.
//
// All adapters run concurrently. The gateway calls Start() on each enabled adapter
// at boot and Stop() on each during shutdown.
package channels

import (
	"context"
	"sync"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/pkg/message"
)

// Adapter is the interface every channel must implement.
type Adapter interface {
	// ID returns the unique identifier for this channel type (e.g. "telegram").
	ID() string

	// Name returns a human-readable display name.
	Name() string

	// Start connects to the platform and begins receiving messages.
	// Inbound messages must be posted to the provided inbox channel.
	// Start must be non-blocking — launch a goroutine internally.
	Start(ctx context.Context, inbox chan<- message.Message) error

	// Send delivers an outbound message to the platform.
	Send(ctx context.Context, msg message.Message) error

	// Stop gracefully disconnects from the platform.
	Stop() error

	// Status reports whether the adapter is connected.
	Status() AdapterStatus
}

// AdapterStatus describes the current connection state of a channel adapter.
type AdapterStatus struct {
	Connected  bool   `json:"connected"`
	Detail     string `json:"detail,omitempty"` // e.g. "polling" or error message
}

// Registry holds all registered channel adapters and routes outbound messages.
//
// Currently Register is only called during boot, so reads after Register
// returns are race-free in practice. The RWMutex is here so a future
// enable/disable-channel-at-runtime flow can mutate the map without
// triggering Go's concurrent-map-write detector. (PRODUCTION_AUDIT → HIGH.)
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
	inbox    chan message.Message
	log      *zap.Logger
}

// NewRegistry creates an empty channel registry with a shared inbox.
func NewRegistry(inboxBufferSize int) *Registry {
	return &Registry{
		adapters: make(map[string]Adapter),
		inbox:    make(chan message.Message, inboxBufferSize),
		log:      zap.NewNop(),
	}
}

// SetLogger attaches a zap logger to the registry for drop-event logging.
// Call this once at boot before messages start flowing.
func (r *Registry) SetLogger(l *zap.Logger) {
	r.log = l.Named("channels")
}

// Register adds an adapter. It will be started when StartAll is called.
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.ID()] = a
}

// Inbox returns the shared inbound message channel (read by the gateway router).
func (r *Registry) Inbox() <-chan message.Message { return r.inbox }

// Enqueue posts a message onto the shared inbox without blocking. Used by
// the gateway's startup crash-recovery (re-injecting in-flight runs that
// the previous process didn't get to finish) and by any future internal
// caller that needs to fan messages into the worker pool.
//
// Returns false if the inbox is full. We never block the caller: the
// recovery code is allowed to drop replays under heavy startup load, and
// the operator can re-trigger them manually if they care.
// (PRODUCTION_AUDIT → F2, 2026-05-27)
func (r *Registry) Enqueue(msg message.Message) bool {
	select {
	case r.inbox <- msg:
		return true
	default:
		// Inbox is full — increment the Prometheus drop counter and log at
		// ERROR level so operators can alert on this condition. A sustained
		// non-zero rate here means the inbox buffer (config: channel.inbox_buffer)
		// is too small for the current load, or agent processing has stalled.
		ch := msg.Channel
		if ch == "" {
			ch = "unknown"
		}
		metrics.ChannelInboxDropsTotal.WithLabelValues(ch).Inc()
		r.log.Error("channel inbox full — message dropped",
			zap.String("msg_id", msg.ID),
			zap.String("agent_id", msg.AgentID),
			zap.String("session_id", msg.SessionID),
			zap.String("channel", ch),
			zap.Int("inbox_cap", cap(r.inbox)),
		)
		return false
	}
}

// StartAll starts all registered adapters. Adapters that fail to start are logged
// but do not prevent others from starting.
func (r *Registry) StartAll(ctx context.Context) []error {
	// Snapshot under lock so Start() can't observe a partially-populated map
	// (and so a Start() that takes a while doesn't block Register).
	r.mu.RLock()
	snapshot := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		snapshot = append(snapshot, a)
	}
	r.mu.RUnlock()

	var errs []error
	for _, a := range snapshot {
		if err := a.Start(ctx, r.inbox); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// StopAll gracefully stops all adapters.
func (r *Registry) StopAll() []error {
	r.mu.RLock()
	snapshot := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		snapshot = append(snapshot, a)
	}
	r.mu.RUnlock()

	var errs []error
	for _, a := range snapshot {
		if err := a.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// Send routes an outbound message to the correct channel adapter.
func (r *Registry) Send(ctx context.Context, msg message.Message) error {
	r.mu.RLock()
	a, ok := r.adapters[msg.Channel]
	r.mu.RUnlock()
	if !ok {
		// Fallback: if no matching adapter, silently drop (or log in production)
		return nil
	}
	return a.Send(ctx, msg)
}

// Statuses returns a map of adapter ID → status for the admin API.
func (r *Registry) Statuses() map[string]AdapterStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := make(map[string]AdapterStatus, len(r.adapters))
	for id, a := range r.adapters {
		m[id] = a.Status()
	}
	return m
}
