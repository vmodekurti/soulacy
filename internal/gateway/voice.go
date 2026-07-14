// voice.go — realtime voice control-plane routes (Story 11, design in
// docs/VOICE_SPIKE.md). The gateway only mints ephemeral client keys and
// reports availability; audio flows browser↔provider directly.
package gateway

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/voice"
)

// VoiceMinter is the control-plane seam for realtime voice providers.
// internal/voice.OpenAIMinter satisfies it; tests use fakes.
type VoiceMinter interface {
	Provider() string
	Ready() (bool, string)
	Mint(ctx context.Context) (voice.EphemeralKey, error)
}

type voiceReadiness struct {
	Status   string `json:"status"`
	Score    int    `json:"score"`
	Enabled  bool   `json:"enabled"`
	Ready    bool   `json:"ready"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Detail   string `json:"detail"`
	Next     string `json:"next,omitempty"`
}

// SetVoiceMinter wires the realtime voice provider. Call after New(),
// before Start(). When nil the voice routes degrade gracefully
// (status: unavailable; ephemeral: 503).
func (s *Server) SetVoiceMinter(m VoiceMinter) {
	s.pluginMu.Lock()
	defer s.pluginMu.Unlock()
	s.voiceMinter = m
}

func (s *Server) voiceMinterRef() VoiceMinter {
	s.pluginMu.RLock()
	defer s.pluginMu.RUnlock()
	return s.voiceMinter
}

// handleVoiceStatus reports realtime-voice availability for the Chat panel.
//
//	GET /api/v1/voice/status
func (s *Server) handleVoiceStatus(c *fiber.Ctx) error {
	m := s.voiceMinterRef()
	if m == nil {
		return c.JSON(fiber.Map{
			"available": false,
			"detail":    "no realtime voice provider configured (set voice.provider and an OpenAI API key in config.yaml)",
		})
	}
	ready, detail := m.Ready()
	out := fiber.Map{"available": ready, "provider": m.Provider()}
	if detail != "" {
		out["detail"] = detail
	}
	if mm, ok := m.(interface{ Model() string }); ok {
		out["model"] = mm.Model()
	}
	return c.JSON(out)
}

func (s *Server) voiceReadiness() voiceReadiness {
	provider := ""
	if s != nil && s.cfg != nil {
		provider = s.cfg.Voice.Provider
	}
	m := s.voiceMinterRef()
	if m == nil {
		if provider == "" {
			return voiceReadiness{
				Status:  "warn",
				Score:   55,
				Enabled: false,
				Ready:   false,
				Detail:  "Realtime voice is disabled; text chat and channel agents still work.",
				Next:    "Set voice.provider: openai and configure an OpenAI API key when voice is part of launch scope.",
			}
		}
		return voiceReadiness{
			Status:   "fail",
			Score:    35,
			Enabled:  true,
			Ready:    false,
			Provider: provider,
			Detail:   "Realtime voice is configured with an unsupported or unwired provider.",
			Next:     "Use voice.provider: openai for the current voice MVP, or install a compatible voice sidecar.",
		}
	}
	ready, detail := m.Ready()
	out := voiceReadiness{
		Enabled:  true,
		Ready:    ready,
		Provider: m.Provider(),
		Detail:   "Realtime voice control plane is configured.",
	}
	if mm, ok := m.(interface{ Model() string }); ok {
		out.Model = mm.Model()
	}
	if ready {
		out.Status = "ok"
		out.Score = 86
		out.Detail = "Chat can mint ephemeral realtime voice sessions; browser audio connects directly to the provider."
		out.Next = "Run one credential-backed voice session before launch if voice is part of the product promise."
		return out
	}
	out.Status = "warn"
	out.Score = 60
	out.Detail = detail
	out.Next = "Fix the voice provider credential, then verify /api/v1/voice/status reports available."
	return out
}

// handleVoiceEphemeral mints a short-lived client key for the browser's
// direct WebRTC connection to the provider. The user's real API key never
// leaves the host.
//
//	POST /api/v1/voice/ephemeral
func (s *Server) handleVoiceEphemeral(c *fiber.Ctx) error {
	m := s.voiceMinterRef()
	if m == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "no realtime voice provider configured",
		})
	}
	if ready, detail := m.Ready(); !ready {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "voice provider not ready: " + detail,
		})
	}
	key, err := m.Mint(c.Context())
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	return c.JSON(fiber.Map{
		"key":        key.Key,
		"expires_at": key.ExpiresAt,
		"model":      key.Model,
		"provider":   key.Provider,
	})
}
