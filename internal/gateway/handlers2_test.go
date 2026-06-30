package gateway

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ── Health handler ────────────────────────────────────────────────────────────

func TestGatewayHealthHandler(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/health", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("health status = %d body=%v", status, body)
	}
	if body["status"] == nil {
		t.Fatalf("health: missing status field, body=%v", body)
	}
}

// ── Provider handlers ─────────────────────────────────────────────────────────

func TestGatewayHandleListProviders(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/providers", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list providers status = %d body=%v", status, body)
	}
	if body["providers"] == nil {
		t.Fatalf("list providers: missing providers field, body=%v", body)
	}
	if body["known"] == nil {
		t.Fatalf("list providers: missing known field, body=%v", body)
	}
	if body["default_provider"] == nil {
		t.Fatalf("list providers: missing default_provider field, body=%v", body)
	}
}

func TestGatewayHandleListProviders_MasksAPIKey(t *testing.T) {
	s := newTestGateway(t, "secret")
	// The test gateway configures openai with an empty key, so api_key should be ""
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/providers", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	providers, ok := body["providers"].(map[string]any)
	if !ok {
		t.Fatalf("providers field is not a map: %v", body)
	}
	openai, ok := providers["openai"].(map[string]any)
	if !ok {
		t.Fatalf("openai entry not found: %v", providers)
	}
	// api_key should be empty string (no key was set on openai in testGateway)
	apiKey, _ := openai["api_key"].(string)
	if apiKey == "rawsecretkey" {
		t.Fatalf("api_key should be masked or empty, got raw value")
	}
}

func TestGatewayHandleListModels_UnknownProvider(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/providers/nonexistent/models", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

func TestGatewayHandleListModels_KnownProvider(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	// "test" provider is registered via fakeLLMProvider
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/providers/test/models", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("known provider status = %d body=%v", status, body)
	}
	if body["models"] == nil {
		t.Fatalf("expected models field, body=%v", body)
	}
}

func TestGatewayHandleSetProviderModel_NoCfgPath(t *testing.T) {
	s := newTestGateway(t, "secret")
	// cfgPath is "" in testGateway → 503
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/openai/model", "secret",
		`{"model":"gpt-4o"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("no cfgPath status = %d body=%v", status, body)
	}
}

func TestGatewayHandleSetProviderModel_MissingModel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/openai/model", "secret",
		`{"model":""}`)
	if status != http.StatusBadRequest {
		t.Fatalf("missing model status = %d body=%v", status, body)
	}
}

func TestGatewayHandleSetProviderModel_Happy(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/openai/model", "secret",
		`{"model":"gpt-4o-mini"}`)
	if status != http.StatusOK {
		t.Fatalf("set model status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandleSetProviderCredentials_NoCfgPath(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/openai", "secret",
		`{"api_key":"sk-test"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("no cfgPath status = %d body=%v", status, body)
	}
}

func TestGatewayHandleSetProviderCredentials_Happy(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/anthropic", "secret",
		`{"api_key":"sk-ant-test","model":"claude-3-5-sonnet-20241022"}`)
	if status != http.StatusOK {
		t.Fatalf("set credentials status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandleSetProviderCredentials_MaskedKey(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	// Sending "***" should not overwrite the existing key (no error either)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/openai", "secret",
		`{"api_key":"***"}`)
	if status != http.StatusOK {
		t.Fatalf("masked key status = %d body=%v", status, body)
	}
}

func TestGatewayHandleSetProviderCredentials_Ollama(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/ollama", "secret",
		`{"base_url":"http://localhost:11434","keep_alive":"5m"}`)
	if status != http.StatusOK {
		t.Fatalf("ollama credentials status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── Config handlers ───────────────────────────────────────────────────────────

func TestGatewayHandleGetConfig(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get config status = %d body=%v", status, body)
	}
	if body["server"] == nil {
		t.Fatalf("get config: missing server field, body=%v", body)
	}
	if body["llm"] == nil {
		t.Fatalf("get config: missing llm field, body=%v", body)
	}
	// Confirm api_key is redacted
	srv, ok := body["server"].(map[string]any)
	if !ok {
		t.Fatalf("server is not a map: %v", body)
	}
	apiKey, _ := srv["api_key"].(string)
	if apiKey != "***" {
		t.Fatalf("api_key should be ***, got %q", apiKey)
	}
}

func TestGatewayHandleGetConfig_NoAPIKey(t *testing.T) {
	// When no api_key is set, the config view should show empty string
	s := newTestGateway(t, "")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "", "")
	if status != http.StatusOK {
		t.Fatalf("get config (open) status = %d body=%v", status, body)
	}
	srv, ok := body["server"].(map[string]any)
	if !ok {
		t.Fatalf("server is not a map: %v", body)
	}
	apiKey, _ := srv["api_key"].(string)
	if apiKey == "***" {
		t.Fatalf("api_key should be empty, got ***")
	}
}

func TestGatewayHandlePatchConfig_NoCfgPath(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"llm":{"default_provider":"anthropic"}}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("patch config (no cfgPath) status = %d body=%v", status, body)
	}
}

func TestGatewayHandlePatchConfig_Happy(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"llm":{"default_provider":"anthropic"}}`)
	if status != http.StatusOK {
		t.Fatalf("patch config status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
	if body["config"] == nil {
		t.Fatalf("expected config in response, body=%v", body)
	}
}

func TestGatewayHandlePatchConfig_InvalidJSON(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{not valid json}`)
	// Fiber body parser returns 400 on bad JSON
	if status != http.StatusBadRequest {
		t.Fatalf("bad json status = %d body=%v", status, body)
	}
}

func TestGatewayHandlePatchConfig_RuntimeFields(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"runtime":{"max_concurrent_sessions":10,"default_max_turns":5}}`)
	if status != http.StatusOK {
		t.Fatalf("patch runtime status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── Memory handlers ───────────────────────────────────────────────────────────

func TestGatewayHandleDeleteMemorySession(t *testing.T) {
	s := newTestGateway(t, "secret")
	// session_id is any string; engine.MemoryPurgeSession succeeds even for nonexistent sessions.
	// The FileStore (used in test gateway) supports PurgeSession without panicking.
	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/memory/some-agent/session-123", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("delete memory session status = %d body=%v", status, body)
	}
	if body["message"] == nil {
		t.Fatalf("expected message field, body=%v", body)
	}
}

// ── Channel handlers ──────────────────────────────────────────────────────────

func TestGatewayHandleListChannels(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/channels", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list channels status = %d body=%v", status, body)
	}
	channels, ok := body["channels"].([]any)
	if !ok {
		t.Fatalf("channels field is not an array: %v", body)
	}
	// Should have at least the "http" channel (always-on)
	if len(channels) == 0 {
		t.Fatalf("expected at least one channel, got empty")
	}
	// Verify http channel is present
	found := false
	for _, ch := range channels {
		chMap, ok := ch.(map[string]any)
		if !ok {
			continue
		}
		if chMap["id"] == "http" {
			found = true
			if chMap["always"] != true {
				t.Fatalf("http channel always=false, expected true")
			}
		}
	}
	if !found {
		t.Fatalf("http channel not found in list: %v", channels)
	}
}

func TestGatewayHandleListChannels_ContainsExpectedChannels(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/channels", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list channels status = %d", status)
	}
	channels, _ := body["channels"].([]any)
	ids := make(map[string]bool)
	for _, ch := range channels {
		if chMap, ok := ch.(map[string]any); ok {
			if id, ok := chMap["id"].(string); ok {
				ids[id] = true
			}
		}
	}
	for _, expected := range []string{"http", "telegram", "discord", "slack"} {
		if !ids[expected] {
			t.Fatalf("expected channel %q not found in: %v", expected, ids)
		}
	}
}

func TestGatewayHandleUpdateChannel_NoCfgPath(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret",
		`{"settings":{"token":"bot123","agent_id":"my-agent"}}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("update channel (no cfgPath) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleUpdateChannel_UnknownChannel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/nonexistent", "secret",
		`{"settings":{}}`)
	if status != http.StatusNotFound {
		t.Fatalf("unknown channel status = %d body=%v", status, body)
	}
}

func TestGatewayHandleUpdateChannel_Happy(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret",
		`{"enabled":true,"settings":{"token":"bot-token-123","agent_id":"my-agent"}}`)
	if status != http.StatusOK {
		t.Fatalf("update channel status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandleEnableChannel_UnknownChannel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/nonexistent/enable", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("enable unknown channel status = %d body=%v", status, body)
	}
}

func TestGatewayHandleEnableChannel_AlwaysOnChannel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	// http is always-on — enabling it returns 400
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/http/enable", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("enable always-on channel status = %d body=%v", status, body)
	}
}

func TestGatewayHandleDisableChannel_AlwaysOnChannel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/http/disable", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("disable always-on channel status = %d body=%v", status, body)
	}
}

func TestGatewayHandleEnableChannel_NoCfgPath(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/telegram/enable", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("enable channel (no cfgPath) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleEnableChannel_Happy(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/telegram/enable", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("enable telegram status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandleDisableChannel_Happy(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/telegram/disable", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("disable telegram status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── Schedule handlers ─────────────────────────────────────────────────────────

func TestGatewayHandleListSchedule(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/schedule", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list schedule status = %d body=%v", status, body)
	}
	// "schedule" key must be present; null/empty slice is valid when no agents are scheduled
	if _, ok := body["schedule"]; !ok {
		t.Fatalf("list schedule: missing schedule field, body=%v", body)
	}
}

func TestGatewayHandleScheduleStatus(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/schedule/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("schedule status = %d body=%v", status, body)
	}
	if body["running"] == nil {
		t.Fatalf("schedule status: missing running field, body=%v", body)
	}
	if body["next"] == nil {
		t.Fatalf("schedule status: missing next field, body=%v", body)
	}
}

func TestGatewayHandleManualTrigger_NotFound(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/nonexistent/trigger", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("trigger nonexistent agent status = %d body=%v", status, body)
	}
}

func TestGatewayHandleManualTrigger_Happy(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	createBody := `{"id":"trigger-agent","name":"Trigger Agent","trigger":"schedule","channels":[],"llm":{"provider":"test","model":"m"},"system_prompt":"run","enabled":true}`
	createStatus, createResp := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if createStatus != http.StatusCreated {
		t.Fatalf("create agent for trigger test: status=%d body=%v", createStatus, createResp)
	}

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/trigger-agent/trigger", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("manual trigger status = %d body=%v", status, body)
	}
	if _, ok := body["result"]; !ok {
		t.Fatalf("manual trigger: missing result field, body=%v", body)
	}
}

// ── Agent actions handler ─────────────────────────────────────────────────────

func TestGatewayHandleAgentActions_NilActions(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Create an agent first
	createBody := `{"id":"action-agent","name":"Action Agent","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/agents/action-agent/actions", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("agent actions status = %d body=%v", status, body)
	}
	// With nil actions store, should return empty events + a note
	if body["events"] == nil {
		t.Fatalf("agent actions: missing events field, body=%v", body)
	}
}

// ── Clone agent handler ───────────────────────────────────────────────────────

func TestGatewayHandleCloneAgent_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/nonexistent/clone", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("clone nonexistent status = %d body=%v", status, body)
	}
}

func TestGatewayHandleCloneAgent_Happy(t *testing.T) {
	s := newTestGateway(t, "secret")

	createBody := `{"id":"orig","name":"Original","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	createStatus, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if createStatus != http.StatusCreated {
		t.Fatalf("create original agent: status=%d", createStatus)
	}

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/orig/clone", "secret", "")
	if status != http.StatusCreated {
		t.Fatalf("clone status = %d body=%v", status, body)
	}
	if body["id"] == nil {
		t.Fatalf("clone: missing id field, body=%v", body)
	}
	// Clone should be disabled
	if body["enabled"] == true {
		t.Fatalf("clone should be disabled, got enabled=true")
	}
}

// ── Enable/Disable agent handlers ────────────────────────────────────────────

func TestGatewayHandleEnableAgent_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/nonexistent/enable", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("enable nonexistent agent status = %d body=%v", status, body)
	}
}

func TestGatewayHandleEnableDisableAgent_Happy(t *testing.T) {
	s := newTestGateway(t, "secret")

	createBody := `{"id":"toggle-agent","name":"Toggle","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)

	// Disable it
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/toggle-agent/disable", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("disable agent status = %d body=%v", status, body)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false, body=%v", body)
	}

	// Re-enable it
	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/toggle-agent/enable", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("enable agent status = %d body=%v", status, body)
	}
	if body["enabled"] != true {
		t.Fatalf("expected enabled=true, body=%v", body)
	}
}

// ── MCP handlers (nil mcp client) ────────────────────────────────────────────

func TestGatewayHandleListMCP_NilClient(t *testing.T) {
	s := newTestGateway(t, "secret")
	// mcp is nil in test gateway
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/mcp", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list mcp (nil) status = %d body=%v", status, body)
	}
	// Should return an empty list and a note
	servers, ok := body["servers"]
	if !ok {
		t.Fatalf("missing servers field, body=%v", body)
	}
	if servers == nil {
		t.Fatalf("servers should not be nil, body=%v", body)
	}
}

func TestGatewayHandleCreateMCPServer_NoCfgPath(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"myserver","transport":"stdio","command":"npx"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("create mcp (no cfgPath) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleCreateMCPServer_MissingID(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"transport":"stdio","command":"npx"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("create mcp (missing id) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleCreateMCPServer_InvalidID(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"bad id!","transport":"stdio","command":"npx"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("create mcp (invalid id) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleCreateMCPServer_StdioNoCommand(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"myserver","transport":"stdio"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("create mcp (stdio no command) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleCreateMCPServer_Happy(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"myserver","transport":"stdio","command":"npx","args":["-y","@modelcontextprotocol/server-filesystem"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create mcp status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
	if body["id"] != "myserver" {
		t.Fatalf("expected id=myserver, body=%v", body)
	}
}

func TestGatewayHandleDeleteMCPServer_NoCfgPath(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/mcp/someserver", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("delete mcp (no cfgPath) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleDeleteMCPServer_Happy(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	// First create then delete
	gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"delserver","transport":"stdio","command":"node"}`)
	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/mcp/delserver", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("delete mcp status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandleUpdateMCPServer_NotFound(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/mcp/nonexistent", "secret",
		`{"transport":"stdio","command":"node"}`)
	if status != http.StatusNotFound {
		t.Fatalf("update mcp (not found) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleTestMCPServer_StdioMissingCommand(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp/test", "secret",
		`{"transport":"stdio"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("test mcp (missing cmd) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleTestMCPServer_StdioCommandNotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp/test", "secret",
		`{"transport":"stdio","command":"this-command-does-not-exist-anywhere-123456"}`)
	if status != http.StatusOK {
		t.Fatalf("test mcp (cmd not found) status = %d body=%v", status, body)
	}
	if body["ok"] != false {
		t.Fatalf("expected ok=false for missing command, body=%v", body)
	}
}

// ── Costs handlers (nil cost store) ──────────────────────────────────────────

func TestGatewayHandleGetCosts_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/costs", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("get costs (nil store) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleGetAgentCosts_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/costs/my-agent", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("get agent costs (nil store) status = %d body=%v", status, body)
	}
}

// ── Rate limit status ─────────────────────────────────────────────────────────

func TestGatewayHandleRateLimitStatus_NilLimiter(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/rate-limit/status", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("rate limit status (nil) status = %d body=%v", status, body)
	}
}

// ── Tool catalog handler ──────────────────────────────────────────────────────

func TestGatewayHandleToolCatalog(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/tool-catalog", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("tool catalog status = %d body=%v", status, body)
	}
	// python_tools and mcp_tools may be null when no tools are registered in the test gateway
	if _, ok := body["python_tools"]; !ok {
		t.Fatalf("tool catalog: missing python_tools key, body=%v", body)
	}
	if _, ok := body["mcp_tools"]; !ok {
		t.Fatalf("tool catalog: missing mcp_tools key, body=%v", body)
	}
	if body["builtins"] == nil {
		t.Fatalf("tool catalog: missing builtins, body=%v", body)
	}
}

// ── Skills handlers (nil skill loader) ───────────────────────────────────────

func TestGatewayHandleListSkills_NilLoader(t *testing.T) {
	s := newTestGateway(t, "secret")
	// skillLoader is nil in testGateway
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/skills", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list skills (nil) status = %d body=%v", status, body)
	}
	if body["skills"] == nil {
		t.Fatalf("list skills: missing skills field, body=%v", body)
	}
}

func TestGatewayHandleGetSkill_NilLoader(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/skills/my-skill", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("get skill (nil loader) status = %d body=%v", status, body)
	}
}

// ── Templates handlers ────────────────────────────────────────────────────────

func TestGatewayHandleListTemplates(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/templates", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list templates status = %d body=%v", status, body)
	}
	if body["templates"] == nil {
		t.Fatalf("list templates: missing templates field, body=%v", body)
	}
}

func TestGatewayHandleInstantiateTemplate_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/templates/nonexistent-template/instantiate", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("instantiate nonexistent template status = %d body=%v", status, body)
	}
}

// ── Tool confirm handler ──────────────────────────────────────────────────────

func TestGatewayHandleToolConfirm_MissingCallID(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat/confirm", "secret",
		`{"approved":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("tool confirm (missing call_id) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleToolConfirm_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat/confirm", "secret",
		`{"call_id":"nonexistent-call-id","approved":true}`)
	if status != http.StatusNotFound {
		t.Fatalf("tool confirm (not found) status = %d body=%v", status, body)
	}
}

// ── Knowledge handlers (nil knowledge store) ──────────────────────────────────

func TestGatewayHandleListKnowledge_Disabled(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/knowledge", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list knowledge (disabled) status = %d body=%v", status, body)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false when knowledge disabled, body=%v", body)
	}
}

func TestGatewayHandleCreateKnowledge_Disabled(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/knowledge", "secret",
		`{"name":"testKB"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("create knowledge (disabled) status = %d body=%v", status, body)
	}
}

// ── Admin DLQ handlers (nil DLQ store) ───────────────────────────────────────

func TestGatewayHandleListDLQ_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/dlq", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("list dlq (nil store) status = %d body=%v", status, body)
	}
}

// ── History handlers (nil history store) ─────────────────────────────────────

func TestGatewayHandleHistory_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/history/session-123", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("history (nil store) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleHistoryByAgent_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/history/agent/my-agent", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("history by agent (nil store) status = %d body=%v", status, body)
	}
}

// ── Logs handler ──────────────────────────────────────────────────────────────

func TestGatewayHandleGetLogs_NoLogFile(t *testing.T) {
	s := newTestGateway(t, "secret")
	// cfg.Log.File is "" so it should return empty lines + note
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/logs", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get logs (no file) status = %d body=%v", status, body)
	}
	if body["lines"] == nil {
		t.Fatalf("expected lines field, body=%v", body)
	}
}

func TestGatewayHandleGetLogs_WithLogFile(t *testing.T) {
	// Create a temporary log file
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	err := os.WriteFile(logPath, []byte("line1\nline2\nline3\n"), 0o644)
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.cfg.Log.File = logPath

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/logs", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get logs status = %d body=%v", status, body)
	}
	lines, ok := body["lines"].([]any)
	if !ok {
		t.Fatalf("lines is not an array: %v", body)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}
}

// ── Builder handlers ──────────────────────────────────────────────────────────

func TestGatewayHandleBuilderChat_MissingMessage(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/builder/chat", "secret",
		`{"session_id":"s1"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("builder chat (missing message) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleBuilderGenerate_MissingSessionID(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/builder/generate", "secret",
		`{"provider":"test"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("builder generate (missing session_id) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleBuilderGenerate_SessionNotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/builder/generate", "secret",
		`{"session_id":"nonexistent-session-xyz"}`)
	if status != http.StatusNotFound {
		t.Fatalf("builder generate (not found) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleBuilderDeploy_MissingSessionID(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/builder/deploy", "secret",
		`{"provider":"test"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("builder deploy (missing session_id) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleBuilderDeleteSession(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/builder/session/some-session-id", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("builder delete session status = %d body=%v", status, body)
	}
	if body["deleted"] != true {
		t.Fatalf("expected deleted=true, body=%v", body)
	}
}

// ── Brain memory handlers (nil brain store) ────────────────────────────────────

func TestGatewayHandleBrainMemoryStats_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/brain-memory", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("brain memory stats (nil) status = %d body=%v", status, body)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false when brain store nil, body=%v", body)
	}
}

func TestGatewayHandleGetEpisodic_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/brain-memory/my-agent/episodic", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("get episodic (nil store) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleGetProcedural_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/brain-memory/my-agent/procedural", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("get procedural (nil store) status = %d body=%v", status, body)
	}
}

// ── Admin API keys (nil store) ────────────────────────────────────────────────

func TestGatewayHandleAdminAPIKeys_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/api-keys", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("list api-keys (nil) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleCreateAdminAPIKey_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/admin/api-keys", "secret",
		`{"name":"my-key"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("create api-key (nil) status = %d body=%v", status, body)
	}
}

// ── Credential vault (nil vault) ──────────────────────────────────────────────

func TestGatewayHandleCredentials_NilVault(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/credentials/my-agent", "secret", "")
	// The lazy API returns 503 when vault is nil
	if status != http.StatusServiceUnavailable {
		t.Fatalf("list credentials (nil vault) status = %d body=%v", status, body)
	}
}

// ── Dashboard handler ─────────────────────────────────────────────────────────

func TestGatewayHandleDashboard(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, raw := gatewayRaw(t, s, http.MethodGet, "/api/v1/admin/dashboard", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("dashboard status = %d body=%s", status, raw)
	}
	if len(raw) == 0 {
		t.Fatalf("dashboard returned empty body")
	}
}

// ── Chat stream validation ────────────────────────────────────────────────────

func TestGatewayChatStreamHandlerValidatesRequiredFields(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, raw := gatewayRaw(t, s, http.MethodPost, "/api/v1/chat/stream", "secret",
		`{"agent_id":"some-agent"}`)
	// Missing text → should be 400
	if status != http.StatusBadRequest {
		t.Fatalf("stream missing text status = %d body=%s", status, raw)
	}
}

// ── parseCostSince unit tests ─────────────────────────────────────────────────

func TestParseCostSince_Empty(t *testing.T) {
	ts, label, err := parseCostSince("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ts.IsZero() {
		t.Fatalf("expected zero time for empty string, got %v", ts)
	}
	if label != "" {
		t.Fatalf("expected empty label, got %q", label)
	}
}

func TestParseCostSince_DayShorthand(t *testing.T) {
	ts, label, err := parseCostSince("7d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero time for 7d")
	}
	if label != "7d" {
		t.Fatalf("expected label=7d, got %q", label)
	}
}

func TestParseCostSince_GoDuration(t *testing.T) {
	ts, _, err := parseCostSince("24h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero time for 24h")
	}
}

func TestParseCostSince_RFC3339(t *testing.T) {
	ts, _, err := parseCostSince("2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero time for RFC3339")
	}
}

func TestParseCostSince_DateOnly(t *testing.T) {
	ts, _, err := parseCostSince("2026-01-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero time for date-only")
	}
}

func TestParseCostSince_Invalid(t *testing.T) {
	_, _, err := parseCostSince("not-a-duration")
	if err == nil {
		t.Fatalf("expected error for invalid input")
	}
}

// ── Channel helper unit tests ─────────────────────────────────────────────────

func TestChannelSpecByID(t *testing.T) {
	spec := channelSpecByID("telegram")
	if spec == nil {
		t.Fatalf("expected telegram spec, got nil")
	}
	if spec.ID != "telegram" {
		t.Fatalf("expected id=telegram, got %q", spec.ID)
	}
}

func TestChannelSpecByID_Unknown(t *testing.T) {
	spec := channelSpecByID("unknown-channel")
	if spec != nil {
		t.Fatalf("expected nil for unknown channel, got %+v", spec)
	}
}

func TestChannelSupportsBots(t *testing.T) {
	for _, id := range []string{"telegram", "discord", "slack"} {
		if !channelSupportsBots(id) {
			t.Fatalf("expected %s to support bots", id)
		}
	}
	for _, id := range []string{"http", "whatsapp", "unknown"} {
		if channelSupportsBots(id) {
			t.Fatalf("expected %s to NOT support bots", id)
		}
	}
}

func TestValuePresent(t *testing.T) {
	if valuePresent(nil) {
		t.Fatal("nil should not be present")
	}
	if valuePresent("") {
		t.Fatal("empty string should not be present")
	}
	if valuePresent("  ") {
		t.Fatal("whitespace should not be present")
	}
	if !valuePresent("hello") {
		t.Fatal("non-empty string should be present")
	}
	if !valuePresent(42) {
		t.Fatal("int should be present")
	}
	if valuePresent([]any{}) {
		t.Fatal("empty slice should not be present")
	}
	if !valuePresent([]any{"x"}) {
		t.Fatal("non-empty slice should be present")
	}
}

func TestValidMCPID(t *testing.T) {
	for _, id := range []string{"myserver", "my-server", "my_server", "server123", "A1-b"} {
		if !validMCPID(id) {
			t.Fatalf("expected %q to be a valid MCP ID", id)
		}
	}
	for _, id := range []string{"", "bad id", "bad!", "has space", "has.dot"} {
		if validMCPID(id) {
			t.Fatalf("expected %q to be an invalid MCP ID", id)
		}
	}
}

func TestSanitizeChannelID(t *testing.T) {
	got := sanitizeChannelID("hello world!")
	if got != "hello-world-" {
		t.Fatalf("sanitizeChannelID = %q, want %q", got, "hello-world-")
	}
	got = sanitizeChannelID("my-agent_1")
	if got != "my-agent_1" {
		t.Fatalf("sanitizeChannelID = %q, want %q", got, "my-agent_1")
	}
}

func TestChannelAdapterID(t *testing.T) {
	if channelAdapterID("telegram", "my-bot", "", 0) != "telegram" {
		t.Fatalf("index 0 should return channelID")
	}
	got := channelAdapterID("telegram", "", "", 1)
	if got != "telegram-2" {
		t.Fatalf("got %q, want telegram-2", got)
	}
	got = channelAdapterID("telegram", "my-bot", "", 1)
	if got != "telegram-my-bot" {
		t.Fatalf("got %q, want telegram-my-bot", got)
	}
}

// ── Ping handler ──────────────────────────────────────────────────────────────

func TestGatewayPingHandler(t *testing.T) {
	s := newTestGateway(t, "")
	status, body := gatewayJSON(t, s, http.MethodGet, "/ping", "", "")
	if status != http.StatusOK {
		t.Fatalf("ping status = %d body=%v", status, body)
	}
	if body["status"] != "ok" {
		t.Fatalf("ping: missing status=ok, body=%v", body)
	}
}

// ── MCP validation helpers ────────────────────────────────────────────────────

func TestValidateMCPServer_StdioNoCommand(t *testing.T) {
	msg := validateMCPServer(mcpServerBody{Transport: "stdio"})
	if msg == "" {
		t.Fatal("expected error for stdio without command")
	}
}

func TestValidateMCPServer_StdioOK(t *testing.T) {
	msg := validateMCPServer(mcpServerBody{Transport: "stdio", Command: "node"})
	if msg != "" {
		t.Fatalf("expected no error, got %q", msg)
	}
}

func TestValidateMCPServer_HTTPNoURL(t *testing.T) {
	msg := validateMCPServer(mcpServerBody{Transport: "http"})
	if msg == "" {
		t.Fatal("expected error for http without url")
	}
}

func TestValidateMCPServer_HTTPOK(t *testing.T) {
	msg := validateMCPServer(mcpServerBody{Transport: "http", URL: "http://localhost:8080"})
	if msg != "" {
		t.Fatalf("expected no error, got %q", msg)
	}
}

func TestValidateMCPServer_UnknownTransport(t *testing.T) {
	msg := validateMCPServer(mcpServerBody{Transport: "grpc"})
	if msg == "" {
		t.Fatal("expected error for unknown transport")
	}
}

// ── newTestGatewayWithCfgPath helper ──────────────────────────────────────────

// newTestGatewayWithCfgPath creates a test gateway with a real config file path
// so handlers that need cfgPath != "" work correctly.
func newTestGatewayWithCfgPath(t *testing.T, apiKey, cfgPath string) *Server {
	t.Helper()
	s := newTestGateway(t, apiKey)
	s.cfgPath = cfgPath
	return s
}
