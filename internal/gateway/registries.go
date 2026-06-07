// registries.go — Story E26: review URLs as skill sources and manage the
// `registries:` config block from the GUI.
//
//	GET  /api/v1/registries        — configured sources (auth headers redacted)
//	POST /api/v1/registries/probe  — {url} → pkgregistry.ProbeReport
//	POST /api/v1/registries        — add a reviewed source to config.yaml
//
// Probe targets are operator-supplied (config-write RBAC) and restricted to
// http/https by pkgregistry.Probe.
package gateway

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/pkgregistry"
	"github.com/soulacy/soulacy/sdk/registry"
)

// registryView is the redacted listing shape.
type registryView struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	BaseURL    string `json:"base_url,omitempty"`
	Priority   int    `json:"priority"`
	HasAuth    bool   `json:"has_auth"`
	SigningKey string `json:"signing_key,omitempty"`
}

// rawRegistries reads the registries block from config.yaml as raw maps.
func (s *Server) rawRegistries() ([]map[string]any, error) {
	current, err := readRawConfig(s.cfgPath)
	if err != nil {
		return nil, err
	}
	raw, _ := current["registries"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Server) handleListRegistries(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.JSON(fiber.Map{"registries": []registryView{}, "count": 0})
	}
	entries, err := s.rawRegistries()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	views := make([]registryView, 0, len(entries))
	for _, m := range entries {
		v := registryView{
			ID:       strAt(m, "id"),
			Type:     strAt(m, "type"),
			BaseURL:  strAt(m, "base_url"),
			Priority: intAt(m, "priority"),
		}
		if v.Type == "" {
			v.Type = "http"
		}
		if ah, ok := m["auth_headers"].(map[string]any); ok && len(ah) > 0 {
			v.HasAuth = true
		}
		if sk := strAt(m, "signing_key"); sk != "" {
			v.SigningKey = sk // public key — not a secret
		}
		views = append(views, v)
	}
	return c.JSON(fiber.Map{"registries": views, "count": len(views)})
}

func (s *Server) handleProbeRegistry(c *fiber.Ctx) error {
	var body struct {
		URL string `json:"url"`
	}
	if err := c.BodyParser(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "body must be {\"url\": \"…\"}"})
	}
	ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
	defer cancel()
	rep, err := pkgregistry.Probe(ctx, body.URL)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(rep)
}

func (s *Server) handleAddRegistry(c *fiber.Ctx) error {
	if s.cfgPath == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "config file path unknown — restart with SOULACY_CONFIG_PATH set to enable writes",
		})
	}
	var body struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		BaseURL    string `json:"base_url"`
		Priority   int    `json:"priority"`
		SigningKey string `json:"signing_key"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body: " + err.Error()})
	}
	if body.Type == "" {
		body.Type = "http"
	}
	if !pkgRegistryTypeKnown(body.Type) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "unknown registry type \"" + body.Type + "\" (registered: " + strings.Join(registry.PkgRegistries(), ", ") + ")",
		})
	}
	if body.Type != "git" && strings.TrimSpace(body.BaseURL) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "base_url is required for type " + body.Type})
	}
	if body.ID == "" {
		body.ID = body.Type
	}

	current, err := readRawConfig(s.cfgPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	raw, _ := current["registries"].([]any)
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok && strAt(m, "id") == body.ID {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error": "a registry with id \"" + body.ID + "\" already exists",
			})
		}
	}
	entry := map[string]any{"id": body.ID, "type": body.Type}
	if body.BaseURL != "" {
		entry["base_url"] = strings.TrimRight(body.BaseURL, "/")
	}
	if body.Priority != 0 {
		entry["priority"] = body.Priority
	}
	if body.SigningKey != "" {
		entry["signing_key"] = body.SigningKey
	}
	current["registries"] = append(raw, entry)
	if err := writeRawConfig(s.cfgPath, current); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	s.log.Info("registry source added via API",
		zap.String("id", body.ID), zap.String("type", body.Type), zap.String("base_url", body.BaseURL))
	return c.JSON(fiber.Map{
		"ok":      true,
		"message": "Source \"" + body.ID + "\" saved. `sy skill install` picks it up immediately; restart the gateway for GUI installs.",
	})
}

func pkgRegistryTypeKnown(t string) bool {
	for _, n := range registry.PkgRegistries() {
		if n == t {
			return true
		}
	}
	return false
}

func strAt(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func intAt(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}
