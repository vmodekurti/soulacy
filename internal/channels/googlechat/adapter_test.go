package googlechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/sdk/registry"
)

func TestGoogleChatSendPostsTextPayload(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("content-type = %q, want json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a, err := New("google_chat", srv.URL, "[Soulacy]", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	msg := message.Message{
		Channel: "google_chat",
		Parts:   message.Text("hello\n```chart\n{}\n```\nworld"),
	}
	if err := a.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if got["text"] != "[Soulacy]\n\nhello\n\nworld" {
		t.Fatalf("text = %#v", got["text"])
	}
	if !a.Status().Connected {
		t.Fatal("adapter should report connected after Start")
	}
}

func TestGoogleChatSendCanOverrideTargetWithHTTPMetadata(t *testing.T) {
	hitDefault := false
	defaultSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitDefault = true
		w.WriteHeader(http.StatusOK)
	}))
	defer defaultSrv.Close()

	overrideHit := false
	overrideSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		overrideHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer overrideSrv.Close()

	a, err := New("google_chat", defaultSrv.URL, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Send(context.Background(), message.Message{
		Metadata: map[string]string{"to": overrideSrv.URL},
		Parts:    message.Text("route this"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if hitDefault || !overrideHit {
		t.Fatalf("defaultHit=%v overrideHit=%v, want only override", hitDefault, overrideHit)
	}
}

func TestGoogleChatSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad hook", http.StatusBadGateway)
	}))
	defer srv.Close()
	a, err := New("google_chat", srv.URL, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Send(context.Background(), message.Message{Parts: message.Text("hello")})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want status 502", err)
	}
}

func TestGoogleChatRegistryRequiresWebhookURL(t *testing.T) {
	_, ok, err := registry.NewChannel("google_chat", map[string]any{})
	if !ok {
		t.Fatal("google_chat factory not registered")
	}
	if err == nil || !strings.Contains(err.Error(), "webhook_url is required") {
		t.Fatalf("err = %v, want webhook_url required", err)
	}
}
