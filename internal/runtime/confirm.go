package runtime

import (
	"context"
	"sync"
)

// ConfirmRequest is the payload emitted as an SSE "tool_confirm" event.
// The client renders a dialog and POSTs the result back to /api/v1/chat/confirm.
type ConfirmRequest struct {
	CallID string         `json:"call_id"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Reason string         `json:"reason,omitempty"`
}

// ConfirmSenderFunc is called by the engine when a tool requires confirmation.
// It emits the SSE event and returns a channel that receives the user's decision.
type ConfirmSenderFunc func(req ConfirmRequest) <-chan bool

// confirmSenderKey is the context key for ConfirmSenderFunc.
type confirmSenderKey struct{}

// WithConfirmSender stores fn in ctx so the engine can emit confirm requests
// over the active SSE connection.
func WithConfirmSender(ctx context.Context, fn ConfirmSenderFunc) context.Context {
	return context.WithValue(ctx, confirmSenderKey{}, fn)
}

// confirmSenderFrom retrieves the ConfirmSenderFunc stored in ctx, if any.
func confirmSenderFrom(ctx context.Context) (ConfirmSenderFunc, bool) {
	fn, ok := ctx.Value(confirmSenderKey{}).(ConfirmSenderFunc)
	return fn, ok
}

// ConfirmBroker maps call IDs to pending approval channels.
// The gateway registers a result channel when it emits the SSE event;
// the confirm endpoint resolves it when the user approves or denies.
type ConfirmBroker struct {
	mu      sync.Mutex
	pending map[string]chan bool
}

// newConfirmBroker allocates a ConfirmBroker.
func newConfirmBroker() *ConfirmBroker {
	return &ConfirmBroker{pending: make(map[string]chan bool)}
}

// Register stores a result channel for callID and returns it.
// The engine blocks on this channel until Resolve is called.
func (b *ConfirmBroker) Register(callID string) chan bool {
	ch := make(chan bool, 1)
	b.mu.Lock()
	b.pending[callID] = ch
	b.mu.Unlock()
	return ch
}

// Resolve delivers the user's decision (approved) for callID.
// Returns true if callID was found and the decision was delivered.
func (b *ConfirmBroker) Resolve(callID string, approved bool) bool {
	b.mu.Lock()
	ch, ok := b.pending[callID]
	if ok {
		delete(b.pending, callID)
	}
	b.mu.Unlock()
	if ok {
		ch <- approved
	}
	return ok
}
