// main.go — Soulacy gateway server entry point.
//
// Thin composition root (Story E10 part 3): the sandbox re-exec intercept
// and config load live here; everything else is wired by internal/app.
// Built-in drivers register themselves with the SDK factory registries via
// the blank imports in builtins_gen.go (generated — see gen.go).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/soulacy/soulacy/internal/app"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/sandbox"
)

func main() {
	// PRODUCTION_AUDIT → F1 (2026-05-27): sandbox subcommand intercept.
	// When the engine re-execs us as a sandbox wrapper (soulacy
	// __exec-sandbox …), we set rlimits and execve straight away —
	// without paying for config load, watcher setup, etc. The "if argv[1]
	// is the sentinel" check is intentionally before anything else.
	if sandbox.IsSandboxInvocation(os.Args) {
		sandbox.RunSandboxedAndExit(os.Args)
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "soulacy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := os.Getenv("SOULACY_CONFIG_PATH")
	cfg, resolvedPath, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfgPath == "" {
		cfgPath = resolvedPath
	}
	if err := config.EnsureDirs(cfg); err != nil {
		return fmt.Errorf("ensure dirs: %w", err)
	}

	a, err := app.New(cfg, app.WithConfigPath(cfgPath))
	if err != nil {
		return err
	}
	return a.Run(context.Background())
}
