// events.go — real-time event streaming hub for the GUI.
// The EventHub maintains a set of connected WebSocket clients and broadcasts
// structured JSON events to all of them. The agent engine emits events via
// the EventSink interface, which the hub implements.
//
// Events are used by the GUI to:
//   - Stream live log entries (message.in, message.out)
//   - Show tool call/result traces
//   - Display scheduler firing events
//   - Update the session activity timeline
package gateway

import (
	"encoding/json"
	"sync"
	"time"

	fws "github.com/gofiber/websocket/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/pkg/message"
)

// wsClient wraps a WebSocket connection with a buffered send queue so a slow or
// stale client can never block the broadcaster (and thus the agent engine).
type wsClient struct {
	conn *fws.Conn
	send chan []byte
}

// EventHub broadcasts events to all connected WebSocket clients and persists
// them to the per-agent action log.
//
// CRITICAL: broadcasting is non-blocking. Each client has a buffered send queue
// drained by its own writer goroutine. If a client falls behind (e.g. a
// backgrounded browser tab that stops reading), its queue fills and we DROP
// events for that client rather than blocking — because Emit runs on the agent
// execution path, and a blocked WriteMessage would otherwise freeze the agent
// mid-run.
type EventHub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
	log     *zap.Logger
	actions storage.ActionLogBackend // nil = persistence disabled
}

// NewEventHub creates an EventHub. actions may be nil to disable persistence.
func NewEventHub(log *zap.Logger, actions storage.ActionLogBackend) *EventHub {
	return &EventHub{
		clients: make(map[*wsClient]struct{}),
		log:     log,
		actions: actions,
	}
}

// Emit implements the runtime.EventSink interface. It persists the event to the
// per-agent action log, then broadcasts it to all connected WebSocket clients.
// It must never block — agent execution depends on it returning promptly.
func (h *EventHub) Emit(event message.Event) {
	if h.actions != nil {
		h.actions.Append(event)
	}
	data, err := json.Marshal(event)
	if err != nil {
		h.log.Error("event marshal failed", zap.Error(err))
		return
	}
	h.broadcast(data)
}

// broadcast enqueues data to every client's send buffer without blocking.
func (h *EventHub) broadcast(data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
			// Client's queue is full — it's too slow / not reading. Drop this
			// event for them rather than blocking the engine.
		}
	}
}

// Handler is the Fiber WebSocket handler. Each connecting client gets a buffered
// send queue and a dedicated writer goroutine; the read loop detects disconnect.
func (h *EventHub) Handler(conn *fws.Conn) {
	c := &wsClient{conn: conn, send: make(chan []byte, 256)}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	h.log.Debug("ws client connected", zap.String("remote", conn.RemoteAddr().String()))

	// Writer goroutine — the ONLY place we call WriteMessage. A write deadline
	// ensures a wedged connection errors out instead of hanging forever.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for data := range c.send {
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(1, data); err != nil { // 1 = TextMessage
				_ = conn.Close() // unblock the read loop so cleanup runs
				return
			}
		}
	}()

	// Welcome event (non-blocking).
	if welcome, err := json.Marshal(message.Event{
		Type: "connected", Payload: "Soulacy event stream active", Timestamp: time.Now().UTC(),
	}); err == nil {
		select {
		case c.send <- welcome:
		default:
		}
	}

	// Read loop — discard client frames; returns on disconnect.
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	// Remove from the broadcast set BEFORE closing send so broadcast never
	// sends on a closed channel.
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	close(c.send)
	<-writerDone

	h.log.Debug("ws client disconnected", zap.String("remote", conn.RemoteAddr().String()))
}

// PublishProgress forwards a ProgressEvent as an SSE frame to all connected
// GUI clients. The SSE event name is "progress".
func (h *EventHub) PublishProgress(ev message.ProgressEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		h.log.Error("progress event marshal failed", zap.Error(err))
		return
	}
	// Format as a proper SSE frame: "event: progress\ndata: <json>\n\n"
	frame := "event: progress\ndata: " + string(data) + "\n\n"
	h.broadcast([]byte(frame))
}

// ClientCount returns the number of connected WebSocket clients.
func (h *EventHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
