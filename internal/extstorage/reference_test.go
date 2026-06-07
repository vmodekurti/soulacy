package extstorage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/sdk/extstorage/storagetest"
	"github.com/soulacy/soulacy/sdk/memory"
	"github.com/soulacy/soulacy/sdk/queue"
)

func referenceSidecar(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	script, err := filepath.Abs("../../scripts/reference-storage-sidecar.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("reference sidecar missing: %v", err)
	}
	return script
}

// TestReferenceSidecarConformance proves the contract is implementable
// outside Go: the exported E24 kit runs against the real python reference
// sidecar (mirrors the E3/E11 cross-language proof).
func TestReferenceSidecarConformance(t *testing.T) {
	script := referenceSidecar(t)
	if err := storagetest.RunConformance(context.Background(), t.TempDir(),
		"python3", script); err != nil {
		t.Fatal(err)
	}
}

// TestReferenceSidecarEndToEnd drives the python reference through the
// real host adapters: vector write/search and queue pub/sub.
func TestReferenceSidecarEndToEnd(t *testing.T) {
	script := referenceSidecar(t)
	cfg := ClientConfig{
		Name:        "py-reference",
		Command:     "python3",
		Args:        []string{script},
		ScratchRoot: t.TempDir(),
	}

	vb, err := NewVectorBackend(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewVectorBackend: %v", err)
	}
	defer vb.Close()
	err = vb.Write(context.Background(), memory.Entry{
		ID: "p1", AgentID: "a1", Content: "soulacy external storage protocol",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	hits, err := vb.Search(context.Background(), "a1", "storage protocol", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.ID != "p1" {
		t.Fatalf("hits = %+v", hits)
	}

	qb, err := NewQueueBackend(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewQueueBackend: %v", err)
	}
	defer qb.Close()
	got := make(chan *queue.Message, 1)
	if _, err := qb.Subscribe(context.Background(), "soulacy.events.>", "", func(m *queue.Message) {
		got <- m
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := qb.Publish(context.Background(), "soulacy.events.test", []byte("hi")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case m := <-got:
		if string(m.Data) != "hi" || m.Subject != "soulacy.events.test" {
			t.Errorf("delivered = %q %q", m.Subject, m.Data)
		}
		if err := m.Ack(); err != nil {
			t.Errorf("Ack: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery from python reference within 5s")
	}
}
