package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

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
				"to": map[string]any{
					"type":        "string",
					"description": "Destination id for the platform: chat id, channel id, user id, phone number, or sidecar thread id. Optional in interactive channel runs; defaults to the inbound chat/thread id.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Message body to send",
				},
				"message": map[string]any{
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
			channelID := strings.TrimSpace(argString(args, "channel"))
			to := strings.TrimSpace(argString(args, "to"))
			text := strings.TrimSpace(argString(args, "text"))
			if text == "" {
				text = strings.TrimSpace(argString(args, "message"))
			}
			if inbound, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok {
				if channelID == "" {
					channelID = strings.TrimSpace(inbound.Channel)
				}
				if to == "" {
					to = strings.TrimSpace(inbound.ThreadID)
				}
			}
			if channelID == "" {
				return "", fmt.Errorf("channel.send: channel is required when there is no inbound channel context")
			}
			if to == "" {
				return "", fmt.Errorf("channel.send: to is required when there is no inbound chat/thread context")
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
				return "", fmt.Errorf("channel.send: send failed: %w", err)
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
