package gateway

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true,
		"::1":       true,
		"[::1]":     true,
		"localhost": true,
		"LOCALHOST": true,
		"0.0.0.0":   false,
		"":          false, // empty == all interfaces == unsafe
		"192.168.1.5": false,
		"example.com": false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func cfgWith(host, key string, allow bool) *config.Config {
	c := &config.Config{}
	c.Server.Host = host
	c.Server.APIKey = key
	c.Server.AllowUnauthenticated = allow
	return c
}

func TestCheckAuthBindSafety(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		authEngine  bool
		wantErr     bool
	}{
		{"loopback no key", cfgWith("127.0.0.1", "", false), false, false},
		{"loopback empty-host treated unsafe", cfgWith("", "", false), false, true},
		{"public no key refuses", cfgWith("0.0.0.0", "", false), false, true},
		{"public with key ok", cfgWith("0.0.0.0", "secret", false), false, false},
		{"public no key but allowed", cfgWith("0.0.0.0", "", true), false, false},
		{"public no static key but jwt configured", cfgWith("0.0.0.0", "", false), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkAuthBindSafety(tt.cfg, tt.authEngine)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkAuthBindSafety() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "refusing to start") {
				t.Errorf("expected remediation message, got: %v", err)
			}
		})
	}
}
