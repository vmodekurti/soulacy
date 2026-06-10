package secrets

import (
	"context"
	"os"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
)

// Migrate moves any plaintext secrets found in cfg into the vault, then blanks
// those values both in the on-disk config file (cfgPath, if set) and in the
// in-memory cfg. It is safe to call on every startup: it is a no-op when no
// plaintext secrets are present. Returns the number of secrets migrated.
//
// After Migrate, call Overlay to repopulate the in-memory cfg from the vault so
// the running process still has working values.
func (m *Manager) Migrate(ctx context.Context, cfg *config.Config, cfgPath string) (int, error) {
	if !m.Enabled() {
		return 0, ErrNoVault
	}
	vals := secretValuesInConfig(cfg)
	if len(vals) == 0 {
		return 0, nil
	}

	migrated := 0
	for name, val := range vals {
		// Don't clobber a vault value the operator already set explicitly.
		if _, exists := m.Get(ctx, name); exists {
			continue
		}
		if err := m.Set(ctx, name, val); err != nil {
			return migrated, err
		}
		migrated++
	}

	// Blank the values in the file (preserve keys, indentation, comments).
	if cfgPath != "" {
		if body, err := os.ReadFile(cfgPath); err == nil {
			if red, n := RedactSecretLines(string(body)); n > 0 {
				_ = os.WriteFile(cfgPath, []byte(red), 0o600)
			}
		}
	}

	// Blank in memory; Overlay restores live values from the vault.
	blankConfigSecrets(cfg)
	return migrated, nil
}

// blankConfigSecrets clears secret string fields in the in-memory config.
func blankConfigSecrets(cfg *config.Config) {
	if cfg == nil {
		return
	}
	for id, pc := range cfg.LLM.Providers {
		if pc.APIKey != "" {
			pc.APIKey = ""
			cfg.LLM.Providers[id] = pc
		}
	}
	for _, settings := range cfg.Channels {
		for _, k := range channelTokenKeys {
			if _, ok := settings[k]; ok {
				settings[k] = ""
			}
		}
	}
	cfg.Server.APIKey = ""
}

// RedactSecretLines blanks the value of any YAML line whose key is a known
// secret key (api_key, bot_token, app_token, token, signing_secret, …),
// preserving indentation, the key, and any trailing comment. Lines that are
// already empty or commented out are left untouched. Returns the redacted body
// and the number of lines changed.
func RedactSecretLines(body string) (string, int) {
	secretKeys := map[string]bool{}
	for _, k := range channelTokenKeys {
		secretKeys[k] = true
	}

	lines := strings.Split(body, "\n")
	changed := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		if !secretKeys[key] {
			continue
		}
		rest := line[colon+1:]

		// Separate a trailing comment (only if preceded by whitespace) so we
		// keep it. A '#' inside a quoted value is unusual for keys; keep simple.
		comment := ""
		if hash := strings.Index(rest, " #"); hash >= 0 {
			comment = rest[hash:]
			rest = rest[:hash]
		}
		valueTrimmed := strings.TrimSpace(rest)
		if valueTrimmed == "" || valueTrimmed == `""` || valueTrimmed == "''" {
			continue // already blank
		}

		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + key + `: ""` + comment
		changed++
	}
	if changed == 0 {
		return body, 0
	}
	return strings.Join(lines, "\n"), changed
}
