// main.go — Soulacy CLI (sy) entry point.
// The CLI communicates with a running Soulacy gateway over its REST API.
// Every action available in the GUI is also available as a CLI command.
// Config is read from ~/.soulacy/config.yaml or SOULACY_CONFIG_PATH.
//
// Usage:
//
//	sy agent list
//	sy agent create --file soul.yaml
//	sy agent enable support-bot
//	sy chat --agent support-bot "Hello, world!"
//	sy channel list
//	sy memory list --agent support-bot
//	sy schedule list
//	sy logs --agent support-bot --follow
//	sy server start
//	sy server status
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	gatewayURL string
	apiKey     string
	outputJSON bool
)

func main() {
	root := buildRoot()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func buildRoot() *cobra.Command {
	root := &cobra.Command{
		Use: "sy",
		Short: "Soulacy CLI — manage your agentic framework from the terminal",
		Long: `sy is the command-line interface for Soulacy.

Every GUI action is available here. All commands communicate with the
Soulacy gateway over its REST API.

Quick start:
  sy server start              # start the gateway
  sy agent list                # list loaded agents
  sy chat --agent my-agent "Hello!"   # chat with an agent
  sy logs --follow             # stream live event log`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip config loading for setup — it creates the config
			if cmd.Name() == "setup" {
				return nil
			}
			viper.SetConfigName("config")
			viper.SetConfigType("yaml")
			home, _ := os.UserHomeDir()
			viper.AddConfigPath(home + "/.soulacy")
			viper.AddConfigPath(".")
			viper.SetEnvPrefix("SOULACY")
			viper.AutomaticEnv()
			_ = viper.ReadInConfig()

			if gatewayURL == "" {
				gatewayURL = viper.GetString("cli.gateway_url")
			}
			if gatewayURL == "" {
				port := viper.GetInt("server.port")
				if port == 0 {
					port = 18789
				}
				gatewayURL = fmt.Sprintf("http://localhost:%d", port)
			}
			if apiKey == "" {
				apiKey = viper.GetString("server.api_key")
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&gatewayURL, "gateway", "", "Gateway URL (default: http://localhost:18789)")
	root.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key for gateway authentication")
	root.PersistentFlags().BoolVar(&outputJSON, "json", false, "Output raw JSON")

	// Sub-commands
	root.AddCommand(
		buildSetupCmd(),   // interactive setup wizard — show first
		buildAgentCmd(),
		buildChatCmd(),
		buildChannelCmd(),
		buildScheduleCmd(),
		buildMemoryCmd(),
		buildSkillCmd(),
		buildLogsCmd(),
		buildServerCmd(),
		buildPullCmd(),    // sy pull — agent marketplace
		buildEvalCmd(),    // sy eval — evaluation framework
		buildVersionCmd(),
	)
	return root
}

// ── Agent commands ──────────────────────────────────────────────────────────

func buildAgentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Manage agents"}

	// list
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List all agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/agents", "agents")
		},
	})

	// get
	cmd.AddCommand(&cobra.Command{
		Use: "get <id>", Short: "Show agent details",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/agents/"+args[0], "")
		},
	})

	// create
	var createFile string
	createCmd := &cobra.Command{
		Use: "create", Short: "Create an agent from a SOUL.yaml file",
		RunE: func(cmd *cobra.Command, args []string) error {
			if createFile == "" {
				return fmt.Errorf("--file is required")
			}
			data, err := os.ReadFile(createFile)
			if err != nil {
				return err
			}
			return apiPost("/agents", data)
		},
	}
	createCmd.Flags().StringVarP(&createFile, "file", "f", "", "Path to SOUL.yaml")
	cmd.AddCommand(createCmd)

	// enable / disable
	cmd.AddCommand(&cobra.Command{
		Use: "enable <id>", Short: "Enable an agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/agents/"+args[0]+"/enable", nil)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "disable <id>", Short: "Disable an agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/agents/"+args[0]+"/disable", nil)
		},
	})

	// delete
	cmd.AddCommand(&cobra.Command{
		Use: "delete <id>", Short: "Delete an agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiDelete("/agents/" + args[0])
		},
	})

	// trigger (manual run)
	cmd.AddCommand(&cobra.Command{
		Use: "trigger <id>", Short: "Manually trigger a scheduled agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/agents/"+args[0]+"/trigger", nil)
		},
	})

	return cmd
}

// ── Chat ────────────────────────────────────────────────────────────────────

func buildChatCmd() *cobra.Command {
	var agentID, userID string
	cmd := &cobra.Command{
		Use:   "chat <text>",
		Short: "Send a message to an agent and print the reply",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentID == "" {
				return fmt.Errorf("--agent is required")
			}
			body, _ := json.Marshal(map[string]string{
				"agent_id": agentID,
				"user_id":  userID,
				"text":     args[0],
			})
			resp, err := apiCall("POST", "/chat", body)
			if err != nil {
				return err
			}
			var result struct {
				Reply string `json:"reply"`
			}
			if err := json.Unmarshal(resp, &result); err != nil {
				fmt.Println(string(resp))
				return nil
			}
			fmt.Println(result.Reply)
			return nil
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent ID to chat with")
	cmd.Flags().StringVar(&userID, "user", "cli-user", "User ID")
	return cmd
}

// ── Channel ─────────────────────────────────────────────────────────────────

func buildChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "channel", Short: "Manage channel adapters"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List channel adapter status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/channels", "channels")
		},
	})
	return cmd
}

// ── Schedule ─────────────────────────────────────────────────────────────────

func buildScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "schedule", Short: "Manage scheduled agents"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List all scheduled agent entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/schedule", "schedule")
		},
	})
	return cmd
}

// ── Memory ───────────────────────────────────────────────────────────────────

func buildMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "memory", Short: "Inspect and manage agent memory"}
	var agentID string
	listCmd := &cobra.Command{
		Use: "list", Short: "List memory entries for an agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentID == "" {
				return fmt.Errorf("--agent is required")
			}
			return apiGet("/memory/"+agentID, "")
		},
	}
	listCmd.Flags().StringVar(&agentID, "agent", "", "Agent ID")
	cmd.AddCommand(listCmd)
	return cmd
}

// ── Skills ───────────────────────────────────────────────────────────────────

func buildSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage Agent Skills (agentskills.io format)",
	}

	// list
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all loaded Agent Skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/skills", "skills")
		},
	})

	// get
	cmd.AddCommand(&cobra.Command{
		Use:   "get <name>",
		Short: "Show full skill instructions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/skills/"+args[0], "")
		},
	})

	// install — copies a local skill directory into ~/.soulacy/skills/
	cmd.AddCommand(&cobra.Command{
		Use:   "install <path>",
		Short: "Install a skill from a local directory into ~/.soulacy/skills/",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return installSkill(args[0])
		},
	})

	return cmd
}

// installSkill copies a skill directory into ~/.soulacy/skills/<name>/.
func installSkill(src string) error {
	// Validate that src contains a SKILL.md
	skillMD := src + "/SKILL.md"
	if _, err := os.Stat(skillMD); err != nil {
		return fmt.Errorf("not a valid skill directory: SKILL.md not found in %s", src)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// Derive skill name from directory name
	name := src
	if name[len(name)-1] == '/' {
		name = name[:len(name)-1]
	}
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' || name[i] == '\\' {
			name = name[i+1:]
			break
		}
	}

	dest := home + "/.soulacy/skills/" + name
	if err := os.MkdirAll(home+"/.soulacy/skills", 0755); err != nil {
		return err
	}

	fmt.Printf("Installing skill %q → %s\n", name, dest)
	if err := copyDir(src, dest); err != nil {
		return err
	}
	fmt.Printf("✓ Installed. Restart the gateway (or it will hot-reload on next skill scan).\n")
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}

// ── Logs ─────────────────────────────────────────────────────────────────────

func buildLogsCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Stream live event logs from the gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			if follow {
				fmt.Printf("Connecting to %s/ws/events ...\n", gatewayURL)
				fmt.Println("(WebSocket streaming — press Ctrl+C to stop)")
				// WebSocket client would go here; simplified for skeleton
				return streamEvents()
			}
			fmt.Println("Use --follow to stream live events")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream events in real-time")
	return cmd
}

func streamEvents() error {
	// Simple polling fallback; real implementation uses gorilla/websocket
	for {
		resp, err := apiCall("GET", "/health", nil)
		if err != nil {
			return err
		}
		fmt.Printf("[%s] gateway alive: %s\n", time.Now().Format(time.RFC3339), string(resp))
		time.Sleep(5 * time.Second)
	}
}

// ── Server ────────────────────────────────────────────────────────────────────

func buildServerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "server", Short: "Control the Soulacy gateway server"}
	cmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the gateway server in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Starting Soulacy gateway...")
			fmt.Println("  Tip: run 'soulacy' directly for production use.")
			fmt.Println("  This command is a convenience wrapper.")
			// In a full implementation this would exec the soulacy binary
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Check gateway health",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/health", "")
		},
	})
	return cmd
}

// ── Version ───────────────────────────────────────────────────────────────────

func buildVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Soulacy CLI — dev build")
			fmt.Printf("Gateway: %s\n", gatewayURL)
		},
	}
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func apiCall(method, path string, body []byte) ([]byte, error) {
	url := gatewayURL + "/api/v1" + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach gateway at %s — is it running?\n  hint: run 'sy server start'", gatewayURL)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("gateway error %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func apiGet(path, field string) error {
	data, err := apiCall("GET", path, nil)
	if err != nil {
		return err
	}
	if outputJSON || field == "" {
		fmt.Println(string(data))
		return nil
	}
	// Pretty-print the named field
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		fmt.Println(string(data))
		return nil
	}
	if val, ok := result[field]; ok {
		pretty, _ := json.MarshalIndent(val, "", "  ")
		fmt.Println(string(pretty))
	} else {
		pretty, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(pretty))
	}
	return nil
}

func apiPost(path string, body []byte) error {
	if body == nil {
		body = []byte("{}")
	}
	data, err := apiCall("POST", path, body)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func apiDelete(path string) error {
	_, err := apiCall("DELETE", path, nil)
	if err != nil {
		return err
	}
	fmt.Println("deleted.")
	return nil
}
