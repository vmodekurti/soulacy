package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// buildChannelSendBuiltin sends a canonical outbound message through any
// registered channel adapter: telegram, slack, discord, WhatsApp, sidecars, etc.
func (e *Engine) buildChannelSendBuiltin() BuiltinTool {
	return BuiltinTool{
		Name:        "channel.send",
		Gate:        "",
		Description: "Send text to a configured outbound channel adapter. Use for final delivery to Telegram, Slack, Discord, WhatsApp, or a sidecar channel. In interactive runs, channel and to default to the inbound channel/chat. Use text for the message body; message is accepted as a compatibility alias.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel": map[string]any{
					"type":        "string",
					"description": "Registered channel adapter id, such as telegram, slack, discord, whatsapp, or a sidecar id. Optional in interactive channel runs; defaults to the inbound adapter id.",
				},
				"adapter": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for channel. Prefer channel.",
				},
				"to": map[string]any{
					"type":        "string",
					"description": "Destination id for the platform: chat id, channel id, user id, phone number, or sidecar thread id. Optional in interactive channel runs; defaults to the inbound chat/thread id.",
				},
				"destination": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for to.",
				},
				"chat_id": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for to, useful for Telegram chat ids.",
				},
				"channel_id": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for to, useful for Slack/Discord channel ids.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Message body to send",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for text",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for text",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for text",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Optional string metadata passed through to the channel adapter",
				},
			},
			"required": []string{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			channelID := argStringFirst(args, "channel", "adapter", "adapter_id")
			to := argStringFirst(args, "to", "destination", "chat_id", "channel_id", "thread_id", "user_id")
			text := argStringFirst(args, "text", "message", "body", "content")
			if inbound, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok {
				if channelID == "" {
					channelID = strings.TrimSpace(inbound.Channel)
				}
				if to == "" {
					to = strings.TrimSpace(inbound.ThreadID)
				}
			}
			defaultOut, hasDefault := e.channelDefaultOutput(channelID)
			if channelID == "" {
				defaultOut, hasDefault = e.onlyChannelDefaultOutput()
				if hasDefault {
					channelID = strings.TrimSpace(defaultOut.Channel)
				}
			}
			if hasDefault {
				if defaultOut.Channel != "" {
					channelID = strings.TrimSpace(defaultOut.Channel)
				}
				if to == "" {
					to = strings.TrimSpace(defaultOut.To)
				}
			}
			if channelID == "" {
				return "", fmt.Errorf("channel.send: channel is required when there is no inbound channel context and no single default outbound channel is configured")
			}
			if to == "" && channelSendRequiresDestination(channelID) {
				return "", fmt.Errorf("channel.send: to is required when there is no inbound chat/thread context and no default_output_to is configured for channel %q", channelID)
			}
			if text == "" {
				return "", fmt.Errorf("channel.send: text is required")
			}
			if e.channelRegistry == nil {
				return "", fmt.Errorf("channel.send: channel registry is unavailable")
			}
			if _, ok := e.channelRegistry.Statuses()[channelID]; !ok {
				return "", fmt.Errorf("channel.send: channel %q is not registered", channelID)
			}

			meta := map[string]string{
				"tool": "channel.send",
			}
			if rawMeta, ok := args["metadata"].(map[string]any); ok {
				for k, v := range rawMeta {
					ks := strings.TrimSpace(k)
					if ks == "" {
						continue
					}
					meta[ks] = strings.TrimSpace(fmt.Sprint(v))
				}
			}

			out := message.Message{
				ID:        uuid.New().String(),
				SessionID: "channel-send-" + uuid.New().String(),
				AgentID:   "channel.send",
				Channel:   channelID,
				ThreadID:  to,
				UserID:    "agent",
				Username:  "agent",
				Role:      message.RoleAssistant,
				Parts:     message.Text(text),
				Metadata:  meta,
				CreatedAt: time.Now().UTC(),
			}
			if err := e.channelRegistry.Send(ctx, out); err != nil {
				return "", fmt.Errorf("channel.send: send failed through channel %q to %q: %w", channelID, to, err)
			}
			b, _ := json.Marshal(map[string]any{
				"ok":      true,
				"channel": channelID,
				"to":      to,
			})
			return string(b), nil
		},
	}
}

func argStringFirst(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := strings.TrimSpace(argString(args, key)); s != "" {
			return s
		}
	}
	return ""
}

func channelSendRequiresDestination(channelID string) bool {
	switch strings.TrimSpace(channelID) {
	case "webhook":
		return false
	default:
		return true
	}
}

func (e *Engine) channelDefaultOutput(channelID string) (agent.ScheduleOutput, bool) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return agent.ScheduleOutput{}, false
	}
	e.channelDefaultMu.RLock()
	defer e.channelDefaultMu.RUnlock()
	out, ok := e.channelDefaults[channelID]
	if !ok {
		return agent.ScheduleOutput{}, false
	}
	if out.Channel == "" {
		out.Channel = channelID
	}
	return out, true
}

func (e *Engine) onlyChannelDefaultOutput() (agent.ScheduleOutput, bool) {
	e.channelDefaultMu.RLock()
	defer e.channelDefaultMu.RUnlock()
	var out agent.ScheduleOutput
	count := 0
	for channelID, candidate := range e.channelDefaults {
		if strings.TrimSpace(candidate.To) == "" {
			continue
		}
		if candidate.Channel == "" {
			candidate.Channel = channelID
		}
		out = candidate
		count++
		if count > 1 {
			return agent.ScheduleOutput{}, false
		}
	}
	return out, count == 1
}
