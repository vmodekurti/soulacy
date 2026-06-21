package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func buildMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP servers",
	}

	var name, transport, command, pipSpec string
	var args []string

	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Install (optional) and register an MCP server in config.yaml",
		Long: `Register an MCP server in the live config.yaml under mcp.servers.

With --pip, the package is first installed into an isolated venv UNDER THE
WORKSPACE (mcp-servers/<name>/venv) so it survives container restarts, and a
bare --command is resolved to that venv's bin/. The config edit preserves
comments and writes 0600 (the file holds secrets).

Examples:
  sy mcp add --name weather --command weather-mcp --transport stdio
  sy mcp add --name notebooklm --pip notebooklm-mcp-cli --command notebooklm-mcp-cli`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			ws, err := config.ResolveWorkspace()
			if err != nil {
				return fmt.Errorf("resolve workspace: %w", err)
			}

			// Optional: install the pip package into a persistent, isolated venv
			// under the workspace volume so it survives restarts (build tools and
			// python ship in the runtime image).
			if pipSpec != "" {
				venvDir := filepath.Join(ws.Root, "mcp-servers", name, "venv")
				if err := os.MkdirAll(filepath.Dir(venvDir), 0o755); err != nil {
					return fmt.Errorf("create mcp dir: %w", err)
				}
				fmt.Printf("→ Creating venv at %s\n", venvDir)
				if out, verr := exec.Command("python3", "-m", "venv", venvDir).CombinedOutput(); verr != nil {
					return fmt.Errorf("create venv: %v\n%s", verr, out)
				}
				pip := filepath.Join(venvDir, "bin", "pip")
				fmt.Printf("→ Installing %q into the venv\n", pipSpec)
				ic := exec.Command(pip, "install", pipSpec)
				ic.Stdout, ic.Stderr = os.Stdout, os.Stderr
				if rerr := ic.Run(); rerr != nil {
					return fmt.Errorf("pip install: %w", rerr)
				}
				// A bare command name resolves to the venv's bin/, so the gateway
				// spawns the persistent install rather than a PATH lookup.
				if command != "" && !strings.ContainsRune(command, '/') {
					command = filepath.Join(venvDir, "bin", command)
				}
			}

			if command == "" {
				return fmt.Errorf("--command is required")
			}

			// Register in the live config, preserving comments (yaml.Node — the
			// same path the onboarding wizard uses, so this never corrupts the file).
			configPath := ws.ConfigFile
			doc, root, err := loadConfigDoc(configPath)
			if err != nil {
				return fmt.Errorf("read config: %w", err)
			}
			servers := ensureMapping(ensureMapping(root, "mcp"), "servers")
			srv := ensureMapping(servers, name)
			setScalar(srv, "transport", transport, 0)
			setScalar(srv, "command", command, yaml.DoubleQuotedStyle)
			if len(args) > 0 {
				setSequence(srv, "args", args)
			}
			if err := saveConfigDoc(configPath, doc); err != nil {
				return fmt.Errorf("write config: %w", err)
			}

			fmt.Printf("✓ Registered MCP server %q in %s\n", name, configPath)
			fmt.Println("  Restart the gateway (or it hot-reloads on config change) to connect.")
			return nil
		},
	}

	addCmd.Flags().StringVar(&name, "name", "", "Name/id of the MCP server")
	addCmd.Flags().StringVar(&transport, "transport", "stdio", "Transport type (stdio or http)")
	addCmd.Flags().StringVar(&command, "command", "", "Command to run the server (a bare name resolves to the venv bin when --pip is used)")
	addCmd.Flags().StringSliceVar(&args, "args", nil, "Arguments for the command")
	addCmd.Flags().StringVar(&pipSpec, "pip", "", "Optional pip package/spec to install into a persistent venv before registering")

	cmd.AddCommand(addCmd)
	return cmd
}

// setSequence sets m[key] to a flow-style YAML sequence of strings, creating or
// replacing the entry. Reuses yamlMapValue from onboard.go (same package).
func setSequence(m *yaml.Node, key string, values []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	if existing := yamlMapValue(m, key); existing != nil {
		existing.Kind = seq.Kind
		existing.Tag = seq.Tag
		existing.Style = seq.Style
		existing.Value = ""
		existing.Content = seq.Content
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		seq,
	)
}
