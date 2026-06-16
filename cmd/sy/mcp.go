package main

import (
	"fmt"
	"os"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func buildMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP servers",
	}

	var name, transport, command string
	var args []string

	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add or update an MCP server in config.yaml",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if command == "" {
				return fmt.Errorf("--command is required")
			}

			ws, err := config.ResolveWorkspace()
			if err != nil {
				return fmt.Errorf("failed to resolve workspace: %w", err)
			}
			configPath := ws.Root + "/config.yaml"

			data, err := os.ReadFile(configPath)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to read config.yaml: %w", err)
			}

			var root map[string]interface{}
			if len(data) > 0 {
				if err := yaml.Unmarshal(data, &root); err != nil {
					return fmt.Errorf("failed to parse config.yaml: %w", err)
				}
			} else {
				root = make(map[string]interface{})
			}

			if root["mcp"] == nil {
				root["mcp"] = make(map[string]interface{})
			}
			mcp, ok := root["mcp"].(map[string]interface{})
			if !ok {
				return fmt.Errorf("'mcp' is not an object in config.yaml")
			}

			if mcp["servers"] == nil {
				mcp["servers"] = make(map[string]interface{})
			}
			servers, ok := mcp["servers"].(map[string]interface{})
			if !ok {
				return fmt.Errorf("'mcp.servers' is not an object in config.yaml")
			}

			serverBlock := map[string]interface{}{
				"transport": transport,
				"command":   command,
				"args":      args,
			}
			
			// If server already exists, preserve env
			if existing, ok := servers[name].(map[string]interface{}); ok {
				if env, ok := existing["env"]; ok {
					serverBlock["env"] = env
				}
			}

			servers[name] = serverBlock

			// Write back
			out, err := yaml.Marshal(&root)
			if err != nil {
				return fmt.Errorf("failed to marshal config.yaml: %w", err)
			}

			if err := os.WriteFile(configPath, out, 0644); err != nil {
				return fmt.Errorf("failed to write config.yaml: %w", err)
			}

			fmt.Printf("Successfully added MCP server '%s' to config.yaml\n", name)
			return nil
		},
	}

	addCmd.Flags().StringVar(&name, "name", "", "Name of the MCP server")
	addCmd.Flags().StringVar(&transport, "transport", "stdio", "Transport type (stdio or http)")
	addCmd.Flags().StringVar(&command, "command", "", "Command to run (e.g. uv, node)")
	addCmd.Flags().StringSliceVar(&args, "args", nil, "Arguments for the command")

	cmd.AddCommand(addCmd)
	return cmd
}
