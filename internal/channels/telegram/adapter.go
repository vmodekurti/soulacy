// Package telegram provides the Telegram Bot channel adapter.
// It uses long-polling (getUpdates) to receive messages and the sendMessage
// API to reply. No third-party library required — plain HTTP calls.
//
// Configuration in config.yaml:
//   channels:
//     telegram:
//       enabled: true
//       token: "YOUR_BOT_TOKEN"
//       agent_id: "my-agent"   # which agent handles all Telegram messages
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

const apiBase = "https://api.telegram.org/bot"

// Adapter polls the Telegram Bot API.
type Adapter struct {
	token     string
	agentID   string
	client    *http.Client
	inbox     chan<- message.Message
	offset    int64
	connected bool
	stopCh    chan struct{}
	stopOnce  sync.Once // guards close(stopCh) so Stop() is idempotent
}

// New creates a Telegram adapter with the given bot token.
func New(token, agentID string) *Adapter {
	return &Adapter{
		token:   token,
		agentID: agentID,
		client:  &http.Client{Timeout: 35 * time.Second},
		stopCh:  make(chan struct{}),
	}
}

func (a *Adapter) ID() string   { return "telegram" }
func (a *Adapter) Name() string { return "Telegram" }

func (a *Adapter) Start(ctx context.Context, inbox chan<- message.Message) error {
	a.inbox = inbox
	a.connected = true
	go a.poll(ctx)
	return nil
}

func (a *Adapter) poll(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		default:
		}

		updates, err := a.getUpdates(ctx)
		if err != nil {
			log.Printf("telegram: getUpdates error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, u := range updates {
			if u.Message == nil {
				continue
			}
			msg := message.Message{
				ID:        uuid.New().String(),
				SessionID: fmt.Sprintf("telegram-%d", u.Message.Chat.ID),
				AgentID:   a.agentID,
				Channel:   "telegram",
				ThreadID:  strconv.FormatInt(u.Message.Chat.ID, 10),
				UserID:    strconv.FormatInt(u.Message.From.ID, 10),
				Username:  u.Message.From.Username,
				Role:      message.RoleUser,
				Parts:     message.Text(u.Message.Text),
				CreatedAt: time.Now().UTC(),
			}
			// Non-blocking send with cancellation. If the shared inbox is
			// saturated (downstream wedged on a slow LLM call), drop the
			// message and log rather than wedging the poll loop too —
			// otherwise Telegram updates pile up server-side and the
			// adapter stops receiving entirely. (PRODUCTION_AUDIT → HIGH)
			select {
			case a.inbox <- msg:
				a.offset = u.UpdateID + 1
			case <-ctx.Done():
				return
			case <-a.stopCh:
				return
			default:
				log.Printf("telegram: inbox full, dropping message %s", msg.ID)
				a.offset = u.UpdateID + 1
			}
		}
	}
}

func (a *Adapter) Send(_ context.Context, msg message.Message) error {
	if len(msg.Parts) == 0 {
		return nil
	}
	body := map[string]any{
		"chat_id": msg.ThreadID,
		"text":    msg.Parts[0].Text,
	}
	payload, _ := json.Marshal(body)
	resp, err := a.client.Post(
		apiBase+a.token+"/sendMessage",
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("telegram: send: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (a *Adapter) Stop() error {
	a.connected = false
	a.stopOnce.Do(func() { close(a.stopCh) })
	return nil
}

func (a *Adapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: a.connected, Detail: "long-polling"}
}

// --- Telegram API types ---

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *tgMsg   `json:"message"`
}

type tgMsg struct {
	Text string  `json:"text"`
	From tgUser  `json:"from"`
	Chat tgChat  `json:"chat"`
}

type tgUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

func (a *Adapter) getUpdates(ctx context.Context) ([]update, error) {
	url := fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=30", apiBase, a.token, a.offset)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool     `json:"ok"`
		Result []update `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram: API returned ok=false")
	}
	return result.Result, nil
}
