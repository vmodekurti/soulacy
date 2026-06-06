package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/voice"
)

type fakeMinter struct {
	ready  bool
	detail string
	key    voice.EphemeralKey
	err    error
}

func (f *fakeMinter) Provider() string      { return "openai" }
func (f *fakeMinter) Ready() (bool, string) { return f.ready, f.detail }
func (f *fakeMinter) Mint(context.Context) (voice.EphemeralKey, error) {
	return f.key, f.err
}

func TestVoiceStatus_NoMinter_Fallback(t *testing.T) {
	s := newTestGateway(t, "")
	status, body := gatewayJSON(t, s, "GET", "/api/v1/voice/status", "", "")
	if status != 200 {
		t.Fatalf("status = %d (fallback must be a clean 200)", status)
	}
	if body["available"] != false {
		t.Fatalf("available = %v, want false", body["available"])
	}
	if body["detail"] == "" {
		t.Fatal("fallback must explain how to enable voice")
	}
}

func TestVoiceStatus_MinterNotReady(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: false, detail: "no API key configured"})
	status, body := gatewayJSON(t, s, "GET", "/api/v1/voice/status", "", "")
	if status != 200 || body["available"] != false {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if body["detail"] != "no API key configured" {
		t.Fatalf("detail = %v", body["detail"])
	}
}

func TestVoiceStatus_Ready(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: true})
	status, body := gatewayJSON(t, s, "GET", "/api/v1/voice/status", "", "")
	if status != 200 || body["available"] != true || body["provider"] != "openai" {
		t.Fatalf("status=%d body=%v", status, body)
	}
}

func TestVoiceEphemeral_NoMinter503(t *testing.T) {
	s := newTestGateway(t, "")
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", "", "")
	if status != 503 {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestVoiceEphemeral_NotReady503(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: false, detail: "no key"})
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", "", "")
	if status != 503 {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestVoiceEphemeral_Success(t *testing.T) {
	s := newTestGateway(t, "")
	exp := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	s.SetVoiceMinter(&fakeMinter{ready: true, key: voice.EphemeralKey{
		Key: "ek_abc", ExpiresAt: exp, Model: "gpt-realtime-mini", Provider: "openai",
	}})
	status, body := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", "", "")
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["key"] != "ek_abc" || body["model"] != "gpt-realtime-mini" || body["provider"] != "openai" {
		t.Fatalf("body = %v", body)
	}
}

func TestVoiceEphemeral_ProviderError502(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: true, err: errors.New("upstream down")})
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", "", "")
	if status != 502 {
		t.Fatalf("status = %d, want 502", status)
	}
}

func TestVoiceEphemeral_PluginTokenDenied(t *testing.T) {
	// Voice routes are user-facing; plugin tokens stay outside (default-deny).
	s, _ := pluginGateway(t, nil)
	s.SetVoiceMinter(&fakeMinter{ready: true})
	tok := issueToken(t, s)
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", tok, "")
	if status != 403 {
		t.Fatalf("status = %d, want 403", status)
	}
}
