package telegram

import "strings"
import "testing"

func TestTelegramErrorDetailPrefersDescription(t *testing.T) {
	got := telegramErrorDetail(strings.NewReader(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	if got != "Bad Request: chat not found" {
		t.Fatalf("detail = %q, want description", got)
	}
}

func TestTelegramErrorDetailFallsBackToRawBody(t *testing.T) {
	got := telegramErrorDetail(strings.NewReader(`plain failure`))
	if got != "plain failure" {
		t.Fatalf("detail = %q, want raw body", got)
	}
}
