// onboard_test.go — table-driven tests for the textual-patch helpers in
// onboard.go. These helpers do best-effort string surgery on config.yaml
// to preserve operator comments and edits — any regression here means
// `sy onboard` silently corrupts someone's hand-edited config, which is
// worse than failing loudly. So we cover the three shapes patchKey can
// be in (key exists / key missing / parent block missing) and the
// edge cases for maskKey.

package main

import (
	"strings"
	"testing"
)

func TestReplaceKeyLine(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		parent string
		key    string
		value  string
		want   string
	}{
		{
			name: "replace existing value, preserve trailing comment",
			body: `server:
  host: "127.0.0.1"
  port: 18789

llm:
  default_provider: ollama
`,
			parent: "server",
			key:    "host",
			value:  `"0.0.0.0"`,
			want: `server:
  host: "0.0.0.0"
  port: 18789

llm:
  default_provider: ollama
`,
		},
		{
			name: "key not present under parent: body unchanged",
			body: `server:
  port: 18789
`,
			parent: "server",
			key:    "host",
			value:  `"127.0.0.1"`,
			want: `server:
  port: 18789
`,
		},
		{
			name: "parent block missing: body unchanged",
			body: `llm:
  default_provider: openai
`,
			parent: "server",
			key:    "host",
			value:  `"127.0.0.1"`,
			want: `llm:
  default_provider: openai
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := replaceKeyLine(tc.body, tc.parent, tc.key, tc.value)
			if got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestInjectProviderKey_ExistingProviderBlock(t *testing.T) {
	body := `llm:
  default_provider: openai
  providers:
    openai:
      api_key: "old-key"
    ollama:
      base_url: "http://localhost:11434"
`
	got := injectProviderKey(body, "openai", "new-key")
	if !strings.Contains(got, `api_key: "new-key"`) {
		t.Fatalf("expected new key in body:\n%s", got)
	}
	if strings.Contains(got, `"old-key"`) {
		t.Fatalf("old key not replaced:\n%s", got)
	}
}

func TestInjectProviderKey_MissingProviderBlock(t *testing.T) {
	body := `llm:
  default_provider: openai
  providers:
    ollama:
      base_url: "http://localhost:11434"
`
	got := injectProviderKey(body, "anthropic", "sk-ant-test")
	if !strings.Contains(got, "anthropic:") {
		t.Fatalf("expected new anthropic block:\n%s", got)
	}
	if !strings.Contains(got, `api_key: "sk-ant-test"`) {
		t.Fatalf("expected api_key under anthropic:\n%s", got)
	}
	// Old ollama block must survive untouched.
	if !strings.Contains(got, "base_url: \"http://localhost:11434\"") {
		t.Fatalf("ollama block damaged by patch:\n%s", got)
	}
}

func TestInjectProviderKey_NoLLMBlock(t *testing.T) {
	body := `server:
  host: "127.0.0.1"
  port: 18789
`
	got := injectProviderKey(body, "openai", "sk-test")
	if !strings.Contains(got, "llm:") {
		t.Fatalf("expected new llm block appended:\n%s", got)
	}
	if !strings.Contains(got, `api_key: "sk-test"`) {
		t.Fatalf("expected api_key:\n%s", got)
	}
	// Pre-existing server block must survive.
	if !strings.Contains(got, "host: \"127.0.0.1\"") {
		t.Fatalf("server block damaged by append:\n%s", got)
	}
}

func TestInjectProviderBaseURL(t *testing.T) {
	body := `
llm:
  providers:
    custom:
      api_key: "sk-custom"
`
	got := injectProviderBaseURL(body, "custom", "http://localhost:11434")
	if !strings.Contains(got, `base_url: "http://localhost:11434"`) {
		t.Fatalf("expected base_url injected, got:\n%s", got)
	}

	// Should not overwrite api_key
	if !strings.Contains(got, `api_key: "sk-custom"`) {
		t.Fatalf("expected api_key preserved, got:\n%s", got)
	}

	// If it already has base_url, it should replace it
	bodyWithBaseURL := `
llm:
  providers:
    custom:
      base_url: "http://old.com"
      api_key: "sk-custom"
`
	gotReplaced := injectProviderBaseURL(bodyWithBaseURL, "custom", "http://new.com")
	if !strings.Contains(gotReplaced, `base_url: "http://new.com"`) {
		t.Fatalf("expected base_url replaced, got:\n%s", gotReplaced)
	}
	if strings.Contains(gotReplaced, `http://old.com`) {
		t.Fatalf("expected old base_url removed, got:\n%s", gotReplaced)
	}
}

func TestMaskKey(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"abc":                     "•••",
		"abcdefgh":                "••••••••",
		"sy_1234567890abcdef":     "sy_••••••••••••cdef",
		"sy_abcdefghijklmnopqrst": "sy_••••••••••••••••qrst",
	}
	for in, want := range cases {
		if got := maskKey(in); got != want {
			t.Errorf("maskKey(%q) = %q; want %q", in, got, want)
		}
	}
}
