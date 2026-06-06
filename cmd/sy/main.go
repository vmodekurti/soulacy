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
	"strings"
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
		Use:   "sy",
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
		buildSetupCmd(), // interactive setup wizard — show first
		buildAgentCmd(),
		buildChatCmd(),
		buildChannelCmd(),
		buildScheduleCmd(),
		buildMemoryCmd(),
		buildSkillCmd(),
		buildLogsCmd(),
		buildServerCmd(),
		buildDoctorCmd(),
		buildPullCmd(), // sy pull — agent marketplace
		buildEvalCmd(), // sy eval — evaluation framework
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
	cmd.AddCommand(buildAgentValidateCmd())

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
	cmd.AddCommand(&cobra.Command{
		Use:   "status <id>",
		Short: "Show one channel adapter status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch, err := getChannel(args[0])
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(ch)
			}
			printChannelSummary(ch)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a configured channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/channels/"+args[0]+"/enable", nil)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/channels/"+args[0]+"/disable", nil)
		},
	})

	var setFields []string
	updateCmd := &cobra.Command{
		Use:   "update <id> --set key=value",
		Short: "Update channel settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(setFields) == 0 {
				return fmt.Errorf("at least one --set key=value is required")
			}
			settings := map[string]any{}
			for _, item := range setFields {
				key, value, ok := strings.Cut(item, "=")
				key = strings.TrimSpace(key)
				if !ok || key == "" {
					return fmt.Errorf("invalid --set %q; expected key=value", item)
				}
				settings[key] = parseCLIValue(value)
			}
			body, _ := json.Marshal(map[string]any{"settings": settings})
			return apiPatch("/channels/"+args[0], body)
		},
	}
	updateCmd.Flags().StringArrayVar(&setFields, "set", nil, "Setting assignment, repeatable: --set key=value")
	cmd.AddCommand(updateCmd)
	cmd.AddCommand(
		buildHTTPChannelCmd(),
		buildTelegramChannelCmd(),
		buildSlackChannelCmd(),
		buildDiscordChannelCmd(),
		buildWhatsAppChannelCmd(),
		buildWhatsAppWebChannelCmd(),
	)
	return cmd
}

func buildHTTPChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "http", Short: "Inspect the always-on HTTP channel"}
	addAdapterCommonCommands(cmd, "http", false)
	return cmd
}

func buildTelegramChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "telegram", Short: "Configure Telegram channel"}
	var token, agentID string
	safety := addSafetyFlags(cmd, "groups")
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure Telegram credentials, routing, and activation safety",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "token", token)
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addSafetySettings(cmd, settings, safety)
			return patchChannelSettings("telegram", settings)
		},
	}
	configure.Flags().StringVar(&token, "token", "", "Telegram bot token")
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	addSafetyFlagsTo(configure, &safety, "groups")
	cmd.AddCommand(configure)
	addAdapterCommonCommands(cmd, "telegram", false)
	return cmd
}

func buildSlackChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "slack", Short: "Configure Slack channel"}
	var botToken, appToken, agentID string
	safety := addSafetyFlags(cmd, "channels")
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure Slack credentials, routing, and activation safety",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "bot-token", botToken, "bot_token")
			addChangedString(cmd, settings, "app-token", appToken, "app_token")
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addSafetySettings(cmd, settings, safety)
			return patchChannelSettings("slack", settings)
		},
	}
	configure.Flags().StringVar(&botToken, "bot-token", "", "Slack bot token (xoxb-...)")
	configure.Flags().StringVar(&appToken, "app-token", "", "Slack app token (xapp-...)")
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	addSafetyFlagsTo(configure, &safety, "channels")
	cmd.AddCommand(configure)
	addAdapterCommonCommands(cmd, "slack", false)
	return cmd
}

func buildDiscordChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "discord", Short: "Configure Discord channel"}
	var token, agentID, guildID string
	safety := addSafetyFlags(cmd, "servers")
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure Discord credentials, routing, and activation safety",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "token", token)
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addChangedString(cmd, settings, "guild", guildID, "guild_id")
			addSafetySettings(cmd, settings, safety)
			return patchChannelSettings("discord", settings)
		},
	}
	configure.Flags().StringVar(&token, "token", "", "Discord bot token")
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	configure.Flags().StringVar(&guildID, "guild", "", "Optional Discord guild ID")
	addSafetyFlagsTo(configure, &safety, "servers")
	cmd.AddCommand(configure)
	addAdapterCommonCommands(cmd, "discord", false)
	return cmd
}

func buildWhatsAppChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "whatsapp", Short: "Configure official WhatsApp Cloud API channel"}
	var phoneNumberID, accessToken, verifyToken, appSecret, agentID string
	safety := addSafetyFlags(cmd, "groups")
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure WhatsApp Cloud API credentials, routing, and activation safety",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "phone-number-id", phoneNumberID, "phone_number_id")
			addChangedString(cmd, settings, "access-token", accessToken, "access_token")
			addChangedString(cmd, settings, "verify-token", verifyToken, "verify_token")
			addChangedString(cmd, settings, "app-secret", appSecret, "app_secret")
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addSafetySettings(cmd, settings, safety)
			return patchChannelSettings("whatsapp", settings)
		},
	}
	configure.Flags().StringVar(&phoneNumberID, "phone-number-id", "", "Meta WhatsApp phone number ID")
	configure.Flags().StringVar(&accessToken, "access-token", "", "Meta WhatsApp access token")
	configure.Flags().StringVar(&verifyToken, "verify-token", "", "Meta webhook verify token")
	configure.Flags().StringVar(&appSecret, "app-secret", "", "Meta app secret for webhook HMAC verification")
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	addSafetyFlagsTo(configure, &safety, "groups")
	cmd.AddCommand(configure)
	addAdapterCommonCommands(cmd, "whatsapp", false)
	return cmd
}

func buildWhatsAppWebChannelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "whatsapp-web",
		Aliases: []string{"wa-web", "whatsappweb"},
		Short:   "Configure and pair WhatsApp Web",
	}

	var agentID, triggerPhrase, allowedChats, allowedSenders, command, args, sessionDir, accountID string
	var allowGroups bool
	var waitSeconds int
	pairCmd := &cobra.Command{
		Use:   "pair",
		Short: "Start WhatsApp Web pairing and print the QR payload",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(agentID) == "" {
				return fmt.Errorf("--agent is required")
			}
			body, _ := json.Marshal(map[string]any{
				"agent_id":           agentID,
				"trigger_phrase":     triggerPhrase,
				"ignore_groups":      !allowGroups,
				"allowed_chat_ids":   allowedChats,
				"allowed_sender_ids": allowedSenders,
			})
			if _, err := apiCall("POST", "/channels/whatsapp_web/pair", body); err != nil {
				return err
			}
			if !outputJSON {
				fmt.Println("WhatsApp Web pairing started.")
				fmt.Printf("Safety: trigger phrase %q, groups %s.\n", triggerPhrase, groupPolicyLabel(!allowGroups))
			}
			ch, err := waitForChannelQR("whatsapp_web", waitSeconds)
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(ch)
			}
			printWhatsAppWebQR(ch)
			return nil
		},
	}
	pairCmd.Flags().StringVar(&agentID, "agent", "", "Agent ID to route WhatsApp messages to")
	pairCmd.Flags().StringVar(&triggerPhrase, "trigger", "!soulacy", "Only messages starting with this phrase trigger the agent")
	pairCmd.Flags().BoolVar(&allowGroups, "allow-groups", false, "Allow group chats to trigger the agent")
	pairCmd.Flags().StringVar(&allowedChats, "allowed-chats", "", "Comma-separated WhatsApp chat JIDs to allow")
	pairCmd.Flags().StringVar(&allowedSenders, "allowed-senders", "", "Comma-separated WhatsApp sender JIDs to allow")
	pairCmd.Flags().IntVar(&waitSeconds, "wait", 20, "Seconds to wait for a QR payload")
	cmd.AddCommand(pairCmd)
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Configure WhatsApp Web sidecar, routing, and activation safety",
		RunE: func(cmd *cobra.Command, argsList []string) error {
			settings := map[string]any{}
			addChangedString(cmd, settings, "agent", agentID, "agent_id")
			addChangedString(cmd, settings, "command", command)
			addChangedString(cmd, settings, "args", args)
			addChangedString(cmd, settings, "session-dir", sessionDir, "session_dir")
			addChangedString(cmd, settings, "account-id", accountID, "account_id")
			addChangedString(cmd, settings, "trigger", triggerPhrase, "trigger_phrase")
			if cmd.Flags().Changed("allow-groups") {
				settings["ignore_groups"] = !allowGroups
			}
			addChangedString(cmd, settings, "allowed-chats", allowedChats, "allowed_chat_ids")
			addChangedString(cmd, settings, "allowed-senders", allowedSenders, "allowed_sender_ids")
			return patchChannelSettings("whatsapp_web", settings)
		},
	}
	configure.Flags().StringVar(&agentID, "agent", "", "Default agent ID")
	configure.Flags().StringVar(&command, "command", "", "Runtime executable; defaults to node")
	configure.Flags().StringVar(&args, "args", "", "Sidecar args, e.g. scripts/whatsapp-web-sidecar.mjs")
	configure.Flags().StringVar(&sessionDir, "session-dir", "", "Where QR-linked auth state is stored")
	configure.Flags().StringVar(&accountID, "account-id", "", "Session subdirectory for this linked account")
	configure.Flags().StringVar(&triggerPhrase, "trigger", "!soulacy", "Only messages starting with this phrase trigger the agent")
	configure.Flags().BoolVar(&allowGroups, "allow-groups", false, "Allow group chats to trigger the agent")
	configure.Flags().StringVar(&allowedChats, "allowed-chats", "", "Comma-separated WhatsApp chat JIDs to allow")
	configure.Flags().StringVar(&allowedSenders, "allowed-senders", "", "Comma-separated WhatsApp sender JIDs to allow")
	cmd.AddCommand(configure)

	addAdapterCommonCommands(cmd, "whatsapp_web", true)
	return cmd
}

type channelSafetyFlags struct {
	trigger         string
	allowGroups     bool
	allowedChats    string
	allowedUsers    string
	groupNounPlural string
}

func addSafetyFlags(_ *cobra.Command, groupNounPlural string) channelSafetyFlags {
	return channelSafetyFlags{
		trigger:         "!soulacy",
		groupNounPlural: groupNounPlural,
	}
}

func addSafetyFlagsTo(cmd *cobra.Command, safety *channelSafetyFlags, groupNounPlural string) {
	cmd.Flags().StringVar(&safety.trigger, "trigger", safety.trigger, "Only messages starting with this phrase trigger the agent")
	cmd.Flags().BoolVar(&safety.allowGroups, "allow-groups", false, "Allow "+groupNounPlural+" to trigger the agent")
	cmd.Flags().StringVar(&safety.allowedChats, "allowed-chats", "", "Comma-separated platform chat/channel IDs to allow")
	cmd.Flags().StringVar(&safety.allowedUsers, "allowed-users", "", "Comma-separated platform user/sender IDs to allow")
}

func addSafetySettings(cmd *cobra.Command, settings map[string]any, safety channelSafetyFlags) {
	addChangedString(cmd, settings, "trigger", safety.trigger, "trigger_phrase")
	if cmd.Flags().Changed("allow-groups") {
		settings["ignore_groups"] = !safety.allowGroups
	}
	addChangedString(cmd, settings, "allowed-chats", safety.allowedChats, "allowed_chat_ids")
	addChangedString(cmd, settings, "allowed-users", safety.allowedUsers, "allowed_user_ids")
}

func addAdapterCommonCommands(cmd *cobra.Command, channelID string, includeQR bool) {
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show channel adapter status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ch, err := getChannel(channelID)
			if err != nil {
				return err
			}
			if outputJSON {
				return printJSON(ch)
			}
			printChannelSummary(ch)
			if includeQR {
				printWhatsAppWebQR(ch)
			}
			return nil
		},
	})
	if channelID == "http" {
		return
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "enable",
		Short: "Enable this channel",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/channels/"+channelID+"/enable", nil)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "disable",
		Short: "Disable this channel",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/channels/"+channelID+"/disable", nil)
		},
	})
}

func addChangedString(cmd *cobra.Command, settings map[string]any, flagName string, value string, settingName ...string) {
	if !cmd.Flags().Changed(flagName) {
		return
	}
	key := flagName
	if len(settingName) > 0 {
		key = settingName[0]
	}
	settings[key] = value
}

func patchChannelSettings(channelID string, settings map[string]any) error {
	if len(settings) == 0 {
		return fmt.Errorf("no settings provided")
	}
	body, _ := json.Marshal(map[string]any{"settings": settings})
	return apiPatch("/channels/"+channelID, body)
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

func apiPatch(path string, body []byte) error {
	if body == nil {
		body = []byte("{}")
	}
	data, err := apiCall("PATCH", path, body)
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

func getChannel(id string) (map[string]any, error) {
	data, err := apiCall("GET", "/channels", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	for _, ch := range result.Channels {
		if fmt.Sprint(ch["id"]) == id {
			return ch, nil
		}
	}
	return nil, fmt.Errorf("channel %q not found", id)
}

func waitForChannelQR(id string, seconds int) (map[string]any, error) {
	if seconds < 0 {
		seconds = 0
	}
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		ch, err := getChannel(id)
		if err != nil {
			return nil, err
		}
		status := mapValue(ch, "status")
		if stringValue(status, "qr_code") != "" || boolValue(status, "connected") || time.Now().After(deadline) {
			return ch, nil
		}
		time.Sleep(time.Second)
	}
}

func printChannelSummary(ch map[string]any) {
	status := mapValue(ch, "status")
	settings := mapValue(ch, "settings")
	fmt.Printf("%s (%s)\n", fmt.Sprint(ch["name"]), fmt.Sprint(ch["id"]))
	fmt.Printf("  enabled:    %v\n", ch["enabled"])
	fmt.Printf("  configured: %v\n", ch["configured"])
	fmt.Printf("  connected:  %v\n", boolValue(status, "connected"))
	if detail := stringValue(status, "detail"); detail != "" {
		fmt.Printf("  detail:     %s\n", detail)
	}
	if fmt.Sprint(ch["id"]) == "whatsapp_web" {
		trigger := stringValue(settings, "trigger_phrase")
		if trigger == "" {
			trigger = "!soulacy"
		}
		fmt.Printf("  trigger:    %s\n", trigger)
		fmt.Printf("  groups:     %s\n", groupPolicyLabel(parseBoolSetting(settings["ignore_groups"], true)))
	}
}

func printWhatsAppWebQR(ch map[string]any) {
	status := mapValue(ch, "status")
	qr := stringValue(status, "qr_code")
	if qr == "" {
		if boolValue(status, "connected") {
			fmt.Println("WhatsApp Web is already connected.")
		} else {
			fmt.Println("No QR payload available yet. Run `sy channel whatsapp-web status` again, or use the Channels GUI for a rendered QR.")
		}
		return
	}
	fmt.Println()
	fmt.Println("QR payload:")
	fmt.Println(qr)
	fmt.Println()
	fmt.Println("Use the Channels GUI for a rendered QR, or render this payload in a trusted terminal QR tool.")
}

func printJSON(v any) error {
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(pretty))
	return nil
}

func parseCLIValue(value string) any {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "true":
		return true
	case "false":
		return false
	default:
		return value
	}
}

func mapValue(m map[string]any, key string) map[string]any {
	raw, ok := m[key]
	if !ok {
		return map[string]any{}
	}
	asMap, ok := raw.(map[string]any)
	if ok {
		return asMap
	}
	return map[string]any{}
}

func stringValue(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func boolValue(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		return parseBoolSetting(v, false)
	}
	return false
}

func parseBoolSetting(v any, fallback bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "1", "on":
			return true
		case "false", "no", "0", "off":
			return false
		}
	}
	return fallback
}

func groupPolicyLabel(ignoreGroups bool) string {
	if ignoreGroups {
		return "ignored"
	}
	return "allowed"
}
