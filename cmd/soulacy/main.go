// main.go — Soulacy gateway server entry point.
//
// Thin composition root (Story E10 part 3): the sandbox re-exec intercept
// and config load live here; everything else is wired by internal/app.
// Built-in drivers register themselves with the SDK factory registries via
// the blank imports in builtins_gen.go (generated — see gen.go).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/soulacy/soulacy/internal/app"
	"github.com/soulacy/soulacy/internal/buildtool"
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

	// `soulacy build` — flavored-binary build tool (Story E12, promoted to
	// a subcommand in Story 19b). Runs before config load: building a
	// custom distribution must not require a working gateway config.
	if len(os.Args) > 1 && os.Args[1] == "build" {
		if err := runBuild(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "soulacy build: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "soulacy: %v\n", err)
		os.Exit(1)
	}
}

// runBuild parses `soulacy build` flags and delegates to internal/buildtool.
//
//	soulacy build --with github.com/acme/soulacy-matrix@v1.2.0 -o bin/soulacy-matrix
func runBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	var with stringList
	out := fs.String("o", "bin/soulacy", "output binary path")
	skipVerify := fs.Bool("skip-verify", false, "skip the conformance/registry test gates before building")
	keep := fs.Bool("keep", true, "keep the generated builtins_extra.go after the build (required for rebuilds)")
	fs.Var(&with, "with", "extra driver module to compile in, module[@version] (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return buildtool.Run(with, *out, *skipVerify, *keep)
}

// stringList collects repeated flag values.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

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
