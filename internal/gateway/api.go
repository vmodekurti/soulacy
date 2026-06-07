// api.go — REST API handlers for the Soulacy gateway.
// Every action available in the GUI is backed by one of these handlers.
// The GUI never talks directly to the filesystem — everything goes through this API.
package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/builder"
	"github.com/soulacy/soulacy/internal/channels"
	wawebchan "github.com/soulacy/soulacy/internal/channels/whatsappweb"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/templates"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// handleHealth returns gateway status, version, and a snapshot of dependency
// health. (PRODUCTION_AUDIT → MED/Observability) Each probe is independent
// and capped at a short timeout so a single slow backend can't make the
// health check itself time out — instead we report it as `degraded`.
//
// Status semantics:
//   - "ok"        all probed deps responded successfully
//   - "degraded"  at least one dep failed but the gateway can still serve
//   - "down"      a probe that's load-bearing for ANY work failed (e.g.
//     the SQLite archive). Currently we degrade rather than
//     returning down — operators can decide via the per-dep
//     statuses returned in the body.
func (s *Server) handleHealth(c *fiber.Ctx) error {
	deps := map[string]string{}

	// Provider registry — list IDs so operators can confirm the expected
	// providers loaded. No network call here; this is just an in-memory
	// snapshot.
	deps["providers"] = strings.Join(s.llmRouter.ProviderIDs(), ",")

	// Knowledge store — quick listKBs call. Capped at 50ms.
	if knowledge := s.engine.Knowledge(); knowledge != nil && knowledge.Store != nil {
		done := make(chan error, 1)
		go func() { _, err := knowledge.Store.ListKBs(); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				deps["knowledge"] = "error: " + err.Error()
			} else {
				deps["knowledge"] = "ok"
			}
		case <-time.After(50 * time.Millisecond):
			deps["knowledge"] = "timeout"
		}
	} else {
		deps["knowledge"] = "disabled"
	}

	status := "ok"
	for _, v := range deps {
		if strings.HasPrefix(v, "error:") || v == "timeout" {
			status = "degraded"
			break
		}
	}

	return c.JSON(fiber.Map{
		"status":     status,
		"version":    config.Version,
		"timestamp":  time.Now().UTC(),
		"deps":       deps,
		"request_id": c.Locals("request_id"),
	})
}

func (s *Server) handleRestart(c *fiber.Ctx) error {
	s.log.Warn("gateway restart requested via API", zap.Any("request_id", c.Locals("request_id")))
	if err := startRestartChild(); err != nil {
		s.log.Error("gateway restart failed to spawn child", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "restart failed: " + err.Error(),
		})
	}
	go func() {
		time.Sleep(250 * time.Millisecond)
		os.Exit(0)
	}()
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"ok":      true,
		"message": "Restart requested. A replacement gateway process is starting.",
	})
}

func startRestartChild() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	script := `sleep 0.75; exec "$@"`
	args := append([]string{"-c", script, "soulacy-restart", exe}, os.Args[1:]...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.Env = os.Environ()
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

// --- Agents ---

func isProtectedSystemAgent(id string) bool {
	return strings.TrimSpace(id) == runtime.SystemAgentID
}

func protectedSystemAgentResponse(c *fiber.Ctx) error {
	return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
		"error": "system agent is protected and cannot be modified, deleted, cloned, disabled, or routed to external channels",
	})
}

func channelReferencesProtectedSystem(id string, settings map[string]any, bots []map[string]any) bool {
	if id == "http" {
		return false
	}
	if isProtectedSystemAgent(fmt.Sprint(settings["agent_id"])) {
		return true
	}
	for _, bot := range bots {
		if isProtectedSystemAgent(fmt.Sprint(bot["agent_id"])) {
			return true
		}
	}
	return false
}

func channelMapReferencesProtectedSystem(id string, chMap map[string]any) bool {
	if id == "http" || chMap == nil {
		return false
	}
	if isProtectedSystemAgent(fmt.Sprint(chMap["agent_id"])) {
		return true
	}
	switch raw := chMap["bots"].(type) {
	case []any:
		for _, item := range raw {
			if bot, ok := item.(map[string]any); ok && isProtectedSystemAgent(fmt.Sprint(bot["agent_id"])) {
				return true
			}
		}
	case []map[string]any:
		for _, bot := range raw {
			if isProtectedSystemAgent(fmt.Sprint(bot["agent_id"])) {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleListAgents(c *fiber.Ctx) error {
	defs := s.loader.All()
	return c.JSON(fiber.Map{"agents": defs, "count": len(defs)})
}

func (s *Server) handleGetAgent(c *fiber.Ctx) error {
	def := s.loader.Get(c.Params("id"))
	if def == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "agent not found"})
	}
	return c.JSON(def)
}

func (s *Server) handleValidateAgent(c *fiber.Ctx) error {
	var def agent.Definition
	if err := c.BodyParser(&def); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	report := agentvalidate.Definition(&def, "", s.agentValidationOptions(c.Context()), agentvalidate.Report{})
	return c.JSON(report)
}

func (s *Server) handleCreateAgent(c *fiber.Ctx) error {
	var def agent.Definition
	if err := c.BodyParser(&def); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if def.ID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id is required"})
	}
	if isProtectedSystemAgent(def.ID) {
		return protectedSystemAgentResponse(c)
	}

	// Default LLM to configured provider
	if def.LLM.Provider == "" {
		def.LLM.Provider = s.cfg.LLM.DefaultProvider
	}
	def.Enabled = true

	// Write to first agent dir
	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if err := s.loader.Upsert(dir, &def); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Register with scheduler if applicable
	_ = s.scheduler.RegisterAgent(&def)

	return c.Status(fiber.StatusCreated).JSON(def)
}

func (s *Server) handleUpdateAgent(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	existing := s.loader.Get(id)
	if existing == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "agent not found"})
	}

	var updates agent.Definition
	if err := c.BodyParser(&updates); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	updates.ID = id // ID cannot be changed via update
	updates.SourcePath = existing.SourcePath
	updates.LoadedAt = existing.LoadedAt

	preserveHiddenAgentUpdateFields(&updates, existing)

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if err := s.loader.Upsert(dir, &updates); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Re-register schedule
	s.scheduler.DeregisterAgent(id)
	_ = s.scheduler.RegisterAgent(&updates)

	return c.JSON(updates)
}

// preserveHiddenAgentUpdateFields protects advanced SOUL.yaml fields that are
// not rendered by every GUI edit surface. Without this, a normal Save from a
// partial editor payload can silently wipe security, tool policy, workflow, and
// routing fields that the user configured by hand.
func preserveHiddenAgentUpdateFields(updates, existing *agent.Definition) {
	if updates == nil || existing == nil {
		return
	}
	if updates.Security == nil {
		updates.Security = existing.Security
	}
	if updates.Builtins == nil {
		updates.Builtins = existing.Builtins
	}
	if updates.MCPServers == nil {
		updates.MCPServers = existing.MCPServers
	}
	if updates.MCPTools == nil {
		updates.MCPTools = existing.MCPTools
	}
	if updates.Workflow == nil {
		updates.Workflow = existing.Workflow
	}
	if updates.NotifyOnFailure == nil {
		updates.NotifyOnFailure = existing.NotifyOnFailure
	}
	if updates.RunTimeout == "" {
		updates.RunTimeout = existing.RunTimeout
	}
	if !updates.SystemTools {
		updates.SystemTools = existing.SystemTools
	}
	if len(updates.ConfirmTools) == 0 {
		updates.ConfirmTools = existing.ConfirmTools
	}
	if len(updates.Labels) == 0 {
		updates.Labels = existing.Labels
	}
	if len(updates.Agents) == 0 {
		updates.Agents = existing.Agents
	}
	if len(updates.Knowledge) == 0 {
		updates.Knowledge = existing.Knowledge
	}
	if len(updates.Tags) == 0 {
		updates.Tags = existing.Tags
	}
}

func (s *Server) handleDeleteAgent(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	s.scheduler.DeregisterAgent(id)
	if err := s.loader.Delete(id); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) handleEnableAgent(c *fiber.Ctx) error {
	return s.setAgentEnabled(c, true)
}

func (s *Server) handleDisableAgent(c *fiber.Ctx) error {
	return s.setAgentEnabled(c, false)
}

func (s *Server) setAgentEnabled(c *fiber.Ctx, enabled bool) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	def := s.loader.Get(id)
	if def == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "agent not found"})
	}
	def.Enabled = enabled
	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	_ = s.loader.Upsert(dir, def)
	if enabled {
		_ = s.scheduler.RegisterAgent(def)
	} else {
		s.scheduler.DeregisterAgent(id)
	}
	return c.JSON(fiber.Map{"id": id, "enabled": enabled})
}

// handleCloneAgent duplicates an existing agent under a new, unique ID.
// The clone is created disabled so duplicate schedules don't fire unexpectedly.
func (s *Server) handleCloneAgent(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	src := s.loader.Get(id)
	if src == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "agent not found"})
	}

	// Value copy, then deep-copy the Schedule pointer so the clone and original
	// don't share a *Schedule (editing one would otherwise affect the other).
	clone := *src
	if src.Schedule != nil {
		sc := *src.Schedule
		clone.Schedule = &sc
	}
	clone.ID = s.uniqueAgentID(id + "-copy")
	if clone.Name != "" {
		clone.Name = clone.Name + " (copy)"
	}
	clone.Enabled = false
	clone.SourcePath = "" // force a fresh file in its own folder

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if err := s.loader.Upsert(dir, &clone); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(clone)
}

// uniqueAgentID returns base if free, otherwise base-2, base-3, …
func (s *Server) uniqueAgentID(base string) string {
	if s.loader.Get(base) == nil {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if s.loader.Get(candidate) == nil {
			return candidate
		}
	}
}

// --- Chat ---

// handleChat is the synchronous HTTP channel: POST a message, get a reply.
// This is what the GUI's built-in chat tester and `sy chat` use.
// We call engine.Handle() directly so we never touch the async inbox path.
func (s *Server) handleChat(c *fiber.Ctx) error {
	var req struct {
		AgentID   string `json:"agent_id"`
		SessionID string `json:"session_id"`
		UserID    string `json:"user_id"`
		Username  string `json:"username"`
		Text      string `json:"text"`
		Overrides struct {
			Provider    string   `json:"provider"`
			Model       string   `json:"model"`
			Temperature *float64 `json:"temperature"`
			MaxTokens   *int     `json:"max_tokens"`
			MaxTurns    *int     `json:"max_turns"`
			ToolChoice  string   `json:"tool_choice"`
		} `json:"overrides"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if req.AgentID == "" || req.Text == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "agent_id and text are required"})
	}
	if req.UserID == "" {
		req.UserID = "api-user"
	}
	if req.Username == "" {
		req.Username = req.UserID
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = fmt.Sprintf("http-%s", req.UserID)
	}
	def := s.loader.Get(req.AgentID)

	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		AgentID:   req.AgentID,
		Channel:   "http",
		ThreadID:  req.UserID,
		UserID:    req.UserID,
		Username:  req.Username,
		Role:      message.RoleUser,
		Parts:     message.Text(req.Text),
		Metadata:  chatOverrideMetadata(req.Overrides.Provider, req.Overrides.Model, req.Overrides.Temperature, req.Overrides.MaxTokens, req.Overrides.MaxTurns, req.Overrides.ToolChoice),
		CreatedAt: time.Now().UTC(),
	}

	// Decouple client connection drop from background execution. Use the
	// agent's declared run_timeout if set, otherwise the gateway default.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), resolveRunTimeout(def))
	defer cancel()

	reply, err := s.engine.Handle(ctx, msg)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	return c.JSON(fiber.Map{"reply": replyText})
}

func chatOverrideMetadata(provider, model string, temperature *float64, maxTokens, maxTurns *int, toolChoice string) map[string]string {
	meta := map[string]string{}
	if provider = strings.TrimSpace(provider); provider != "" {
		meta["playground.llm.provider"] = provider
	}
	if model = strings.TrimSpace(model); model != "" {
		meta["playground.llm.model"] = model
	}
	if temperature != nil {
		meta["playground.llm.temperature"] = fmt.Sprintf("%g", *temperature)
	}
	if maxTokens != nil && *maxTokens > 0 {
		meta["playground.llm.max_tokens"] = fmt.Sprintf("%d", *maxTokens)
	}
	if maxTurns != nil && *maxTurns > 0 {
		meta["playground.max_turns"] = fmt.Sprintf("%d", *maxTurns)
	}
	if toolChoice = strings.TrimSpace(toolChoice); toolChoice != "" {
		meta["playground.llm.tool_choice"] = toolChoice
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// handleChatStream is the SSE (Server-Sent Events) variant of handleChat.
// The response is a text/event-stream where each token from the LLM is sent
// as a `data: <token>\n\n` frame. The stream ends with `data: [DONE]\n\n`.
// If the agent has no streaming-capable LLM reply (tool calls are present),
// it falls back to a single data frame containing the full reply text.
//
// Client usage:
//
//	const es = new EventSource('/api/v1/chat/stream?agent_id=X&text=Hello');
//	es.onmessage = e => { if(e.data==='[DONE]') es.close(); else buf+=e.data; };
//
// The endpoint also accepts POST with the same JSON body as /chat. A GET form
// is also supported for EventSource compatibility (query params mapped to fields).
func (s *Server) handleChatStream(c *fiber.Ctx) error {
	// Accept both POST (JSON body) and GET (query params) for EventSource compat.
	var req struct {
		AgentID  string `json:"agent_id"`
		UserID   string `json:"user_id"`
		Username string `json:"username"`
		Text     string `json:"text"`
	}
	if c.Method() == "POST" {
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
	} else {
		req.AgentID = c.Query("agent_id")
		req.UserID = c.Query("user_id")
		req.Username = c.Query("username")
		req.Text = c.Query("text")
	}
	if req.AgentID == "" || req.Text == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "agent_id and text are required"})
	}
	if req.UserID == "" {
		req.UserID = "api-user"
	}
	if req.Username == "" {
		req.Username = req.UserID
	}
	def := s.loader.Get(req.AgentID)

	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: fmt.Sprintf("http-%s", req.UserID),
		AgentID:   req.AgentID,
		Channel:   "http",
		ThreadID:  req.UserID,
		UserID:    req.UserID,
		Username:  req.Username,
		Role:      message.RoleUser,
		Parts:     message.Text(req.Text),
		CreatedAt: time.Now().UTC(),
	}

	// Decouple the client connection lifetime from background execution.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), resolveRunTimeout(def))
	defer cancel()

	// sseEvent is the unified event type for the SSE stream.
	// Event == "" → default "message" frame (token).
	// Event != "" → named SSE event frame (tool_confirm, error).
	type sseEvent struct {
		Event string // SSE event name (omit for default "message")
		Data  string // SSE data payload
	}

	// Single channel for all SSE events: tokens, confirms, errors, done.
	// Buffered so the engine goroutine doesn't block on slow writers.
	events := make(chan sseEvent, 256)

	// Stream callback delivers LLM tokens as default SSE message events.
	streamCtx := runtime.WithStreamCallback(runCtx, func(token string) {
		select {
		case events <- sseEvent{Data: strings.ReplaceAll(token, "\n", "\\n")}:
		case <-runCtx.Done():
		}
	})

	// Confirm sender emits a tool_confirm event and registers a result channel
	// in the broker. The engine blocks on the result channel until the user
	// approves or denies via POST /api/v1/chat/confirm.
	streamCtx = runtime.WithConfirmSender(streamCtx, func(req runtime.ConfirmRequest) <-chan bool {
		resultCh := s.engine.Broker().Register(req.CallID)
		data, _ := json.Marshal(req)
		select {
		case events <- sseEvent{Event: "tool_confirm", Data: string(data)}:
		case <-runCtx.Done():
		}
		return resultCh
	})

	go func() {
		defer close(events)
		_, err := s.engine.Handle(streamCtx, msg)
		if err != nil {
			select {
			case events <- sseEvent{Event: "error", Data: err.Error()}:
			case <-runCtx.Done():
			}
		} else {
			select {
			case events <- sseEvent{Data: "[DONE]"}:
			case <-runCtx.Done():
			}
		}
	}()

	// Switch to SSE mode. SetBodyStreamWriter takes ownership of the connection.
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no") // disable nginx/proxy buffering

	c.Context().SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		for ev := range events {
			if ev.Event != "" {
				fmt.Fprintf(w, "event: %s\n", ev.Event) //nolint:errcheck
			}
			fmt.Fprintf(w, "data: %s\n\n", ev.Data) //nolint:errcheck
			w.Flush()                               //nolint:errcheck
		}
	}))

	return nil
}

// handleToolConfirm resolves a pending tool-confirmation request.
// The client POSTs here after the user approves or denies a "tool_confirm" SSE event.
//
//	POST /api/v1/chat/confirm
//	{"call_id": "<id>", "approved": true}
func (s *Server) handleToolConfirm(c *fiber.Ctx) error {
	var req struct {
		CallID   string `json:"call_id"`
		Approved bool   `json:"approved"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if req.CallID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "call_id is required"})
	}
	if !s.engine.Broker().Resolve(req.CallID, req.Approved) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "call_id not found — it may have already timed out or been resolved",
		})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// --- Channels ---

// channelField describes one editable setting on a channel.
type channelField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"` // "text" or "password"
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	Help     string `json:"help,omitempty"`
}

// channelSpec is the static definition of a supported channel type.
type channelSpec struct {
	ID     string
	Name   string
	Always bool // always-on (e.g. http) — cannot be disabled
	Fields []channelField
}

// channelSpecs is the catalog of every channel Soulacy supports. The GUI uses
// this to render configuration forms even for channels not yet configured.
var channelSpecs = []channelSpec{
	{ID: "http", Name: "HTTP", Always: true, Fields: nil},
	{ID: "telegram", Name: "Telegram", Fields: []channelField{
		{Key: "bot_name", Label: "Bot name", Type: "text", Required: false, Help: "Friendly name shown in mappings and schedules"},
		{Key: "token", Label: "Bot token", Type: "password", Required: true, Secret: true, Help: "Get one from @BotFather"},
		{Key: "agent_id", Label: "Default agent ID", Type: "text", Required: true},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "ignore_groups", Label: "Ignore group chats", Type: "text", Required: false, Help: "true by default; set false only for deliberate group usage"},
		{Key: "allowed_chat_ids", Label: "Allowed chat IDs", Type: "text", Required: false, Help: "Optional comma-separated Telegram chat IDs to allow"},
		{Key: "allowed_user_ids", Label: "Allowed user IDs", Type: "text", Required: false, Help: "Comma-separated Telegram user IDs"},
	}},
	{ID: "discord", Name: "Discord", Fields: []channelField{
		{Key: "bot_name", Label: "Bot name", Type: "text", Required: false, Help: "Friendly name shown in mappings and schedules"},
		{Key: "token", Label: "Bot token", Type: "password", Required: true, Secret: true},
		{Key: "agent_id", Label: "Default agent ID", Type: "text", Required: true},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "ignore_groups", Label: "Ignore servers", Type: "text", Required: false, Help: "true by default; set false only for deliberate server usage"},
		{Key: "allowed_chat_ids", Label: "Allowed channel IDs", Type: "text", Required: false, Help: "Optional comma-separated Discord channel IDs to allow"},
		{Key: "allowed_user_ids", Label: "Allowed user IDs", Type: "text", Required: false, Help: "Optional comma-separated Discord user IDs to allow"},
		{Key: "guild_id", Label: "Guild ID", Type: "text", Required: false},
	}},
	{ID: "slack", Name: "Slack", Fields: []channelField{
		{Key: "bot_name", Label: "Bot name", Type: "text", Required: false, Help: "Friendly name shown in mappings and schedules"},
		{Key: "bot_token", Label: "Bot token", Type: "password", Required: true, Secret: true, Help: "xoxb-..."},
		{Key: "app_token", Label: "App token", Type: "password", Required: true, Secret: true, Help: "xapp-..."},
		{Key: "agent_id", Label: "Default agent ID", Type: "text", Required: true},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "ignore_groups", Label: "Ignore channels", Type: "text", Required: false, Help: "true by default; set false only for deliberate channel usage"},
		{Key: "allowed_chat_ids", Label: "Allowed channel IDs", Type: "text", Required: false, Help: "Optional comma-separated Slack channel IDs to allow"},
		{Key: "allowed_user_ids", Label: "Allowed user IDs", Type: "text", Required: false, Help: "Optional comma-separated Slack user IDs to allow"},
	}},
	{ID: "whatsapp", Name: "WhatsApp", Fields: []channelField{
		{Key: "bot_name", Label: "Bot name", Type: "text", Required: false, Help: "Friendly name shown in mappings and schedules"},
		{Key: "phone_number_id", Label: "Phone number ID", Type: "text", Required: true},
		{Key: "access_token", Label: "Access token", Type: "password", Required: true, Secret: true},
		{Key: "verify_token", Label: "Verify token", Type: "password", Required: true, Secret: true},
		{Key: "app_secret", Label: "App secret", Type: "password", Required: true, Secret: true, Help: "Meta app secret used to verify webhook signatures"},
		{Key: "agent_id", Label: "Default agent ID", Type: "text", Required: true},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "allowed_user_ids", Label: "Allowed phone numbers", Type: "text", Required: false, Help: "Optional comma-separated WhatsApp sender IDs to allow"},
	}},
	{ID: "whatsapp_web", Name: "WhatsApp Web (experimental)", Fields: []channelField{
		{Key: "bot_name", Label: "Bot name", Type: "text", Required: false, Help: "Friendly name shown in mappings and schedules"},
		{Key: "trigger_phrase", Label: "Trigger phrase", Type: "text", Required: false, Help: "Only messages beginning with this phrase will trigger the agent; defaults to !soulacy"},
		{Key: "ignore_groups", Label: "Ignore group chats", Type: "text", Required: false, Help: "true by default; set false only for deliberate group usage"},
		{Key: "allowed_chat_ids", Label: "Allowed chat IDs", Type: "text", Required: false, Help: "Optional comma-separated WhatsApp chat JIDs to allow"},
		{Key: "allowed_sender_ids", Label: "Allowed sender IDs", Type: "text", Required: false, Help: "Optional comma-separated WhatsApp sender JIDs to allow"},
		{Key: "command", Label: "Command", Type: "text", Required: false, Help: "Runtime executable; defaults to node"},
		{Key: "args", Label: "Arguments", Type: "text", Required: true, Help: "Command args, e.g. scripts/whatsapp-web-sidecar.mjs"},
		{Key: "session_dir", Label: "Session directory", Type: "text", Required: false, Help: "Where QR-linked auth state is stored"},
		{Key: "account_id", Label: "Account ID", Type: "text", Required: false, Help: "Session subdirectory for this linked account"},
		{Key: "agent_id", Label: "Default agent ID", Type: "text", Required: true},
	}},
}

func channelSpecByID(id string) *channelSpec {
	for i := range channelSpecs {
		if channelSpecs[i].ID == id {
			return &channelSpecs[i]
		}
	}
	return nil
}

func channelSupportsBots(id string) bool {
	switch id {
	case "telegram", "discord", "slack":
		return true
	default:
		return false
	}
}

func valuePresent(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case []any:
		return len(t) > 0
	case []int64:
		return len(t) > 0
	case []int:
		return len(t) > 0
	default:
		return true
	}
}

func displayChannelValue(v any) any {
	switch t := v.(type) {
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, fmt.Sprint(item))
		}
		return strings.Join(parts, ", ")
	case []int64:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, strconv.FormatInt(item, 10))
		}
		return strings.Join(parts, ", ")
	case []int:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, strconv.Itoa(item))
		}
		return strings.Join(parts, ", ")
	case []string:
		return strings.Join(t, " ")
	default:
		return t
	}
}

func normalizeChannelValue(key, val string) any {
	if key == "args" {
		return strings.Fields(val)
	}
	if key != "allowed_user_ids" {
		return val
	}
	parts := strings.FieldsFunc(val, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			if n, err := strconv.ParseInt(part, 10, 64); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func normalizeChannelBots(spec channelSpec, bots []map[string]any, existingRaw any) []map[string]any {
	existing := rawBotList(existingRaw)
	out := make([]map[string]any, 0, len(bots))
	for i, bot := range bots {
		row := map[string]any{}
		var existingBot map[string]any
		if i < len(existing) {
			existingBot = existing[i]
		}
		for _, f := range spec.Fields {
			raw, present := bot[f.Key]
			if !present {
				continue
			}
			val := strings.TrimSpace(fmt.Sprint(raw))
			if f.Secret && (val == "" || val == "***") {
				if existingBot != nil && valuePresent(existingBot[f.Key]) {
					row[f.Key] = existingBot[f.Key]
				}
				continue
			}
			row[f.Key] = normalizeChannelValue(f.Key, val)
		}
		out = append(out, row)
	}
	return out
}

func maskChannelBots(spec channelSpec, raw any, statuses map[string]channels.AdapterStatus) []fiber.Map {
	botList := rawBotList(raw)
	out := make([]fiber.Map, 0, len(botList))
	for i, bot := range botList {
		row := fiber.Map{}
		for _, f := range spec.Fields {
			rawVal := bot[f.Key]
			if f.Secret && valuePresent(rawVal) {
				row[f.Key] = "***"
			} else {
				row[f.Key] = displayChannelValue(rawVal)
			}
		}
		agentID, _ := bot["agent_id"].(string)
		adapterID := channelAdapterID(spec.ID, agentID, i)
		st := statuses[adapterID]
		row["_adapter_id"] = adapterID
		row["_connected"] = st.Connected
		row["_detail"] = st.Detail
		out = append(out, row)
	}
	return out
}

func rawBotList(raw any) []map[string]any {
	switch list := raw.(type) {
	case []map[string]any:
		return list
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func channelAdapterID(channelID, agentID string, index int) string {
	if index == 0 {
		return channelID
	}
	if agentID == "" {
		return channelID + "-" + strconv.Itoa(index+1)
	}
	return channelID + "-" + sanitizeChannelID(agentID)
}

func sanitizeChannelID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// handleListChannels returns every supported channel merged with its current
// config (secrets masked) and live connection status. This is an ARRAY so the
// GUI can iterate it directly.
func (s *Server) handleListChannels(c *fiber.Ctx) error {
	statuses := s.channels.Statuses()

	out := make([]fiber.Map, 0, len(channelSpecs))
	for _, spec := range channelSpecs {
		cfg := s.cfg.Channels[spec.ID] // may be nil

		enabled := spec.Always
		if v, ok := cfg["enabled"].(bool); ok {
			enabled = v
		}

		// Build masked settings view + flag whether anything is configured
		settings := make(fiber.Map, len(spec.Fields))
		configured := false
		for _, f := range spec.Fields {
			raw := cfg[f.Key]
			if valuePresent(raw) {
				configured = true
			}
			if f.Secret && valuePresent(raw) {
				settings[f.Key] = "***"
			} else {
				settings[f.Key] = displayChannelValue(raw)
			}
		}
		bots := maskChannelBots(spec, cfg["bots"], statuses)
		if len(bots) > 0 {
			configured = true
		}

		st, registered := statuses[spec.ID]

		out = append(out, fiber.Map{
			"id":         spec.ID,
			"name":       spec.Name,
			"always":     spec.Always,
			"enabled":    enabled,
			"configured": configured,
			"registered": registered,
			"schema":     spec.Fields,
			"bot_schema": spec.Fields,
			"multi_bot":  channelSupportsBots(spec.ID),
			"bots":       bots,
			"settings":   settings,
			"status": fiber.Map{
				"connected": st.Connected,
				"detail":    st.Detail,
				"qr_code":   st.QRCode,
			},
		})
	}
	return c.JSON(fiber.Map{"channels": out})
}

// handleUpdateChannel merges channel settings (and optional enabled flag) into
// config.yaml. Secret fields sent as "***" or empty are preserved (not clobbered).
func (s *Server) handleUpdateChannel(c *fiber.Ctx) error {
	id := c.Params("id")
	spec := channelSpecByID(id)
	if spec == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "unknown channel: " + id})
	}
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config file path unknown — cannot persist channel settings"})
	}

	var req struct {
		Enabled  *bool            `json:"enabled"`
		Settings map[string]any   `json:"settings"`
		Bots     []map[string]any `json:"bots"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if channelReferencesProtectedSystem(id, req.Settings, req.Bots) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "system agent is web-only and cannot be assigned to external channels",
		})
	}

	// Existing in-memory settings for this channel (source of truth for secrets we keep).
	existing := s.cfg.Channels[id]

	// Apply to on-disk config.
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "read config: " + err.Error()})
	}
	channelsMap := getOrCreateMap(raw, "channels")
	chMap := getOrCreateMap(channelsMap, id)

	if req.Enabled != nil {
		chMap["enabled"] = *req.Enabled
	}
	for _, f := range spec.Fields {
		rawVal, present := req.Settings[f.Key]
		if !present {
			continue
		}
		var val string
		switch v := rawVal.(type) {
		case bool:
			val = strconv.FormatBool(v)
		case string:
			val = v
		case nil:
			val = ""
		default:
			val = fmt.Sprint(v)
		}
		// Preserve a secret when the client sends back the mask or blank.
		if f.Secret && (val == "" || val == "***") {
			if prev, ok := existing[f.Key].(string); ok && prev != "" {
				chMap[f.Key] = prev
			}
			continue
		}
		chMap[f.Key] = normalizeChannelValue(f.Key, val)
	}
	if req.Bots != nil {
		if len(req.Bots) == 0 {
			delete(chMap, "bots")
		} else if !channelSupportsBots(id) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": id + " does not support multiple bot mappings"})
		} else {
			chMap["bots"] = normalizeChannelBots(*spec, req.Bots, existing["bots"])
		}
	}
	if channelMapReferencesProtectedSystem(id, chMap) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "system agent is web-only and cannot be assigned to external channels",
		})
	}

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "write config: " + err.Error()})
	}

	// Mirror into the live in-memory config so the list reflects changes pre-restart.
	s.applyChannelToMemory(id, chMap)

	s.log.Info("channel settings updated via API", zap.String("channel", id))
	return c.JSON(fiber.Map{
		"ok":      true,
		"message": "Channel saved. Restart the gateway to connect/disconnect adapters.",
	})
}

func (s *Server) handleStartWhatsAppWebPairing(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config file path unknown — cannot persist WhatsApp Web settings"})
	}
	var req struct {
		AgentID          string `json:"agent_id"`
		TriggerPhrase    string `json:"trigger_phrase"`
		IgnoreGroups     *bool  `json:"ignore_groups"`
		AllowedChatIDs   string `json:"allowed_chat_ids"`
		AllowedSenderIDs string `json:"allowed_sender_ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "agent_id is required"})
	}
	if isProtectedSystemAgent(agentID) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "system agent is web-only and cannot be assigned to WhatsApp Web",
		})
	}
	if s.loader.Get(agentID) == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "unknown agent: " + agentID})
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "read config: " + err.Error()})
	}
	chMap := getOrCreateMap(getOrCreateMap(raw, "channels"), "whatsapp_web")
	chMap["enabled"] = true
	chMap["agent_id"] = agentID
	if strings.TrimSpace(fmt.Sprint(chMap["command"])) == "" {
		chMap["command"] = "node"
	}
	if len(parseStringListValue(chMap["args"])) == 0 {
		chMap["args"] = []string{"scripts/whatsapp-web-sidecar.mjs"}
	}
	if strings.TrimSpace(fmt.Sprint(chMap["session_dir"])) == "" {
		base := filepath.Dir(s.cfg.Memory.Dir)
		if base == "." || base == "" {
			base = filepath.Dir(s.cfgPath)
		}
		chMap["session_dir"] = filepath.Join(base, "whatsapp-web")
	}
	if strings.TrimSpace(fmt.Sprint(chMap["account_id"])) == "" {
		chMap["account_id"] = "default"
	}
	if strings.TrimSpace(fmt.Sprint(chMap["bot_name"])) == "" {
		chMap["bot_name"] = "WhatsApp Web"
	}
	if strings.TrimSpace(req.TriggerPhrase) != "" {
		chMap["trigger_phrase"] = strings.TrimSpace(req.TriggerPhrase)
	} else if strings.TrimSpace(fmt.Sprint(chMap["trigger_phrase"])) == "" {
		chMap["trigger_phrase"] = "!soulacy"
	}
	if req.IgnoreGroups != nil {
		chMap["ignore_groups"] = *req.IgnoreGroups
	} else if _, ok := chMap["ignore_groups"]; !ok {
		chMap["ignore_groups"] = true
	}
	if strings.TrimSpace(req.AllowedChatIDs) != "" {
		chMap["allowed_chat_ids"] = strings.TrimSpace(req.AllowedChatIDs)
	}
	if strings.TrimSpace(req.AllowedSenderIDs) != "" {
		chMap["allowed_sender_ids"] = strings.TrimSpace(req.AllowedSenderIDs)
	}

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "write config: " + err.Error()})
	}
	s.applyChannelToMemory("whatsapp_web", chMap)

	command, _ := chMap["command"].(string)
	args := parseStringListValue(chMap["args"])
	sessionDir, _ := chMap["session_dir"].(string)
	accountID, _ := chMap["account_id"].(string)
	activation := channels.ActivationPolicy{
		TriggerPhrase:    strings.TrimSpace(fmt.Sprint(chMap["trigger_phrase"])),
		IgnoreGroups:     parseBoolValue(chMap["ignore_groups"], true),
		AllowedThreadIDs: parseDelimitedStringList(chMap["allowed_chat_ids"]),
		AllowedUserIDs:   parseDelimitedStringList(chMap["allowed_sender_ids"]),
	}
	adapter := wawebchan.New("whatsapp_web", command, args, sessionDir, agentID, accountID, activation, s.log)
	if err := s.channels.StartAdapter(context.Background(), adapter); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "start WhatsApp Web sidecar: " + err.Error()})
	}

	return c.JSON(fiber.Map{
		"ok":      true,
		"message": "WhatsApp Web pairing started. Scan the QR when it appears.",
	})
}

func parseStringListValue(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(v)
	default:
		return nil
	}
}

func parseDelimitedStringList(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return parseStringListValue(v)
	case []any:
		return parseStringListValue(v)
	case string:
		parts := strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\t'
		})
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}

func parseBoolValue(raw any, fallback bool) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1", "on":
			return true
		case "false", "no", "0", "off":
			return false
		}
	}
	return fallback
}

// applyChannelToMemory copies a freshly-written channel config block into the
// live config so GET /channels reflects it without a restart.
func (s *Server) applyChannelToMemory(id string, chMap map[string]any) {
	if s.cfg.Channels == nil {
		s.cfg.Channels = map[string]map[string]any{}
	}
	merged := map[string]any{}
	for k, v := range chMap {
		merged[k] = v
	}
	s.cfg.Channels[id] = merged
}

func (s *Server) setChannelEnabled(c *fiber.Ctx, enabled bool) error {
	id := c.Params("id")
	spec := channelSpecByID(id)
	if spec == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "unknown channel: " + id})
	}
	if spec.Always {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": id + " is always enabled"})
	}
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config file path unknown — cannot persist"})
	}
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	chMap := getOrCreateMap(getOrCreateMap(raw, "channels"), id)
	chMap["enabled"] = enabled
	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.applyChannelToMemory(id, chMap)
	return c.JSON(fiber.Map{
		"ok":      true,
		"id":      id,
		"enabled": enabled,
		"message": "Saved. Restart the gateway to apply.",
	})
}

func (s *Server) handleEnableChannel(c *fiber.Ctx) error  { return s.setChannelEnabled(c, true) }
func (s *Server) handleDisableChannel(c *fiber.Ctx) error { return s.setChannelEnabled(c, false) }

// --- Schedule ---

func (s *Server) handleListSchedule(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"schedule": s.scheduler.Entries()})
}

func (s *Server) handleManualTrigger(c *fiber.Ctx) error {
	id := c.Params("id")
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	def := s.loader.Get(id)
	if def == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "agent not found"})
	}

	// Block concurrent runs: if this agent is already executing (manual or
	// scheduled), refuse rather than running it twice.
	if !s.scheduler.TryStartRun(id) {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "agent is already running"})
	}
	defer s.scheduler.FinishRun(id)

	sessionID := fmt.Sprintf("manual-%s", id)
	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		AgentID:   id,
		Channel:   "http",
		ThreadID:  "manual-trigger",
		UserID:    "manual-trigger",
		Username:  "manual-trigger",
		Role:      message.RoleUser,
		Parts:     message.Text("__trigger:manual__"),
		CreatedAt: time.Now().UTC(),
	}

	runStart := time.Now()
	s.log.Info("manual trigger started",
		zap.String("agent", id),
		zap.String("session", sessionID),
		zap.String("llm_provider", def.LLM.Provider),
		zap.String("llm_model", def.LLM.Model),
	)

	// Decouple client connection drop from background execution. Use the
	// agent's run_timeout — long-running tools (e.g. NotebookLM audio gen)
	// would blow the old 120s ceiling.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), resolveRunTimeout(def))
	defer cancel()

	reply, err := s.engine.Handle(ctx, msg)
	elapsed := time.Since(runStart).Round(time.Millisecond)
	if err != nil {
		s.log.Error("manual trigger failed",
			zap.String("agent", id),
			zap.String("session", sessionID),
			zap.Duration("elapsed", elapsed),
			zap.Error(err),
		)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	s.log.Info("manual trigger completed",
		zap.String("agent", id),
		zap.String("session", sessionID),
		zap.Duration("elapsed", elapsed),
		zap.Int("reply_len", len(replyText)),
		zap.String("reply_preview", func() string {
			if len(replyText) > 120 {
				return replyText[:120] + "…"
			}
			return replyText
		}()),
	)
	return c.JSON(fiber.Map{"result": replyText})
}

// resolveRunTimeout returns the max wall-clock duration for one agent run.
// Honors agent.RunTimeout (Go duration string) when set, else falls back to
// the gateway default (15 minutes — enough for chains involving slow tools
// like NotebookLM audio generation).
func resolveRunTimeout(def *agent.Definition) time.Duration {
	return def.ResolvedRunTimeout(15 * time.Minute)
}

// handleScheduleStatus returns live run state for polling by the GUI:
//   - running: agentID → ISO start time for agents currently executing
//   - next:    agentID → ISO next scheduled fire time (enabled cron agents)
func (s *Server) handleScheduleStatus(c *fiber.Ctx) error {
	running := s.scheduler.RunningSnapshot()
	runningOut := make(fiber.Map, len(running))
	for id, t := range running {
		runningOut[id] = t.UTC()
	}

	next := fiber.Map{}
	for _, e := range s.scheduler.Entries() {
		if !e.Next.IsZero() {
			next[e.AgentID] = e.Next.UTC()
		}
	}
	return c.JSON(fiber.Map{"running": runningOut, "next": next})
}

// handleToolCatalog returns every tool an agent could be wired to:
//   - python_tools: every *.py in ~/.soulacy/tools/ (the convention) and in
//     <agent_dir>/tools/ for each configured agent dir.
//   - mcp_tools:    every tool exposed by a currently-connected MCP server,
//     with the full namespaced name (mcp__<server>__<tool>).
//   - builtins:     Go-native tools shipped with the engine (web_search,
//     read_skill, etc.).
//
// Used by the Agents Edit page's python_file picker and by the Builder so the
// LLM picks real tools instead of inventing names.
//
// PRODUCTION_AUDIT → HIGH/Caching: this used to do dozens of stat/open/read
// syscalls per request (GUI polls, Builder calls every turn). Now wrapped in
// a 30s TTL cache + extractPythonDocstring memoises on (path, mtime). The
// file watcher additionally calls InvalidateToolCatalog() when it sees a
// *.py change so freshly-added tools surface immediately instead of waiting
// up to 30s. MCP tools refresh on every call because the MCP client tracks
// connection state separately and we want that to be live.
func (s *Server) handleToolCatalog(c *fiber.Ctx) error {
	catalog := s.toolCatalog()
	return c.JSON(catalog)
}

// toolCatalogPayload is the shape returned by /tool-catalog. Defined as a
// named type so we can cache fully-rendered responses without re-marshalling.
type toolCatalogPayload struct {
	PythonTools []pyToolView      `json:"python_tools"`
	MCPTools    []mcpToolView     `json:"mcp_tools"`
	Builtins    []builtinToolView `json:"builtins"`
}
type pyToolView struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description"`
}
type mcpToolView struct {
	FullName    string `json:"full_name"`
	Name        string `json:"name"`
	Server      string `json:"server"`
	Description string `json:"description"`
}
type builtinToolView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

const toolCatalogTTL = 30 * time.Second

// toolCatalog returns a fresh-enough catalog. The Python-tools portion is
// memoised behind a TTL; MCP and built-ins are recomputed because they're
// cheap (in-memory snapshots) and we want them live.
func (s *Server) toolCatalog() toolCatalogPayload {
	// Hot-path: serve the cached Python list if still fresh.
	s.toolCatalogMu.Lock()
	cached := s.toolCatalogCache
	cachedAt := s.toolCatalogAt
	s.toolCatalogMu.Unlock()

	var pys []pyToolView
	if cached != nil && time.Since(cachedAt) < toolCatalogTTL {
		pys = cached
	} else {
		pys = s.scanPythonTools()
		s.toolCatalogMu.Lock()
		s.toolCatalogCache = pys
		s.toolCatalogAt = time.Now()
		s.toolCatalogMu.Unlock()
	}

	mcps := s.snapshotMCPTools()
	builtins := s.snapshotBuiltins()
	return toolCatalogPayload{PythonTools: pys, MCPTools: mcps, Builtins: builtins}
}

// InvalidateToolCatalog drops the cache so the next call rescans. Called by
// the file watcher when it sees a *.py change under any tool dir.
func (s *Server) InvalidateToolCatalog() {
	s.toolCatalogMu.Lock()
	s.toolCatalogCache = nil
	s.toolCatalogMu.Unlock()
}

// PythonToolDirs returns the on-disk directories the catalog watches for
// .py files. Used by the file watcher to know which paths to monitor.
func (s *Server) PythonToolDirs() []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		if wsPaths, werr := config.ResolveWorkspace(); werr == nil {
			dirs = append(dirs, wsPaths.Tools)
		} else {
			dirs = append(dirs, filepath.Join(home, ".soulacy", "tools"))
		}
	}
	for _, ad := range s.cfg.AgentDirs {
		dirs = append(dirs, filepath.Join(ad, "tools"))
	}
	return dirs
}

func (s *Server) scanPythonTools() []pyToolView {
	var pys []pyToolView
	seen := map[string]bool{}
	scanDir := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if seen[full] {
				continue
			}
			seen[full] = true
			name := strings.TrimSuffix(e.Name(), ".py")
			pys = append(pys, pyToolView{
				Name: name, Path: full,
				Description: extractPythonDocstring(full),
			})
		}
	}
	for _, d := range s.PythonToolDirs() {
		scanDir(d)
	}
	return pys
}

func (s *Server) snapshotMCPTools() []mcpToolView {
	var mcps []mcpToolView
	if s.mcp == nil {
		return mcps
	}
	for _, srv := range s.mcp.ServersSnapshot() {
		if !srv.Connected {
			continue
		}
		for _, t := range srv.Tools {
			mcps = append(mcps, mcpToolView{
				FullName: t.FullName, Name: t.Name,
				Server: srv.ID, Description: t.Description,
			})
		}
	}
	return mcps
}

func (s *Server) snapshotBuiltins() []builtinToolView {
	var builtins []builtinToolView
	if s.engine == nil {
		return builtins
	}
	for _, b := range s.engine.Builtins() {
		builtins = append(builtins, builtinToolView{
			Name: b.Name, Description: b.Description,
		})
	}
	return builtins
}

// docstringCache memoises extractPythonDocstring results by (path, mtime).
// PRODUCTION_AUDIT → LOW/Performance: was an 8KB read + regex per file per
// Builder turn. Now O(1) for unchanged files.
var (
	docstringMu    sync.RWMutex
	docstringCache = map[string]docstringEntry{}
)

type docstringEntry struct {
	mtime time.Time
	doc   string
}

// extractPythonDocstring reads the first triple-quoted docstring out of a
// Python file (capped at the first 8KB). Cached by (path, mtime).
func extractPythonDocstring(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	mtime := info.ModTime()

	docstringMu.RLock()
	hit, ok := docstringCache[path]
	docstringMu.RUnlock()
	if ok && hit.mtime.Equal(mtime) {
		return hit.doc
	}

	doc := extractPythonDocstringUncached(path)
	docstringMu.Lock()
	docstringCache[path] = docstringEntry{mtime: mtime, doc: doc}
	docstringMu.Unlock()
	return doc
}

func extractPythonDocstringUncached(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	src := string(buf[:n])
	// Look for the first """ or ''' after optional shebang/imports.
	for _, q := range []string{`"""`, `'''`} {
		start := strings.Index(src, q)
		if start < 0 {
			continue
		}
		rest := src[start+3:]
		end := strings.Index(rest, q)
		if end < 0 {
			continue
		}
		doc := strings.TrimSpace(rest[:end])
		// Keep just the first paragraph — short and useful for tooltips.
		if i := strings.Index(doc, "\n\n"); i >= 0 {
			doc = doc[:i]
		}
		// Single-line collapse
		doc = strings.ReplaceAll(doc, "\n", " ")
		if len(doc) > 240 {
			doc = doc[:240] + "…"
		}
		return doc
	}
	return ""
}

// handleListMCP returns the configured MCP servers with connection status and
// each server's tool list. Used by the MCP page in the GUI.
func (s *Server) handleListMCP(c *fiber.Ctx) error {
	if s.mcp == nil {
		return c.JSON(fiber.Map{"servers": []any{}, "note": "MCP not initialised"})
	}
	return c.JSON(fiber.Map{"servers": s.mcp.ServersSnapshot()})
}

// mcpServerBody is the shape the GUI POSTs/PATCHes to add or edit an MCP
// server. Mirrors config.MCPServerConfig with JSON tags so the same payload
// can be re-marshalled back into YAML cleanly.
type mcpServerBody struct {
	ID        string            `json:"id"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
}

// validateMCPServer enforces the per-transport invariants. Returns "" on success
// or a user-facing error string on failure (so the GUI can surface it inline).
func validateMCPServer(body mcpServerBody) string {
	t := strings.ToLower(strings.TrimSpace(body.Transport))
	switch t {
	case "", "stdio":
		if strings.TrimSpace(body.Command) == "" {
			return "stdio transport requires a `command` (e.g. 'npx' or '/usr/local/bin/server')"
		}
	case "http", "https":
		if strings.TrimSpace(body.URL) == "" {
			return "http transport requires a `url`"
		}
	default:
		return fmt.Sprintf("unknown transport %q — expected stdio or http", body.Transport)
	}
	return ""
}

// mcpBodyToServerConfig converts an mcpServerBody to an mcp.ServerConfig for
// hot-adding to the live client.
func mcpBodyToServerConfig(body mcpServerBody) mcp.ServerConfig {
	return mcp.ServerConfig{
		Transport: body.Transport,
		Command:   body.Command,
		Args:      body.Args,
		Env:       body.Env,
		URL:       body.URL,
		Headers:   body.Headers,
	}
}

// handleCreateMCPServer adds a new MCP server to config.yaml and hot-connects
// it immediately — no gateway restart required.
func (s *Server) handleCreateMCPServer(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config file path unknown — cannot persist"})
	}
	var body mcpServerBody
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id is required"})
	}
	if !validMCPID(id) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id may contain only letters, digits, '-' and '_'"})
	}
	if msg := validateMCPServer(body); msg != "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": msg})
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	mcpMap := getOrCreateMap(raw, "mcp")
	serversMap := getOrCreateMap(mcpMap, "servers")
	if _, exists := serversMap[id]; exists {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": fmt.Sprintf("server %q already exists; use PATCH to edit", id)})
	}
	serversMap[id] = mcpServerToYAML(body)

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.log.Info("mcp server created", zap.String("server", id))

	// Hot-connect: start the server live without a restart.
	connectErr := ""
	if s.mcp != nil {
		if err := s.mcp.AddServer(id, mcpBodyToServerConfig(body)); err != nil {
			connectErr = err.Error()
		}
	}

	resp := fiber.Map{"ok": true, "id": id, "restart_needed": false, "message": "Connected."}
	if connectErr != "" {
		resp["message"] = "Saved, but could not connect: " + connectErr
		resp["connect_error"] = connectErr
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

// handleUpdateMCPServer overwrites an existing MCP server config in
// config.yaml. The id in the URL is authoritative — body.id is ignored if set.
func (s *Server) handleUpdateMCPServer(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config file path unknown — cannot persist"})
	}
	id := c.Params("id")
	if id == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id path parameter is required"})
	}
	var body mcpServerBody
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	body.ID = id // URL wins
	if msg := validateMCPServer(body); msg != "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": msg})
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	mcpMap := getOrCreateMap(raw, "mcp")
	serversMap := getOrCreateMap(mcpMap, "servers")
	if _, exists := serversMap[id]; !exists {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": fmt.Sprintf("server %q not found", id)})
	}
	serversMap[id] = mcpServerToYAML(body)

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.log.Info("mcp server updated", zap.String("server", id))

	// Hot-reconnect: remove old process, start updated one.
	connectErr := ""
	if s.mcp != nil {
		if err := s.mcp.AddServer(id, mcpBodyToServerConfig(body)); err != nil {
			connectErr = err.Error()
		}
	}

	resp := fiber.Map{"ok": true, "id": id, "restart_needed": false, "message": "Updated and reconnected."}
	if connectErr != "" {
		resp["message"] = "Saved, but could not reconnect: " + connectErr
		resp["connect_error"] = connectErr
	}
	return c.JSON(resp)
}

// handleDeleteMCPServer removes an MCP server from config.yaml.
func (s *Server) handleDeleteMCPServer(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config file path unknown — cannot persist"})
	}
	id := c.Params("id")
	if id == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id is required"})
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if mcpMap, ok := raw["mcp"].(map[string]any); ok {
		if serversMap, ok := mcpMap["servers"].(map[string]any); ok {
			delete(serversMap, id)
		}
	}
	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.log.Info("mcp server deleted", zap.String("server", id))

	// Hot-disconnect: stop the process immediately.
	if s.mcp != nil {
		_ = s.mcp.RemoveServer(id)
	}

	return c.JSON(fiber.Map{
		"ok":             true,
		"id":             id,
		"restart_needed": false,
		"message":        "Removed and disconnected.",
	})
}

// handleTestMCPServer attempts a minimal reachability check WITHOUT modifying
// any state. For stdio it confirms the executable resolves on PATH; for http
// it issues a HEAD request with a short timeout. Returns ok=true on success
// so the GUI can show a green checkmark before the user clicks Save.
func (s *Server) handleTestMCPServer(c *fiber.Ctx) error {
	var body mcpServerBody
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if msg := validateMCPServer(body); msg != "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": msg})
	}

	transport := strings.ToLower(strings.TrimSpace(body.Transport))
	switch transport {
	case "", "stdio":
		// LookPath returns the resolved path if the command exists. Doesn't run it.
		path, err := exec.LookPath(body.Command)
		if err != nil {
			return c.JSON(fiber.Map{
				"ok":      false,
				"error":   fmt.Sprintf("command %q not found in PATH", body.Command),
				"details": err.Error(),
			})
		}
		return c.JSON(fiber.Map{"ok": true, "resolved_command": path})
	case "http", "https":
		req, err := http.NewRequestWithContext(c.Context(), http.MethodHead, body.URL, nil)
		if err != nil {
			return c.JSON(fiber.Map{"ok": false, "error": err.Error()})
		}
		for k, v := range body.Headers {
			req.Header.Set(k, v)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return c.JSON(fiber.Map{"ok": false, "error": fmt.Sprintf("could not reach %s: %v", body.URL, err)})
		}
		// defer ensures we close the body regardless of which branch we return on.
		defer resp.Body.Close()
		// 405/501 from a HEAD is acceptable — the server is reachable, just doesn't
		// support HEAD. Real MCP handshake happens at gateway boot.
		return c.JSON(fiber.Map{"ok": true, "status_code": resp.StatusCode})
	}
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "unknown transport"})
}

// mcpServerToYAML renders an mcpServerBody as the YAML-friendly map shape
// that ends up in config.yaml.
func mcpServerToYAML(body mcpServerBody) map[string]any {
	t := strings.ToLower(strings.TrimSpace(body.Transport))
	if t == "" {
		t = "stdio"
	}
	out := map[string]any{"transport": t}
	if t == "stdio" {
		if body.Command != "" {
			out["command"] = body.Command
		}
		if len(body.Args) > 0 {
			args := make([]any, len(body.Args))
			for i, a := range body.Args {
				args[i] = a
			}
			out["args"] = args
		}
		if len(body.Env) > 0 {
			out["env"] = mapToAny(body.Env)
		}
	} else {
		if body.URL != "" {
			out["url"] = body.URL
		}
		if len(body.Headers) > 0 {
			out["headers"] = mapToAny(body.Headers)
		}
	}
	return out
}

// mapToAny widens map[string]string → map[string]any so YAML serializes it
// as a plain map.
func mapToAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// handleProvisionGlama fetches an MCP server spec from the Glama registry and
// writes it to config.yaml in one shot — no manual copy-pasting required.
//
// POST /api/v1/mcp/provision-glama
//
// Request:
//
//	{
//	  "glama_url": "https://glama.ai/mcp/servers/adamzaidi/icloud-mcp",
//	  "env": { "IMAP_USER": "you@icloud.com", "IMAP_PASSWORD": "xxxx-xxxx-xxxx-xxxx" }
//	}
//
// Response (env fields missing — returns what's required):
//
//	{ "ok": false, "spec": {...}, "env_required": ["IMAP_USER","IMAP_PASSWORD"] }
//
// Response (saved successfully):
//
//	{ "ok": true, "id": "icloud-mcp", "restart_needed": true, "message": "..." }
func (s *Server) handleProvisionGlama(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "config file path unknown — cannot persist",
		})
	}

	var body struct {
		GlamaURL string            `json:"glama_url"`
		Env      map[string]string `json:"env"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if strings.TrimSpace(body.GlamaURL) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "glama_url is required"})
	}

	// Fetch the spec from Glama (4s timeout inside FetchGlamaServer).
	spec, err := builder.FetchGlamaServer(body.GlamaURL)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "could not fetch Glama server spec: " + err.Error(),
		})
	}

	// Collect required env vars that are missing from the request.
	var missing []string
	for _, ev := range spec.EnvSchema {
		if ev.Required {
			if v := strings.TrimSpace(body.Env[ev.Name]); v == "" {
				missing = append(missing, ev.Name)
			}
		}
	}
	if len(missing) > 0 {
		return c.JSON(fiber.Map{
			"ok":           false,
			"spec":         spec,
			"env_required": missing,
		})
	}

	// All required env vars present — write to config.yaml.
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	mcpMap := getOrCreateMap(raw, "mcp")
	serversMap := getOrCreateMap(mcpMap, "servers")

	id := spec.ID
	if _, exists := serversMap[id]; exists {
		// Server already exists — update it in place.
		s.log.Info("glama provision: updating existing server", zap.String("server", id))
	}

	serverCfg := mcpServerBody{
		ID:        id,
		Transport: "stdio",
		Command:   spec.Command,
		Args:      spec.Args,
		Env:       body.Env,
	}
	serversMap[id] = mcpServerToYAML(serverCfg)

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.log.Info("glama provision: server saved",
		zap.String("server", id),
		zap.String("glama_url", body.GlamaURL),
	)

	// Hot-connect immediately.
	connectErr := ""
	if s.mcp != nil {
		hotCfg := mcp.ServerConfig{
			Transport: "stdio",
			Command:   spec.Command,
			Args:      spec.Args,
			Env:       body.Env,
		}
		if err := s.mcp.AddServer(id, hotCfg); err != nil {
			connectErr = err.Error()
		}
	}

	resp := fiber.Map{
		"ok":             true,
		"id":             id,
		"spec":           spec,
		"restart_needed": false,
		"message":        fmt.Sprintf("%q connected with %d tools.", id, 0),
	}
	if connectErr != "" {
		resp["message"] = fmt.Sprintf("Saved, but connection failed: %s", connectErr)
		resp["connect_error"] = connectErr
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

// handleMCPRegistrySearch proxies search queries to registry.modelcontextprotocol.io.
//
// The GUI cannot call the registry directly because the registry does not set
// CORS headers that would allow browser requests from a different origin. By
// routing through this backend endpoint the browser talks only to the Soulacy
// gateway (same origin), and the gateway makes the outbound call server-side.
// This also provides a natural place to add caching or rate-limiting later
// without touching the frontend.
//
// GET /api/v1/mcp/registry/search?q=...&limit=20&cursor=...
func (s *Server) handleMCPRegistrySearch(c *fiber.Ctx) error {
	query := c.Query("q", "")
	cursor := c.Query("cursor", "")
	limit := c.QueryInt("limit", 20)

	servers, nextCursor, err := builder.SearchMCPRegistry(query, cursor, limit)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "registry search failed: " + err.Error(),
		})
	}
	return c.JSON(fiber.Map{
		"servers":    servers,
		"nextCursor": nextCursor,
	})
}

// handleProvisionMCPRegistry installs an MCP server from the official registry.
// Mirrors the two-phase flow used by handleProvisionGlama:
//
// Phase 1 — spec fetch + env-var check:
//  1. Fetch the server detail from registry.modelcontextprotocol.io via
//     builder.FetchMCPRegistryServer.
//  2. Inspect the spec's EnvSchema for required fields not present in the
//     request body's env map.
//  3. If any required env vars are missing, return
//     { "ok": false, "spec": {...}, "env_required": [...] } with HTTP 200.
//     The GUI renders a form for the missing vars and resubmits.
//
// Phase 2 — save + hot-connect:
//  4. Write the server config to config.yaml (stdio transport, command + args
//     derived from the registry package, env vars from the request).
//  5. Hot-connect via mcp.AddServer so the tools are available immediately
//     without restarting the gateway.
//
// POST /api/v1/mcp/provision-registry
//
// Request:  { "server_name": "io.modelcontextprotocol/filesystem", "env": {...} }
// Response: same shape as provision-glama (ok, spec, env_required, id, message)
func (s *Server) handleProvisionMCPRegistry(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "config file path unknown — cannot persist",
		})
	}

	var body struct {
		ServerName string            `json:"server_name"`
		Env        map[string]string `json:"env"`
		Preview    bool              `json:"preview"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if strings.TrimSpace(body.ServerName) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "server_name is required"})
	}

	spec, err := builder.FetchMCPRegistryServer(body.ServerName)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "could not fetch registry server spec: " + err.Error(),
		})
	}

	// Collect required env vars that are missing.
	var missing []string
	for _, ev := range spec.EnvSchema {
		if ev.Required {
			if v := strings.TrimSpace(body.Env[ev.Name]); v == "" {
				missing = append(missing, ev.Name)
			}
		}
	}
	for _, ev := range spec.URLVariables {
		if ev.Required {
			if v := strings.TrimSpace(body.Env[ev.Name]); v == "" {
				missing = append(missing, ev.Name)
			}
		}
	}
	for _, ev := range spec.HeaderSchema {
		if ev.Required {
			if v := strings.TrimSpace(body.Env[ev.Name]); v == "" {
				missing = append(missing, ev.Name)
			}
		}
	}
	if body.Preview {
		return c.JSON(fiber.Map{
			"ok":           false,
			"preview":      true,
			"spec":         spec,
			"env_required": missing,
		})
	}
	if len(missing) > 0 {
		return c.JSON(fiber.Map{
			"ok":           false,
			"spec":         spec,
			"env_required": missing,
		})
	}

	// Write to config.yaml.
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	mcpMap := getOrCreateMap(raw, "mcp")
	serversMap := getOrCreateMap(mcpMap, "servers")

	id := spec.ID
	serverBody, err := registrySpecToServerBody(id, spec, body.Env)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	serversMap[id] = mcpServerToYAML(serverBody)

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.log.Info("mcp registry provision: server saved",
		zap.String("server", id),
		zap.String("registry_name", body.ServerName),
	)

	// Hot-connect immediately.
	connectErr := ""
	if s.mcp != nil {
		if err := s.mcp.AddServer(id, mcp.ServerConfig{
			Transport: serverBody.Transport,
			Command:   serverBody.Command,
			Args:      serverBody.Args,
			Env:       serverBody.Env,
			URL:       serverBody.URL,
			Headers:   serverBody.Headers,
		}); err != nil {
			connectErr = err.Error()
		}
	}

	resp := fiber.Map{
		"ok":             true,
		"id":             id,
		"spec":           spec,
		"restart_needed": false,
		"message":        fmt.Sprintf("%q connected.", id),
	}
	if connectErr != "" {
		resp["message"] = fmt.Sprintf("Saved, but connection failed: %s", connectErr)
		resp["connect_error"] = connectErr
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

func registrySpecToServerBody(id string, spec *builder.GlamaProvisionSpec, values map[string]string) (mcpServerBody, error) {
	if spec == nil {
		return mcpServerBody{}, fmt.Errorf("registry spec is missing")
	}
	transport := strings.ToLower(strings.TrimSpace(spec.Transport))
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		if strings.TrimSpace(spec.Command) == "" {
			return mcpServerBody{}, fmt.Errorf("registry package did not provide a command")
		}
		return mcpServerBody{
			ID:        id,
			Transport: "stdio",
			Command:   spec.Command,
			Args:      spec.Args,
			Env:       values,
		}, nil
	case "http", "https":
		resolvedURL := spec.URL
		for _, ev := range spec.URLVariables {
			resolvedURL = strings.ReplaceAll(resolvedURL, "{"+ev.Name+"}", strings.TrimSpace(values[ev.Name]))
		}
		if strings.TrimSpace(resolvedURL) == "" {
			return mcpServerBody{}, fmt.Errorf("registry remote did not provide a URL")
		}
		headers := map[string]string{}
		for _, ev := range spec.HeaderSchema {
			if v := strings.TrimSpace(values[ev.Name]); v != "" {
				headers[ev.Name] = v
			}
		}
		return mcpServerBody{
			ID:        id,
			Transport: "http",
			URL:       resolvedURL,
			Headers:   headers,
		}, nil
	default:
		return mcpServerBody{}, fmt.Errorf("unsupported registry transport %q", spec.Transport)
	}
}

// validMCPID accepts the same characters the rest of the codebase uses for
// safe identifiers (filenames, table-name sanitisation, etc.).
func validMCPID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// handleAgentActions returns recent events from the agent's action log.
//
// The action log is an append-only record of every event the engine emits for
// an agent: message.in, message.out, tool.call, tool.result, error, etc.
// This handler tails the log and optionally filters it.
//
// Query params:
//
//	limit (int, default 500)  — maximum number of events to return (most recent)
//	types (string, optional)  — comma-separated allowlist of event types.
//	                            When supplied, only events whose Type field
//	                            appears in the list are returned. Example:
//	                            "message.in,message.out,error" returns only
//	                            conversation turns and failures, omitting the
//	                            verbose tool.call / tool.result lines that
//	                            inflate the History view payload. When omitted,
//	                            all event types are returned.
func (s *Server) handleAgentActions(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.actions == nil {
		return c.JSON(fiber.Map{"agent_id": id, "path": "", "events": []message.Event{}, "note": "action logging disabled"})
	}
	limit := c.QueryInt("limit", 500)
	events, err := s.actions.Tail(id, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Optional event-type filter — callers like the History panel only need a
	// subset and passing types=message.in,message.out,error avoids returning
	// thousands of tool.log lines that inflate the response and bury run boundaries.
	if typesParam := c.Query("types", ""); typesParam != "" {
		allowed := map[string]bool{}
		for _, t := range strings.Split(typesParam, ",") {
			if t = strings.TrimSpace(t); t != "" {
				allowed[t] = true
			}
		}
		if len(allowed) > 0 {
			filtered := events[:0]
			for _, ev := range events {
				if allowed[ev.Type] {
					filtered = append(filtered, ev)
				}
			}
			events = filtered
		}
	}

	return c.JSON(fiber.Map{
		"agent_id": id,
		"path":     s.actions.EventFilePath(id),
		"events":   events,
		"count":    len(events),
	})
}

// --- Memory ---

func (s *Server) handleListMemory(c *fiber.Ctx) error {
	agentID := c.Params("agent_id")
	query := c.Query("q", "")

	var entries interface{}
	var err error
	if query != "" {
		entries, err = s.engine.MemorySearch(agentID, query, 200)
	} else {
		entries, err = s.engine.MemoryList(agentID, 200)
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"entries": entries})
}

func (s *Server) handleDeleteMemorySession(c *fiber.Ctx) error {
	sessionID := c.Params("session_id")
	if err := s.engine.MemoryPurgeSession(sessionID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"message": "session memory purged", "session_id": sessionID})
}

// --- Providers ---

// handleListProviders returns each configured provider with its API key redacted.
// Also includes which provider IDs are currently registered (live) so the GUI
// can show a "configured but needs restart" state for newly-added providers.
func (s *Server) handleListProviders(c *fiber.Ctx) error {
	registered := map[string]bool{}
	if s.llmRouter != nil {
		for _, id := range s.llmRouter.ProviderIDs() {
			registered[id] = true
		}
	}
	providers := make(fiber.Map, len(s.cfg.LLM.Providers))
	for name, pc := range s.cfg.LLM.Providers {
		apiKey := ""
		if pc.APIKey != "" {
			apiKey = "***"
		}
		providers[name] = fiber.Map{
			"base_url":            pc.BaseURL,
			"api_key":             apiKey,
			"model":               pc.Model,
			"keep_alive":          pc.KeepAlive,
			"options":             pc.Options,
			"prompt_caching":      pc.PromptCaching,
			"thinking_budget":     pc.ThinkingBudget,
			"safety_level":        pc.SafetyLevel,
			"extended_thinking":   pc.ExtendedThinking,
			"organization":        pc.Organization,
			"parallel_tool_calls": pc.ParallelToolCalls,
			"registered":          registered[name],
		}
	}
	// Known provider IDs the GUI should always offer (even when not yet
	// configured), so users can pick them from a dropdown to add credentials.
	known := []string{"ollama", "openai", "anthropic", "google", "groq", "mistral", "openrouter"}
	return c.JSON(fiber.Map{
		"providers":        providers,
		"default_provider": s.cfg.LLM.DefaultProvider,
		"known":            known,
		"registered":       s.llmRouter.ProviderIDs(),
	})
}

// handleListModels asks the live provider for its available models.
// For Ollama this performs the equivalent of `ollama list` (GET /api/tags).
func (s *Server) handleListModels(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.llmRouter == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "LLM router unavailable"})
	}
	p := s.llmRouter.Provider(id)
	if p == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": fmt.Sprintf("provider %q is not registered (configured: %v)", id, s.llmRouter.ProviderIDs()),
		})
	}

	ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
	defer cancel()

	models, err := p.Models(ctx)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}

	selected := ""
	if pc, ok := s.cfg.LLM.Providers[id]; ok {
		selected = pc.Model
	}
	return c.JSON(fiber.Map{"models": models, "selected": selected})
}

func (s *Server) agentValidationOptions(ctx context.Context) agentvalidate.Options {
	opts := agentvalidate.Options{Config: s.cfg, ProviderModels: map[string][]string{}}
	if s.llmRouter == nil {
		return opts
	}
	opts.RegisteredProviders = s.llmRouter.ProviderIDs()
	for _, id := range opts.RegisteredProviders {
		p := s.llmRouter.Provider(id)
		if p == nil {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		models, err := p.Models(probeCtx)
		cancel()
		if err == nil && len(models) > 0 {
			opts.ProviderModels[id] = models
		}
	}
	return opts
}

// handleSetProviderModel persists the chosen default model for a provider into
// config.yaml (llm.providers.<id>.model) and updates the live config.
func (s *Server) handleSetProviderModel(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config file path unknown — cannot persist"})
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if req.Model == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "model is required"})
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	llmMap := getOrCreateMap(raw, "llm")
	provsMap := getOrCreateMap(llmMap, "providers")
	provMap := getOrCreateMap(provsMap, id)
	provMap["model"] = req.Model

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Mirror into live config.
	if s.cfg.LLM.Providers == nil {
		s.cfg.LLM.Providers = map[string]config.ProviderConfig{}
	}
	pc := s.cfg.LLM.Providers[id]
	pc.Model = req.Model
	s.cfg.LLM.Providers[id] = pc

	s.log.Info("provider model updated via API", zap.String("provider", id), zap.String("model", req.Model))
	return c.JSON(fiber.Map{
		"ok":      true,
		"model":   req.Model,
		"message": "Model saved. Restart the gateway for it to take effect for running agents.",
	})
}

// handleSetProviderCredentials persists base_url / api_key for a provider into
// config.yaml. The new provider takes effect after a gateway restart — the
// response surfaces that hint to the GUI.
func (s *Server) handleSetProviderCredentials(c *fiber.Ctx) error {
	id := c.Params("id")
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config file path unknown — cannot persist"})
	}
	var req struct {
		BaseURL           string         `json:"base_url"`
		APIKey            string         `json:"api_key"`
		Model             string         `json:"model"`
		KeepAlive         string         `json:"keep_alive"`
		Options           map[string]any `json:"options"`
		PromptCaching     *bool          `json:"prompt_caching"`      // pointer: omitted ≠ false
		ThinkingBudget    *int           `json:"thinking_budget"`     // Google/Anthropic: 0=off, -1=auto, N=tokens
		SafetyLevel       *string        `json:"safety_level"`        // Google: ""|"default"|"off"|"strict"
		ExtendedThinking  *bool          `json:"extended_thinking"`   // Anthropic: Claude 3.7+ thinking
		Organization      *string        `json:"organization"`        // OpenAI: Org ID header
		ParallelToolCalls *bool          `json:"parallel_tool_calls"` // OpenAI: false=serialize tool calls
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if id == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "provider id is required"})
	}

	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	llmMap := getOrCreateMap(raw, "llm")
	provsMap := getOrCreateMap(llmMap, "providers")
	provMap := getOrCreateMap(provsMap, id)
	// Only update fields the caller actually sent (so a Save Model only doesn't
	// clobber the api_key — *** is not a real value).
	if req.BaseURL != "" {
		provMap["base_url"] = req.BaseURL
	}
	if req.APIKey != "" && req.APIKey != "***" {
		provMap["api_key"] = req.APIKey
	}
	if req.Model != "" {
		provMap["model"] = req.Model
	}
	if id == "ollama" {
		provMap["keep_alive"] = req.KeepAlive
	}
	if req.PromptCaching != nil {
		provMap["prompt_caching"] = *req.PromptCaching
	}
	if req.ThinkingBudget != nil {
		provMap["thinking_budget"] = *req.ThinkingBudget
	}
	if req.SafetyLevel != nil {
		provMap["safety_level"] = *req.SafetyLevel
	}
	if req.ExtendedThinking != nil {
		provMap["extended_thinking"] = *req.ExtendedThinking
	}
	if req.Organization != nil {
		provMap["organization"] = *req.Organization
	}
	if req.ParallelToolCalls != nil {
		provMap["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.Options != nil {
		if len(req.Options) == 0 {
			delete(provMap, "options")
		} else {
			provMap["options"] = req.Options
		}
	}

	if err := writeRawConfig(s.cfgPath, raw); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Mirror to live config.
	if s.cfg.LLM.Providers == nil {
		s.cfg.LLM.Providers = map[string]config.ProviderConfig{}
	}
	pc := s.cfg.LLM.Providers[id]
	if req.BaseURL != "" {
		pc.BaseURL = req.BaseURL
	}
	if req.APIKey != "" && req.APIKey != "***" {
		pc.APIKey = req.APIKey
	}
	if req.Model != "" {
		pc.Model = req.Model
	}
	if id == "ollama" {
		pc.KeepAlive = req.KeepAlive
	}
	if req.PromptCaching != nil {
		pc.PromptCaching = *req.PromptCaching
	}
	if req.ThinkingBudget != nil {
		pc.ThinkingBudget = *req.ThinkingBudget
	}
	if req.SafetyLevel != nil {
		pc.SafetyLevel = *req.SafetyLevel
	}
	if req.ExtendedThinking != nil {
		pc.ExtendedThinking = *req.ExtendedThinking
	}
	if req.Organization != nil {
		pc.Organization = *req.Organization
	}
	if req.ParallelToolCalls != nil {
		pc.ParallelToolCalls = req.ParallelToolCalls
	}
	if req.Options != nil {
		pc.Options = req.Options
	}
	s.cfg.LLM.Providers[id] = pc

	if id == "ollama" && req.APIKey != "" && req.APIKey != "***" && s.engine != nil {
		s.engine.SetOllamaAPIKey(req.APIKey)
	}

	s.log.Info("provider credentials updated", zap.String("provider", id))
	message := "Saved. Restart the gateway for the new provider to be registered."
	if id == "ollama" {
		message = "Saved. Ollama web-search credentials are active for new requests; restart to apply local model, base URL, or tuning changes."
	} else if id == "anthropic" {
		message = "Saved. Restart the gateway for changes to take effect."
	}
	return c.JSON(fiber.Map{
		"ok":      true,
		"message": message,
	})
}

// --- Skills ---

// handleListSkills returns the catalog of all loaded Agent Skills.
func (s *Server) handleListSkills(c *fiber.Ctx) error {
	if s.skillLoader == nil {
		return c.JSON(fiber.Map{"skills": []struct{}{}, "count": 0})
	}
	all := s.skillLoader.All()

	type skillSummary struct {
		Name          string            `json:"name"`
		Description   string            `json:"description"`
		License       string            `json:"license,omitempty"`
		Compatibility string            `json:"compatibility,omitempty"`
		Metadata      map[string]string `json:"metadata,omitempty"`
		Dir           string            `json:"dir"`
		Resources     []string          `json:"resources"`
	}
	out := make([]skillSummary, len(all))
	for i, sk := range all {
		out[i] = skillSummary{
			Name:          sk.Name,
			Description:   sk.Description,
			License:       sk.License,
			Compatibility: sk.Compatibility,
			Metadata:      sk.Metadata,
			Dir:           sk.Dir,
			Resources:     sk.ResourceFiles(),
		}
	}
	return c.JSON(fiber.Map{"skills": out, "count": len(out)})
}

// handleGetSkill returns the full content (instructions) of a single skill.
func (s *Server) handleGetSkill(c *fiber.Ctx) error {
	if s.skillLoader == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "skills not enabled"})
	}
	name := c.Params("name")
	sk := s.skillLoader.Get(name)
	if sk == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "skill not found"})
	}
	return c.JSON(fiber.Map{
		"name":          sk.Name,
		"description":   sk.Description,
		"license":       sk.License,
		"compatibility": sk.Compatibility,
		"metadata":      sk.Metadata,
		"allowed_tools": sk.AllowedTools,
		"body":          sk.Body,
		"dir":           sk.Dir,
		"resources":     sk.ResourceFiles(),
	})
}

// handleProvisionAgenticSkill fetches a skill from agenticskills.io and
// installs it into ~/.soulacy/skills/ — no restart required.
//
// Request:  POST /api/skills/provision-agenticskills
//
//	Body:   { "url": "https://agenticskills.io/skills/frontend-design" }
//	        or { "slug": "frontend-design" }
//
// Response (success): { "ok": true, "slug": "...", "source": "org/repo@sha", "message": "..." }
// Response (error):   { "ok": false, "error": "..." }
//
// Flow:
//  1. Derive slug from the URL or the slug field.
//  2. Fetch the agenticskills.io skill page HTML.
//  3. Extract the "View on GitHub" blob URL via regex.
//  4. Download the raw SKILL.md from raw.githubusercontent.com.
//  5. Write to ~/.soulacy/skills/<slug>/SKILL.md.
//  6. Hot-rescan via the Scan() method on the loader (if available).
var githubBlobRe = regexp.MustCompile(`href="(https://github\.com/[^/]+/[^/]+/blob/[^/"]+/[^"]+/SKILL\.md)"`)

// handleRescanSkills re-scans the skill directories so freshly installed
// skills (e.g. `sy skill install <slug>`, Story E18) hot-load without a
// gateway restart.
//
// POST /api/v1/skills/rescan → { "ok": true, "count": <loaded skills> }
func (s *Server) handleRescanSkills(c *fiber.Ctx) error {
	if s.skillLoader == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"ok": false, "error": "skill loader unavailable",
		})
	}
	scanner, ok := s.skillLoader.(interface{ Scan() []error })
	if !ok {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{
			"ok": false, "error": "skill loader does not support rescanning",
		})
	}
	if errs := scanner.Scan(); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return c.JSON(fiber.Map{"ok": true, "count": len(s.skillLoader.All()), "warnings": msgs})
	}
	return c.JSON(fiber.Map{"ok": true, "count": len(s.skillLoader.All())})
}

func (s *Server) handleProvisionAgenticSkill(c *fiber.Ctx) error {
	if s.skillLoader == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"ok": false, "error": "skill loader not initialised",
		})
	}

	var body struct {
		URL  string `json:"url"`
		Slug string `json:"slug"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "invalid JSON body"})
	}

	// Derive slug.
	slug := strings.TrimSpace(body.Slug)
	if slug == "" && body.URL != "" {
		u, err := url.Parse(strings.TrimSpace(body.URL))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "invalid URL"})
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 || parts[0] != "skills" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"ok": false, "error": "URL must be https://agenticskills.io/skills/<slug>",
			})
		}
		slug = parts[1]
	}
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": "url or slug required"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 30 * time.Second}

	// 1. Fetch agenticskills.io page.
	pageURL := fmt.Sprintf("https://agenticskills.io/skills/%s", url.PathEscape(slug))
	pageReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"ok": false, "error": err.Error()})
	}
	pageReq.Header.Set("User-Agent", "Soulacy/1.0 (+https://github.com/soulacy/soulacy)")

	pageResp, err := client.Do(pageReq)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("agenticskills.io unreachable: %v", err),
		})
	}
	defer pageResp.Body.Close()

	if pageResp.StatusCode == http.StatusNotFound {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("skill %q not found on agenticskills.io", slug),
		})
	}
	if pageResp.StatusCode >= 300 {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("agenticskills.io: HTTP %d", pageResp.StatusCode),
		})
	}

	pageHTML, err := io.ReadAll(io.LimitReader(pageResp.Body, 2<<20)) // 2 MB cap
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"ok": false, "error": "failed to read page"})
	}

	// 2. Extract GitHub blob URL from page.
	match := githubBlobRe.FindSubmatch(pageHTML)
	if match == nil {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"ok":    false,
			"error": "could not find SKILL.md GitHub source on agenticskills.io — the skill may not have a public GitHub source",
		})
	}
	blobURL := string(match[1])

	// 3. Convert blob URL → raw URL.
	// https://github.com/{org}/{repo}/blob/{ref}/{path}
	// → https://raw.githubusercontent.com/{org}/{repo}/{ref}/{path}
	rawURL := strings.Replace(blobURL, "https://github.com/", "https://raw.githubusercontent.com/", 1)
	rawURL = strings.Replace(rawURL, "/blob/", "/", 1)

	rawReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"ok": false, "error": err.Error()})
	}
	rawResp, err := client.Do(rawReq)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("download SKILL.md: %v", err),
		})
	}
	defer rawResp.Body.Close()

	if rawResp.StatusCode >= 300 {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("GitHub: HTTP %d for SKILL.md", rawResp.StatusCode),
		})
	}

	skillMD, err := io.ReadAll(io.LimitReader(rawResp.Body, 1<<20)) // 1 MB cap
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"ok": false, "error": "failed to read SKILL.md"})
	}

	// 4. Write to <workspace>/skills/<slug>/SKILL.md.
	wsPaths, err := config.ResolveWorkspace()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"ok": false, "error": "cannot resolve workspace",
		})
	}
	skillDir := filepath.Join(wsPaths.Skills, slug)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("create skill dir: %v", err),
		})
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillMD, 0o644); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"ok": false, "error": fmt.Sprintf("write SKILL.md: %v", err),
		})
	}

	// 5. Hot-rescan (skills.Loader implements Scan(); use type assertion to
	//    avoid adding the method to the runtime.SkillLoader interface).
	if scanner, ok := s.skillLoader.(interface{ Scan() []error }); ok {
		_ = scanner.Scan()
	}

	// Derive a friendly source label from the blob URL.
	// e.g. "anthropics/skills@5be498e"
	source := strings.TrimPrefix(blobURL, "https://github.com/")
	if i := strings.Index(source, "/blob/"); i >= 0 {
		repo := source[:i]
		rest := source[i+len("/blob/"):]
		sha := rest
		if j := strings.Index(rest, "/"); j >= 0 {
			sha = rest[:j]
		}
		if len(sha) > 8 {
			sha = sha[:8]
		}
		source = repo + "@" + sha
	}

	return c.JSON(fiber.Map{
		"ok":      true,
		"slug":    slug,
		"source":  source,
		"message": fmt.Sprintf("Skill %q installed from %s.", slug, source),
	})
}

// --- Templates ---
//
// Starter agent definitions a user can clone with one click. Inspired by
// Langflow's "New Project from Template" flow. Default templates ship
// embedded in the binary; users can drop additional *.yaml files under
// ~/.soulacy/templates to extend or override.

// templatesCatalog is constructed per-request rather than cached on Server
// so a user can drop new files into ~/.soulacy/templates without a
// restart. The List() call is cheap (parses ~4 small YAML files).
func (s *Server) templatesCatalog() *templates.Catalog {
	return templates.New(templates.DefaultUserDir())
}

func (s *Server) handleListTemplates(c *fiber.Ctx) error {
	entries, err := s.templatesCatalog().List()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"templates": entries, "count": len(entries)})
}

// handleInstantiateTemplate clones a template into a fresh agent via the
// loader. Body (all optional):
//
//	{ "id": "my-bot" }   // desired agent ID; auto-derived if omitted
//
// Returns the created Definition. The agent is created enabled (matches the
// normal create-agent flow) so it shows up in the GUI immediately. The
// scheduler is also notified so cron-triggered templates start ticking.
func (s *Server) handleInstantiateTemplate(c *fiber.Ctx) error {
	name := c.Params("name")
	if name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "template name is required"})
	}
	var req struct {
		ID string `json:"id"`
	}
	// Body is optional — empty body is fine.
	_ = c.BodyParser(&req)

	def, err := s.templatesCatalog().Instantiate(name, req.ID, func(candidate string) bool {
		return s.loader.Get(candidate) == nil
	})
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}

	// Default LLM provider to the configured one if the template left it
	// blank (templates ship with `ollama` but a user without Ollama will
	// want their own default to win).
	if def.LLM.Provider == "" {
		def.LLM.Provider = s.cfg.LLM.DefaultProvider
	}
	def.Enabled = true

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if err := s.loader.Upsert(dir, def); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Register with scheduler if applicable (cron / oneshot templates).
	_ = s.scheduler.RegisterAgent(def)

	return c.Status(fiber.StatusCreated).JSON(def)
}
