// httpclient.go — shared HTTP transport for every LLM provider.
//
// PRODUCTION_AUDIT → HIGH/Performance: each provider was building its own
// http.Client with the default Transport, which caps idle conns per host at
// 2. With concurrent agents hitting the same Ollama/Anthropic/OpenAI host
// the pool churned and every nth request paid a full TLS handshake. We now
// share one well-tuned Transport across all providers and embedders, so the
// pool warms up once and stays warm.
//
// Per-provider timeout overrides still apply: each provider builds its own
// *http.Client around this shared Transport with its own Timeout.

package llm

import (
	"net/http"
	"sync"
	"time"
)

var (
	sharedTransportOnce sync.Once
	sharedTransport     *http.Transport
)

// SharedTransport returns the process-wide *http.Transport used by every
// LLM provider client. Tuned for high-concurrency same-host workloads:
//   - MaxIdleConnsPerHost: 64  (vs default 2)
//   - MaxConnsPerHost:     0   (unlimited; flow control is upstream)
//   - IdleConnTimeout:    90s  (Go default)
//   - ResponseHeaderTimeout: 60s — protects against a stuck provider that
//     accepts the connection but never returns headers.
func SharedTransport() *http.Transport {
	sharedTransportOnce.Do(func() {
		// Start from a clone of DefaultTransport so we inherit sane defaults
		// for DialContext, ForceAttemptHTTP2, TLSHandshakeTimeout, etc.
		base, ok := http.DefaultTransport.(*http.Transport)
		var t *http.Transport
		if ok {
			t = base.Clone()
		} else {
			t = &http.Transport{}
		}
		t.MaxIdleConnsPerHost = 64
		t.MaxConnsPerHost = 0
		t.IdleConnTimeout = 90 * time.Second
		t.ResponseHeaderTimeout = 60 * time.Second
		sharedTransport = t
	})
	return sharedTransport
}

// SharedHTTPClient returns an *http.Client wrapping SharedTransport with the
// caller's chosen overall timeout. Pass 0 to disable the client-level timeout
// (Ollama uses this — its context governs the upper bound because cold-loading
// large models can exceed any reasonable client deadline).
func SharedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: SharedTransport(),
		Timeout:   timeout,
	}
}
