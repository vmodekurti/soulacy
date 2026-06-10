// engine_tools_misc.go — host-introspection built-ins.
//
// ARCH-2: mechanically extracted from engine.go (no behaviour change).
// SAFE (read-only): env_get, sys_info.
package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"sort"
	"strings"
)

// buildMiscTools returns the misc-domain OS-level built-in tools. Extracted
// from buildSystemTools (ARCH-2) — identical definitions, no behaviour change.
func (e *Engine) buildMiscTools() []BuiltinTool {
	return []BuiltinTool{
		{
			Name:        "env_get",
			Gate:        "",
			Description: "Read environment variables. Pass a variable name to get its value, or omit to list all environment variables. Useful for checking API keys, PATH, HOME, or any runtime configuration.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Variable name to look up (omit to return all)",
					},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				name := strings.TrimSpace(argString(args, "name"))
				if name != "" {
					val := os.Getenv(name)
					if val == "" {
						return fmt.Sprintf("%s=(not set)", name), nil
					}
					return fmt.Sprintf("%s=%s", name, val), nil
				}
				// Return all env vars, sorted for readability
				all := os.Environ()
				sort.Strings(all)
				return strings.Join(all, "\n"), nil
			},
		},
		{
			Name:        "sys_info",
			Gate:        "",
			Description: "Return system information: operating system, CPU architecture, hostname, current user home directory, working directory, Go runtime version, and PATH. Useful for understanding the host environment before running commands.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				hostname, _ := os.Hostname()
				home, _ := os.UserHomeDir()
				cwd, _ := os.Getwd()

				// Get current user via id command (works on macOS and Linux)
				userStr := os.Getenv("USER")
				if userStr == "" {
					userStr = os.Getenv("LOGNAME")
				}
				if userStr == "" {
					if out, err := exec.Command("id", "-un").Output(); err == nil {
						userStr = strings.TrimSpace(string(out))
					}
				}

				return fmt.Sprintf(
					"OS: %s\nArch: %s\nHostname: %s\nUser: %s\nHome: %s\nCWD: %s\nPATH: %s\nGo: %s",
					goruntime.GOOS,
					goruntime.GOARCH,
					hostname,
					userStr,
					home,
					cwd,
					os.Getenv("PATH"),
					goruntime.Version(),
				), nil
			},
		},
	}
}
