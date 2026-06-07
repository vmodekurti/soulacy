package extstorage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdkext "github.com/soulacy/soulacy/sdk/extstorage"
)

// TestHelperStorageSidecar is not a real test: it is re-executed as the
// sidecar subprocess (standard helper-process pattern, mirroring the E3
// adapter tests). GO_EXTSTORAGE_SIDECAR selects the behaviour.
func TestHelperStorageSidecar(t *testing.T) {
	mode := os.Getenv("GO_EXTSTORAGE_SIDECAR")
	if mode == "" {
		t.Skip("helper process only")
	}
	defer os.Exit(0)
	runHelperSidecar(mode)
}

func runHelperSidecar(mode string) {
	out := os.Stdout
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 4096), 4*1024*1024)

	type entry struct {
		ID, AgentID, Content string
	}
	var store []entry
	subs := map[string]string{} // id → subject
	nextSub := 0

	respond := func(id int64, result any) {
		m, _ := sdkext.NewResponse(id, result)
		_ = sdkext.WriteMessage(out, m)
	}

	if mode == "silent" {
		time.Sleep(10 * time.Second)
		return
	}

	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		m, err := sdkext.ParseMessage([]byte(line))
		if err != nil {
			continue
		}
		if !m.IsRequest() {
			continue
		}
		id := *m.ID
		switch m.Method {
		case sdkext.MethodNegotiate:
			var p sdkext.NegotiateParams
			_ = json.Unmarshal(m.Params, &p)
			shared := p.SharedDir
			if mode == "noecho" {
				shared = "/somewhere/else"
			}
			respond(id, sdkext.NegotiateResult{
				Protocol:     1,
				Name:         "go-helper",
				Capabilities: []string{"vector", "queue"},
				SharedDir:    shared,
			})
			if mode == "crashafterhello" {
				os.Exit(1)
			}
		case sdkext.MethodShutdown:
			respond(id, map[string]bool{"ok": true})
			return
		case sdkext.MethodVectorWrite:
			var p sdkext.VectorWriteParams
			_ = json.Unmarshal(m.Params, &p)
			content := p.Content
			// Shared-mount transport: content_file is relative to the
			// negotiated shared dir, which the host passes as our CWD --
			// no, it travels in negotiate params; helper reads env-free:
			// the test writes the absolute path into content_file's dir
			// via SHARED_DIR env set by... simpler: resolve relative to
			// the env var the host test sets.
			if p.ContentFile != "" {
				b, err := os.ReadFile(os.Getenv("HELPER_SHARED_DIR") + "/" + p.ContentFile)
				if err == nil {
					content = string(b)
				}
			}
			store = append(store, entry{ID: p.ID, AgentID: p.AgentID, Content: content})
			respond(id, sdkext.VectorWriteResult{OK: true})
		case sdkext.MethodVectorSearch:
			var p sdkext.VectorSearchParams
			_ = json.Unmarshal(m.Params, &p)
			hits := []sdkext.VectorHit{}
			for _, e := range store {
				if p.AgentID != "" && e.AgentID != p.AgentID {
					continue
				}
				if strings.Contains(e.Content, p.Query) {
					hits = append(hits, sdkext.VectorHit{ID: e.ID, AgentID: e.AgentID, Content: e.Content, Distance: 0.1})
				}
			}
			respond(id, sdkext.VectorSearchResult{Results: hits})
		case sdkext.MethodQueueSubscribe:
			nextSub++
			var p sdkext.QueueSubscribeParams
			_ = json.Unmarshal(m.Params, &p)
			sid := fmt.Sprintf("sub-%d", nextSub)
			subs[sid] = p.Subject
			respond(id, sdkext.QueueSubscribeResult{SubscriptionID: sid})
		case sdkext.MethodQueueUnsubscribe:
			var p sdkext.QueueUnsubscribeParams
			_ = json.Unmarshal(m.Params, &p)
			delete(subs, p.SubscriptionID)
			respond(id, map[string]bool{"ok": true})
		case sdkext.MethodQueuePublish:
			var p sdkext.QueuePublishParams
			_ = json.Unmarshal(m.Params, &p)
			for sid, subject := range subs {
				if subject == p.Subject || subject == ">" {
					_ = sdkext.WriteMessage(out, sdkext.Message{
						JSONRPC: sdkext.Version2,
						Method:  sdkext.NotifyQueueMessage,
						Params: sdkext.MustParams(sdkext.QueueMessageParams{
							SubscriptionID: sid, Subject: p.Subject,
							Data: p.Data, DeliveryID: "d-1",
						}),
					})
				}
			}
			respond(id, sdkext.QueuePublishResult{OK: true})
		case sdkext.MethodQueueAck:
			respond(id, map[string]bool{"ok": true})
		default:
			_ = sdkext.WriteMessage(out, sdkext.NewErrorResponse(id, sdkext.CodeMethodNotFound, "method not found"))
		}
	}
}

// helperConfig builds a ClientConfig that re-executes this test binary as
// the sidecar in the given mode.
func helperConfig(t *testing.T, mode string) ClientConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv("GO_EXTSTORAGE_SIDECAR", mode)
	return ClientConfig{
		Name:        "helper-" + mode,
		Command:     exe,
		Args:        []string{"-test.run", "TestHelperStorageSidecar"},
		ScratchRoot: t.TempDir(),
	}
}

func TestClient_NegotiateHappyPath(t *testing.T) {
	c := NewClient(helperConfig(t, "happy"))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	neg := c.Negotiated()
	if neg.Protocol != 1 || neg.Name != "go-helper" {
		t.Errorf("negotiated = %+v", neg)
	}
	if c.SharedDir() == "" {
		t.Error("SharedDir empty after Start")
	}
	if fi, err := os.Stat(c.SharedDir()); err != nil || !fi.IsDir() {
		t.Errorf("scratch dir missing: %v", err)
	}
	if neg.SharedDir != c.SharedDir() {
		t.Errorf("sidecar echo %q != %q", neg.SharedDir, c.SharedDir())
	}
}

func TestClient_RefusesWrongSharedDirEcho(t *testing.T) {
	c := NewClient(helperConfig(t, "noecho"))
	err := c.Start(context.Background())
	if err == nil {
		c.Close()
		t.Fatal("Start must fail when the sidecar doesn't echo the shared dir")
	}
	if !strings.Contains(err.Error(), "shared dir") {
		t.Errorf("error should mention shared dir: %v", err)
	}
}

func TestClient_HandshakeTimeout(t *testing.T) {
	cfg := helperConfig(t, "silent")
	cfg.HandshakeTimeout = 200 * time.Millisecond
	c := NewClient(cfg)
	start := time.Now()
	if err := c.Start(context.Background()); err == nil {
		c.Close()
		t.Fatal("Start must fail on a silent sidecar")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("handshake timeout not honoured (took %s)", time.Since(start))
	}
}

func TestClient_UnknownMethodSurfacesRPCError(t *testing.T) {
	c := NewClient(helperConfig(t, "happy"))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	err := c.Call(context.Background(), "no.such.method", struct{}{}, nil)
	if err == nil {
		t.Fatal("expected method-not-found error")
	}
	var rpcErr *sdkext.Error
	if !errorsAs(err, &rpcErr) || rpcErr.Code != sdkext.CodeMethodNotFound {
		t.Errorf("want wrapped *extstorage.Error code %d, got %v", sdkext.CodeMethodNotFound, err)
	}
}

func TestClient_CallAfterSidecarExit(t *testing.T) {
	c := NewClient(helperConfig(t, "crashafterhello"))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after sidecar exit")
	}
	err := c.Call(context.Background(), sdkext.MethodVectorSearch,
		sdkext.VectorSearchParams{Query: "x", TopK: 1}, nil)
	if err == nil {
		t.Fatal("Call after exit must error")
	}
}

func TestClient_CloseRemovesScratchAndIsIdempotent(t *testing.T) {
	c := NewClient(helperConfig(t, "happy"))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	dir := c.SharedDir()
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("scratch dir not removed: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// errorsAs avoids importing errors in two places.
func errorsAs(err error, target *(*sdkext.Error)) bool {
	for err != nil {
		if e, ok := err.(*sdkext.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
