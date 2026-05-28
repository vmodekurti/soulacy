// Package message defines the canonical message types that flow through Soulacy.
// All channel adapters translate their platform-specific formats into these types,
// ensuring the runtime never has to know which channel a message came from.
package message

import "time"

// Role identifies the origin of a message in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// ContentType describes the media type of a message part.
type ContentType string

const (
	ContentText  ContentType = "text"
	ContentImage ContentType = "image"
	ContentAudio ContentType = "audio"
	ContentFile  ContentType = "file"
)

// Part is one piece of a (possibly multi-modal) message body.
type Part struct {
	Type     ContentType `json:"type"`
	Text     string      `json:"text,omitempty"`
	MimeType string      `json:"mime_type,omitempty"`
	Data     []byte      `json:"data,omitempty"` // base64 decoded
	URL      string      `json:"url,omitempty"`
}

// Message is the canonical inbound/outbound message shared across all subsystems.
type Message struct {
	ID        string            `json:"id"`
	SessionID string            `json:"session_id"`
	AgentID   string            `json:"agent_id"`
	Channel   string            `json:"channel"`   // e.g. "telegram", "discord", "http"
	ThreadID  string            `json:"thread_id"` // channel-native thread/conversation id
	UserID    string            `json:"user_id"`
	Username  string            `json:"username"`
	Role      Role              `json:"role"`
	Parts     []Part            `json:"parts"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// Text is a convenience constructor for a plain-text message.
func Text(text string) []Part {
	return []Part{{Type: ContentText, Text: text}}
}

// ToolCall represents a request from the LLM to call a specific tool.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResult carries the result back from a tool execution.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// Event is a structured log event streamed over WebSocket to the GUI.
type Event struct {
	Type      string    `json:"type"` // message.in, message.out, tool.call, tool.result, error
	AgentID   string    `json:"agent_id"`
	SessionID string    `json:"session_id"`
	Payload   any       `json:"payload"`
	Timestamp time.Time `json:"timestamp"`

	// Parts carries typed media attachments associated with this event.
	// Nil for events that carry no attachment context.
	Parts []TypedPart `json:"parts,omitempty"`
}
