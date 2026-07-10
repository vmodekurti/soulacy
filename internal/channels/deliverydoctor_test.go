package channels

import (
	"errors"
	"testing"
)

func TestClassifyDeliveryError(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want DeliveryCategory
	}{
		// Telegram (from "telegram: send: API returned status %d: %s")
		{"telegram-chat-not-found", "telegram: send: API returned status 400: Bad Request: chat not found", DeliveryInvalidDest},
		{"telegram-unauthorized", "telegram: send: API returned status 401: Unauthorized", DeliveryBadToken},
		{"telegram-bot-blocked", "telegram: send: API returned status 403: Forbidden: bot was blocked by the user", DeliveryForbidden},
		// Slack (raw error codes)
		{"slack-not-in-channel", "slack: send: not_in_channel", DeliveryBotNotInvited},
		{"slack-missing-scope", "slack: send: missing_scope", DeliveryMissingScope},
		{"slack-channel-not-found", "slack: send: channel_not_found", DeliveryInvalidDest},
		{"slack-invalid-auth", "slack: send: invalid_auth", DeliveryBadToken},
		// Discord (from discordErrorDetail: "<message> (code <n>)")
		{"discord-unknown-channel", "discord: send: API returned status 404: Unknown Channel (code 10003)", DeliveryInvalidDest},
		{"discord-unauthorized", "discord: send: API returned status 401: 401: Unauthorized", DeliveryBadToken},
		{"discord-missing-access", "discord: send: API returned status 403: Missing Access (code 50001)", DeliveryForbidden},
		// Transport
		{"network-timeout", "telegram: send: context deadline exceeded", DeliveryNetwork},
		{"network-refused", "slack: send: dial tcp: connection refused", DeliveryNetwork},
		// Unknown
		{"weird", "slack: send: something_totally_new", DeliveryUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyDeliveryError(tc.raw)
			if got.Category != tc.want {
				t.Errorf("ClassifyDeliveryError(%q) category = %q, want %q", tc.raw, got.Category, tc.want)
			}
			if got.Reason == "" {
				t.Errorf("empty Reason for %q", tc.raw)
			}
			if got.OK {
				t.Errorf("classified error should never be OK for %q", tc.raw)
			}
		})
	}
}

func TestDiagnoseDeliveryPreconditions(t *testing.T) {
	// No destination.
	if d := DiagnoseDelivery("telegram", "", true, true, nil); d.Category != DeliveryMissingTo {
		t.Errorf("missing to: got %q", d.Category)
	}
	// Adapter not registered.
	if d := DiagnoseDelivery("telegram", "123", false, false, nil); d.Category != DeliveryAdapterDown {
		t.Errorf("adapter down: got %q", d.Category)
	}
	// Registered but disconnected.
	if d := DiagnoseDelivery("telegram", "123", true, false, nil); d.Category != DeliveryAdapterDisconnect {
		t.Errorf("disconnected: got %q", d.Category)
	}
	// All good.
	d := DiagnoseDelivery("telegram", "123", true, true, nil)
	if !d.OK || d.Category != DeliveryOK {
		t.Errorf("ok: got OK=%v cat=%q", d.OK, d.Category)
	}
	// Send error is classified and detail preserved.
	d = DiagnoseDelivery("slack", "C123", true, true, errors.New("slack: send: missing_scope"))
	if d.Category != DeliveryMissingScope {
		t.Errorf("send err: got %q", d.Category)
	}
	if d.Detail == "" {
		t.Errorf("expected raw detail to be preserved")
	}
}
