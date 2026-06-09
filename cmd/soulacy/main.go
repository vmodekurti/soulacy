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

	// `soulacy registry` — reference E19 package registry (serve/keygen).
	// Also pre-config: a registry host needs no gateway config.
	if len(os.Args) > 1 && os.Args[1] == "registry" {
		if err := runRegistry(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "soulacy registry: %v\n", err)
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

	// "Dumb install" first-run bootstrap. If config.yaml doesn't exist,
	// write a default with a generated API key. If it exists but has no
	// key on a loopback bind, generate one and patch just that field
	// (preserves comments + operator edits). No-op on subsequent runs.
	bootstrap, err := config.EnsureBootstrap(cfg, cfgPath)
	if err != nil {
		return fmt.Errorf("first-run bootstrap: %w", err)
	}
	if bootstrap.Action != config.BootstrapNoop {
		printFirstRunBanner(bootstrap, cfg.Server.Host, cfg.Server.Port)
	}

	a, err := app.New(cfg, app.WithConfigPath(cfgPath))
	if err != nil {
		return err
	}
	return a.Run(context.Background())
}

// printFirstRunBanner shows the operator their freshly-bootstrapped
// install once. After this run, config.yaml exists and the banner
// never fires again. Goes to stderr so it surfaces in service-manager
// logs regardless of stdout redirection.
func printFirstRunBanner(b config.BootstrapResult, host string, port int) {
	url := fmt.Sprintf("http://%s:%d", host, port)
	what := "Bootstrapped configuration"
	if b.Action == config.BootstrapGeneratedKey {
		what = "Generated API key (config file kept)"
	}
	fmt.Fprintf(os.Stderr,
		"\n┌─ Soulacy first-run ────────────────────────────────────────────┐\n"+
			"│ %-62s │\n"+
			"│ Config:  %-53s │\n"+
			"│ URL:     %-53s │\n"+
			"│ API key: %-53s │\n"+
			"│ This banner appears once. Save the key — it gates every API. │\n"+
			"└────────────────────────────────────────────────────────────────┘\n\n",
		what, b.ConfigPath, url, b.APIKey,
	)
}
