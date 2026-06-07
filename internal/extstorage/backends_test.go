package extstorage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/sdk/memory"
	"github.com/soulacy/soulacy/sdk/queue"
)

func TestVectorBackend_WriteSearchRoundTrip(t *testing.T) {
	b, err := NewVectorBackend(context.Background(), helperConfig(t, "happy"))
	if err != nil {
		t.Fatalf("NewVectorBackend: %v", err)
	}
	defer b.Close()

	err = b.Write(context.Background(), memory.Entry{
		ID: "e1", AgentID: "a1", Scope: memory.ScopeAgent,
		Content: "the quick brown fox", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	hits, err := b.Search(context.Background(), "a1", "quick", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.ID != "e1" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].Entry.Content != "the quick brown fox" {
		t.Errorf("content = %q", hits[0].Entry.Content)
	}

	// Agent scoping: other agents see nothing.
	hits, err = b.Search(context.Background(), "other", "quick", 5)
	if err != nil {
		t.Fatalf("Search other: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("cross-agent hits = %+v", hits)
	}
}

func TestQueueBackend_PublishSubscribeAck(t *testing.T) {
	b, err := NewQueueBackend(context.Background(), helperConfig(t, "happy"))
	if err != nil {
		t.Fatalf("NewQueueBackend: %v", err)
	}
	defer b.Close()

	got := make(chan *queue.Message, 1)
	sub, err := b.Subscribe(context.Background(), "events.test", "", func(m *queue.Message) {
		got <- m
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := b.Publish(context.Background(), "events.test", []byte("payload")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case m := <-got:
		if m.Subject != "events.test" || string(m.Data) != "payload" {
			t.Errorf("delivered = %q %q", m.Subject, m.Data)
		}
		if err := m.Ack(); err != nil {
			t.Errorf("Ack: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery within 5s")
	}

	// After Unsubscribe, deliveries stop reaching the handler.
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if err := b.Publish(context.Background(), "events.test", []byte("late")); err != nil {
		t.Fatalf("Publish 2: %v", err)
	}
	select {
	case m := <-got:
		t.Errorf("delivery after unsubscribe: %q", m.Data)
	case <-time.After(300 * time.Millisecond):
	}

	if err := sub.Unsubscribe(); err != nil {
		t.Errorf("second Unsubscribe: %v", err)
	}
}

func TestNewScratchDir(t *testing.T) {
	root := t.TempDir()
	dir, cleanup, err := NewScratchDir(root, "my id/!x")
	if err != nil {
		t.Fatalf("NewScratchDir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("dir not absolute: %q", dir)
	}
	if !strings.HasPrefix(filepath.Base(dir), "my_id__x-") {
		t.Errorf("sanitised name wrong: %q", filepath.Base(dir))
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("perm = %v, want 0700", fi.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup left dir behind")
	}
}

func TestRegistryFactories(t *testing.T) {
	// Missing command must error without spawning anything.
	if _, err := clientConfigFrom(map[string]any{}, "vector-external"); err == nil {
		t.Error("empty command must error")
	}
	cc, err := clientConfigFrom(map[string]any{
		"command":      "/bin/echo",
		"args":         []any{"a", "b"},
		"id":           "custom",
		"scratch_root": "/tmp/x",
	}, "vector-external")
	if err != nil {
		t.Fatalf("clientConfigFrom: %v", err)
	}
	if cc.Name != "custom" || cc.Command != "/bin/echo" || len(cc.Args) != 2 || cc.ScratchRoot != "/tmp/x" {
		t.Errorf("cc = %+v", cc)
	}
}
