// deliverydoctor.go — plain-language diagnosis of channel delivery failures.
//
// When a message doesn't reach Telegram/Slack/Discord, the underlying adapter
// returns a raw provider error ("not_in_channel", "Bad Request: chat not
// found", "401 Unauthorized", "Unknown Channel (code 10003)", ...). Those are
// accurate but opaque to a non-expert operator. DiagnoseDelivery turns the
// precondition state plus the raw error into a stable category, a plain-English
// reason, and a concrete fix — the "delivery doctor" behind the Diagnose button
// on each channel mapping.
//
// This file is deliberately pure (no I/O, no gateway types) so it is trivially
// unit-tested and reused by both the API handler and the CLI doctor.

package channels

import "strings"

// DeliveryCategory is a stable, machine-readable classification of a delivery
// attempt. UIs can branch on it; humans read Reason/Fix.
type DeliveryCategory string

const (
	DeliveryOK                DeliveryCategory = "ok"
	DeliveryMissingTo         DeliveryCategory = "missing_destination"
	DeliveryAdapterDown       DeliveryCategory = "adapter_unavailable"
	DeliveryAdapterDisconnect DeliveryCategory = "adapter_disconnected"
	DeliveryBadToken          DeliveryCategory = "bad_token"
	DeliveryMissingScope      DeliveryCategory = "missing_scope"
	DeliveryBotNotInvited     DeliveryCategory = "bot_not_invited"
	DeliveryInvalidDest       DeliveryCategory = "invalid_destination"
	DeliveryForbidden         DeliveryCategory = "forbidden"
	DeliveryNetwork           DeliveryCategory = "network"
	DeliveryUnknown           DeliveryCategory = "unknown"
)

// Diagnosis is the result of a delivery check.
type Diagnosis struct {
	OK       bool             `json:"ok"`
	Category DeliveryCategory `json:"category"`
	Reason   string           `json:"reason"`           // plain-language: what happened
	Fix      string           `json:"fix,omitempty"`    // plain-language: what to do about it
	Detail   string           `json:"detail,omitempty"` // raw provider error, for the curious
}

// DiagnoseDelivery composes the precondition state and any send error into a
// single Diagnosis. Callers pass:
//   - adapterID: the resolved adapter/mapping id (e.g. "telegram-support")
//   - to:        the destination (chat/channel id); "" means none set
//   - registered: whether the adapter is registered/running
//   - connected:  whether the adapter reports itself connected (Status)
//   - sendErr:    the error from attempting Send (nil if the send succeeded, or
//     nil if the caller only wanted the precondition checks)
//
// Precondition problems (no destination, adapter down/disconnected) are reported
// without needing an actual send error, so a mapping can be diagnosed even when
// a live send isn't attempted.
func DiagnoseDelivery(adapterID, to string, registered, connected bool, sendErr error) Diagnosis {
	if strings.TrimSpace(to) == "" && sendErr == nil {
		return Diagnosis{
			Category: DeliveryMissingTo,
			Reason:   "No destination is set for this mapping, so there is nowhere to deliver the message.",
			Fix:      "Set a destination (chat/channel ID) on the mapping, or configure a default outbound destination for scheduled sends.",
		}
	}
	if !registered {
		return Diagnosis{
			Category: DeliveryAdapterDown,
			Reason:   "The adapter for this channel is not running. It may be disabled, or the config changed since the gateway last started.",
			Fix:      "Enable the channel and restart the gateway (`sy daemon stop && sy daemon start`), then diagnose again.",
		}
	}
	if sendErr == nil {
		if !connected {
			return Diagnosis{
				Category: DeliveryAdapterDisconnect,
				Reason:   "The adapter is loaded but not currently connected to the provider.",
				Fix:      "Check the bot token and network access; the adapter should reconnect automatically once reachable.",
			}
		}
		return Diagnosis{OK: true, Category: DeliveryOK, Reason: "Delivery succeeded."}
	}

	d := ClassifyDeliveryError(sendErr.Error())
	d.Detail = sendErr.Error()
	return d
}

// ClassifyDeliveryError maps a raw adapter error string to a plain-language
// category/reason/fix. It matches on the stable substrings the Telegram, Slack,
// and Discord adapters preserve from provider responses. Ordering matters:
// more specific signals (missing scope, not-invited) are checked before the
// broad auth/not-found buckets.
func ClassifyDeliveryError(raw string) Diagnosis {
	s := strings.ToLower(raw)

	contains := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return true
			}
		}
		return false
	}

	switch {
	// Missing OAuth scope (Slack) — check before generic auth.
	case contains("missing_scope", "missing scope", "not_allowed_token_type"):
		return Diagnosis{
			Category: DeliveryMissingScope,
			Reason:   "The bot token is valid but is missing a permission (scope) needed to post here.",
			Fix:      "Add the `chat:write` scope (and `chat:write.public` for channels the bot isn't in) to the Slack app, then reinstall the app to your workspace.",
		}

	// Bot not a member of the channel/group.
	case contains("not_in_channel", "not in channel", "not a member", "is not a member", "user_not_in_channel"):
		return Diagnosis{
			Category: DeliveryBotNotInvited,
			Reason:   "The bot has not been added to this channel, so it cannot post there.",
			Fix:      "Invite the bot to the channel/group (e.g. `/invite @yourbot` in Slack, or add it to the Telegram group / Discord channel), then try again.",
		}

	// Bad / rotated / missing token.
	case contains("unauthorized", "invalid_auth", "invalid auth", "invalid token", "token_revoked", "account_inactive", "401"):
		return Diagnosis{
			Category: DeliveryBadToken,
			Reason:   "The provider rejected the bot token — it is missing, wrong, or has been revoked.",
			Fix:      "Update the bot token in the vault with a current, valid token, then restart the gateway.",
		}

	// Destination doesn't exist (or bot can't see it). Telegram "chat not found",
	// Slack "channel_not_found", Discord "Unknown Channel".
	case contains("chat not found", "channel_not_found", "unknown channel", "unknown_channel", "chat_not_found", "peer_id_invalid", "user not found"):
		return Diagnosis{
			Category: DeliveryInvalidDest,
			Reason:   "The destination ID is invalid, or the bot cannot see that chat/channel.",
			Fix:      "Double-check the chat/channel ID. For Telegram, the bot must have received at least one message in that chat first; for Slack/Discord, the bot must be able to access the channel.",
		}

	// Forbidden / blocked / lacks permission (but token itself is valid).
	case contains("forbidden", "403", "missing permissions", "missing access", "bot was blocked", "cannot initiate conversation", "not enough rights"):
		return Diagnosis{
			Category: DeliveryForbidden,
			Reason:   "The bot is authenticated but is not permitted to post to this destination (blocked, or lacking the right permissions).",
			Fix:      "Grant the bot permission to post in this channel, or ensure the user hasn't blocked the bot.",
		}

	// Generic bad request — usually a malformed chat/channel id on Telegram.
	case contains("bad request", "400"):
		return Diagnosis{
			Category: DeliveryInvalidDest,
			Reason:   "The provider rejected the request — most often a malformed or wrong destination ID.",
			Fix:      "Verify the destination ID format for this channel and try again.",
		}

	// Transport-level failure.
	case contains("timeout", "deadline exceeded", "connection refused", "no such host", "dial tcp", "eof", "network is unreachable"):
		return Diagnosis{
			Category: DeliveryNetwork,
			Reason:   "The message could not be sent because the provider was unreachable (a network or connectivity problem).",
			Fix:      "Check outbound network access to the provider and retry; if it persists, verify the provider isn't down.",
		}

	default:
		return Diagnosis{
			Category: DeliveryUnknown,
			Reason:   "Delivery failed for a reason the doctor didn't recognize. The raw provider error is included below.",
			Fix:      "Check the raw error detail and the channel setup guide for this provider.",
		}
	}
}
