package telegram

import (
	"fmt"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Registry self-registration (Story E10). The host resolves the "telegram"
// config entry through registry.NewChannel; this factory replaces the old
// hardcoded constructor call in cmd/soulacy.
//
// Config keys: id (default "telegram"), token (required), agent_id,
// allowed_user_ids, trigger_phrase, ignore_groups (default true),
// allowed_chat_ids.
func init() {
	registry.MustRegisterChannel("telegram", func(cfg map[string]any) (channel.Adapter, error) {
		token := cfgmap.Str(cfg, "token", "")
		if token == "" {
			return nil, fmt.Errorf("telegram: config key %q is required", "token")
		}
		return NewWithIDAndActivation(
			cfgmap.Str(cfg, "id", "telegram"),
			token,
			cfgmap.Str(cfg, "agent_id", ""),
			channels.ParseInt64List(cfg, "allowed_user_ids"),
			channels.ActivationFromConfig(cfg, true),
		), nil
	})
}
