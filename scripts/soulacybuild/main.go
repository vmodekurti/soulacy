// Command soulacybuild produces a custom ("flavored") soulacy binary with
// extra drivers compiled in (Story E12). Third-party channels, providers,
// queue/vector backends, and reasoning strategies self-register with the
// SDK factory registries from init() (E10/E15); all a custom distribution
// needs is their packages blank-imported into cmd/soulacy and a normal
// build. This tool automates exactly that:
//
//	go run ./scripts/soulacybuild \
//	    --with github.com/acme/soulacy-matrix@v1.2.0 \
//	    -o bin/soulacy-matrix
//
// Steps: (1) write cmd/soulacy/builtins_extra.go blank-importing every
// --with module; (2) `go get module@version` for each; (3) run the
// conformance/registry test gates (E11 kits + TestAllBuiltinsRegistered)
// unless --skip-verify; (4) `go build` the single static binary.
//
// Run from the repository root. Without --with it performs a plain verified
// build. Promote to a `soulacy build` subcommand once the workflow settles.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// withFlags collects repeated --with values.
type withFlags []string

func (w *withFlags) String() string     { return strings.Join(*w, ",") }
func (w *withFlags) Set(v string) error { *w = append(*w, v); return nil }

const extraFile = "cmd/soulacy/builtins_extra.go"

func main() {
	var with withFlags
	out := flag.String("o", "bin/soulacy", "output binary path")
	skipVerify := flag.Bool("skip-verify", false, "skip the conformance/registry test gates before building")
	keep := flag.Bool("keep", true, "keep the generated builtins_extra.go after the build (required for rebuilds)")
	flag.Var(&with, "with", "extra driver module to compile in, module[@version] (repeatable)")
	flag.Parse()

	if err := run(with, *out, *skipVerify, *keep); err != nil {
		fmt.Fprintf(os.Stderr, "soulacybuild: %v\n", err)
		os.Exit(1)
	}
}

func run(with []string, out string, skipVerify, keep bool) error {
	if _, err := os.Stat("cmd/soulacy"); err != nil {
		return fmt.Errorf("run from the repository root (cmd/soulacy not found): %w", err)
	}

	// 1. Parse modules and write the extra blank-import file.
	var modules []string
	for _, w := range with {
		mod, ver, err := parseWith(w)
		if err != nil {
			return err
		}
		modules = append(modules, mod)
		// 2. Resolve the module into go.mod/go.sum.
		fmt.Printf("→ go get %s@%s\n", mod, ver)
		if err := runCmd("go", "get", mod+"@"+ver); err != nil {
			return fmt.Errorf("resolve %s@%s: %w", mod, ver, err)
		}
	}
	if src := generateExtraImports(modules); src != nil {
		if err := os.WriteFile(extraFile, src, 0644); err != nil {
			return fmt.Errorf("write %s: %w", extraFile, err)
		}
		fmt.Printf("→ wrote %s (%d extra driver(s))\n", extraFile, len(modules))
		if !keep {
			defer os.Remove(extraFile)
		}
	}

	// 3. Verification gates: the E11 conformance kits run against every
	// compiled-in driver's package tests, and TestAllBuiltinsRegistered
	// proves the registries are fully populated in THIS build.
	if !skipVerify {
		fmt.Println("→ verification: conformance kits + registry pin")
		if err := runCmd("go", "test", "-run", "TestConformance",
			"./internal/llm/", "./internal/channels/"); err != nil {
			return fmt.Errorf("conformance gate failed: %w", err)
		}
		// Sidecar protocol kit against the reference sidecars (skips
		// gracefully when python3 is unavailable on the build host).
		if err := runCmd("go", "test", "-run", "TestRunConformance|TestPoCVoiceSidecarConformance",
			"./internal/channels/external/"); err != nil {
			return fmt.Errorf("sidecar conformance gate failed: %w", err)
		}
		if err := runCmd("go", "test", "-run", "TestAllBuiltinsRegistered", "./cmd/soulacy/"); err != nil {
			return fmt.Errorf("registry gate failed: %w", err)
		}
	}

	// 4. Single static binary.
	if dir := filepath.Dir(out); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create output dir: %w", err)
		}
	}
	fmt.Printf("→ go build -o %s ./cmd/soulacy\n", out)
	if err := runCmd("go", "build", "-trimpath", "-o", out, "./cmd/soulacy"); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	fmt.Printf("✓ flavored binary ready: %s\n", out)
	return nil
}

// parseWith splits "module[@version]"; version defaults to "latest".
func parseWith(s string) (module, version string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("--with: empty module spec")
	}
	module, version = s, "latest"
	if i := strings.LastIndex(s, "@"); i >= 0 {
		module, version = s[:i], s[i+1:]
	}
	if module == "" || version == "" {
		return "", "", fmt.Errorf("--with %q: want module[@version]", s)
	}
	if strings.ContainsAny(module, " \t\"'") {
		return "", "", fmt.Errorf("--with %q: invalid module path", s)
	}
	return module, version, nil
}

// generateExtraImports renders the blank-import file for the extra driver
// modules. Returns nil when there is nothing to import.
func generateExtraImports(modules []string) []byte {
	if len(modules) == 0 {
		return nil
	}
	var b bytes.Buffer
	b.WriteString("// Code generated by scripts/soulacybuild. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// Extra drivers compiled into this flavored binary (Story E12). Their\n")
	b.WriteString("// init() functions self-register with the SDK factory registries.\n")
	b.WriteString("package main\n\nimport (\n")
	for _, m := range modules {
		fmt.Fprintf(&b, "\t_ %q\n", m)
	}
	b.WriteString(")\n")
	return b.Bytes()
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return cmd.Run()
}
