package gateway

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/config"
)

type readinessItem struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	Href     string `json:"href,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

type readinessSummary struct {
	Status          string `json:"status"`
	Score           int    `json:"score"`
	ReadyItems      int    `json:"ready_items"`
	WarningItems    int    `json:"warning_items"`
	BlockerItems    int    `json:"blocker_items"`
	TotalItems      int    `json:"total_items"`
	ProvidersReady  int    `json:"providers_ready"`
	ChannelsReady   int    `json:"channels_ready"`
	Agents          int    `json:"agents"`
	EnabledAgents   int    `json:"enabled_agents"`
	ChatAgents      int    `json:"chat_agents"`
	ScheduledAgents int    `json:"scheduled_agents"`
	LearningAgents  int    `json:"learning_agents"`
	Templates       int    `json:"templates"`
	UpdatesReady    bool   `json:"updates_ready"`
}

// handleReadiness returns Soulacy's product journey state in one place:
// setup, build, delivery, monitoring, learning, and release readiness. It is
// intentionally platform-wide, not tied to one agent, so every agent type gets
// the same go-to-market guardrails.
func (s *Server) handleReadiness(c *fiber.Ctx) error {
	return c.JSON(s.readinessPayload(c))
}

func (s *Server) readinessPayload(c *fiber.Ctx) fiber.Map {
	providers := s.providerDoctorChecks(c)
	channels := s.channelDoctorChecks()
	templates, _ := s.templatesCatalog().List()
	s.applyTemplateRuntimeDefaults(templates)

	agents, enabledAgents, chatAgents, scheduledAgents, learningAgents := s.agentReadinessCounts()
	providersReady := countDoctorProviders(providers, "ok", "warn")
	channelsReady := countDoctorChannels(channels, "ok", "warn")
	usableOutbound := countUsableOutboundChannels(channels)
	updateManifest := s.updateManifestSource()

	journey := []readinessItem{
		providerReadinessItem(providers, providersReady),
		{
			Key:    "first_run",
			Label:  "First Run",
			Status: statusFromBool(len(templates) > 0),
			Detail: detailTemplates(len(templates)),
			Href:   "#onboarding",
		},
		{
			Key:    "studio",
			Label:  "Studio",
			Status: statusFromBool(len(templates) > 0),
			Detail: detailStudio(len(templates)),
			Href:   "#studio",
		},
		agentReadinessItem(agents, enabledAgents, chatAgents, scheduledAgents),
		channelReadinessItem(channels, channelsReady, usableOutbound),
		{
			Key:    "monitor",
			Label:  "Runs",
			Status: "ok",
			Detail: "Run history, replay, and Studio debug links are available.",
			Href:   "#activity",
		},
		learningReadinessItem(learningAgents, enabledAgents),
		{
			Key:    "release",
			Label:  "Production Launch",
			Status: releaseStatus(providersReady, enabledAgents, usableOutbound, updateManifest),
			Detail: releaseDetail(providersReady, enabledAgents, usableOutbound, updateManifest),
			Href:   "#config",
		},
	}

	next := make([]readinessItem, 0)
	for i := range journey {
		switch journey[i].Status {
		case "ok":
		case "warn":
			journey[i].Priority = 20 + i
			next = append(next, journey[i])
		default:
			journey[i].Priority = 10 + i
			next = append(next, journey[i])
		}
	}
	score := readinessScore(journey)
	readyItems, warningItems, blockerItems := readinessStatusCounts(journey)
	sort.SliceStable(next, func(i, j int) bool {
		return next[i].Priority < next[j].Priority
	})
	if len(next) > 5 {
		next = next[:5]
	}

	status := "ready"
	for _, item := range journey {
		if item.Status == "fail" {
			status = "needs_setup"
			break
		}
		if item.Status == "warn" && status != "needs_setup" {
			status = "at_risk"
		}
	}

	return fiber.Map{
		"summary": readinessSummary{
			Status:          status,
			Score:           score,
			ReadyItems:      readyItems,
			WarningItems:    warningItems,
			BlockerItems:    blockerItems,
			TotalItems:      len(journey),
			ProvidersReady:  providersReady,
			ChannelsReady:   channelsReady,
			Agents:          agents,
			EnabledAgents:   enabledAgents,
			ChatAgents:      chatAgents,
			ScheduledAgents: scheduledAgents,
			LearningAgents:  learningAgents,
			Templates:       len(templates),
			UpdatesReady:    updateManifest != "",
		},
		"journey":      journey,
		"next_actions": next,
		"providers":    providers,
		"channels":     channels,
		"release": fiber.Map{
			"version":         strings.TrimSpace(config.Version),
			"update_manifest": updateManifest,
			"updates_ready":   updateManifest != "",
			"update_hint":     updateManifestHint(updateManifest),
			"install_command": "sy update install --yes",
			"dry_run_command": "sy update install --dry-run",
		},
	}
}

func (s *Server) updateManifestSource() string {
	if s != nil && s.cfg != nil {
		if src := strings.TrimSpace(s.cfg.Updates.ManifestURL); src != "" {
			return src
		}
	}
	if src := strings.TrimSpace(os.Getenv("SOULACY_UPDATE_MANIFEST")); src != "" {
		return src
	}
	return ""
}

func readinessStatusCounts(items []readinessItem) (ready, warnings, blockers int) {
	for _, item := range items {
		switch item.Status {
		case "ok":
			ready++
		case "warn":
			warnings++
		default:
			blockers++
		}
	}
	return
}

func readinessScore(items []readinessItem) int {
	if len(items) == 0 {
		return 0
	}
	points := 0
	for _, item := range items {
		switch item.Status {
		case "ok":
			points += 100
		case "warn":
			points += 55
		}
	}
	return points / len(items)
}

func (s *Server) agentReadinessCounts() (agents, enabled, chat, scheduled, learning int) {
	if s.loader == nil {
		return
	}
	for _, def := range s.loader.All() {
		if def == nil || s.loader.IsBuiltin(def.ID) {
			continue
		}
		agents++
		if def.Enabled {
			enabled++
		}
		if def.Trigger == "cron" || (def.Schedule != nil && def.Schedule.Cron != "") {
			scheduled++
		}
		if def.Learning.Enabled || def.Learning.AutoPropose {
			learning++
		}
		if agentHasChatSurface(def.Trigger, def.Channels, def.Surfaces) {
			chat++
		}
	}
	return
}

func agentHasChatSurface(trigger any, channels, surfaces []string) bool {
	for _, s := range surfaces {
		if strings.EqualFold(s, "chat") {
			return true
		}
	}
	for _, ch := range channels {
		if strings.EqualFold(ch, "http") || strings.EqualFold(ch, "chat") {
			return true
		}
	}
	triggerText := strings.ToLower(strings.TrimSpace(fmt.Sprint(trigger)))
	return triggerText == "channel" || triggerText == "webhook"
}

func countDoctorProviders(checks []doctorProviderCheck, usable ...string) int {
	return countStatuses(len(checks), func(i int) string { return checks[i].Status }, usable...)
}

func countDoctorChannels(checks []doctorChannelCheck, usable ...string) int {
	return countStatuses(len(checks), func(i int) string { return checks[i].Status }, usable...)
}

func countStatuses(n int, statusAt func(int) string, usable ...string) int {
	allowed := map[string]bool{}
	for _, s := range usable {
		allowed[s] = true
	}
	count := 0
	for i := 0; i < n; i++ {
		if allowed[statusAt(i)] {
			count++
		}
	}
	return count
}

func countUsableOutboundChannels(checks []doctorChannelCheck) int {
	count := 0
	for _, ch := range checks {
		if ch.ID == "http" {
			continue
		}
		if ch.Enabled && ch.Configured && (ch.Connected || ch.Status == "ok" || ch.Status == "warn") {
			count++
		}
	}
	return count
}

func statusFromBool(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}

func providerReadinessItem(providers []doctorProviderCheck, ready int) readinessItem {
	if ready > 0 {
		return readinessItem{
			Key:    "providers",
			Label:  "Model Providers",
			Status: "ok",
			Detail: plural(ready, "provider") + " ready for agent runs.",
			Href:   "#providers",
		}
	}
	status := "fail"
	if len(providers) > 0 {
		status = "warn"
	}
	return readinessItem{
		Key:    "providers",
		Label:  "Model Providers",
		Status: status,
		Detail: "Connect and test at least one LLM provider before building production agents.",
		Href:   "#providers",
	}
}

func agentReadinessItem(agents, enabled, chat, scheduled int) readinessItem {
	if enabled > 0 {
		detail := plural(enabled, "enabled agent")
		if chat > 0 || scheduled > 0 {
			detail += " · " + strconv.Itoa(chat) + " chat-ready · " + strconv.Itoa(scheduled) + " scheduled"
		}
		return readinessItem{Key: "agents", Label: "Deployed Agents", Status: "ok", Detail: detail, Href: "#agents"}
	}
	if agents > 0 {
		return readinessItem{Key: "agents", Label: "Deployed Agents", Status: "warn", Detail: "Agents exist, but none are enabled.", Href: "#agents"}
	}
	return readinessItem{Key: "agents", Label: "Deployed Agents", Status: "fail", Detail: "Create or instantiate an agent from Studio.", Href: "#studio"}
}

func channelReadinessItem(channels []doctorChannelCheck, ready, outbound int) readinessItem {
	if outbound > 0 {
		return readinessItem{
			Key:    "channels",
			Label:  "Delivery Channels",
			Status: "ok",
			Detail: plural(outbound, "outbound channel") + " ready; use channel tests before scheduling.",
			Href:   "#channels",
		}
	}
	if ready > 0 {
		return readinessItem{
			Key:    "channels",
			Label:  "Delivery Channels",
			Status: "warn",
			Detail: "HTTP is available, but no outbound destination is ready for cron or notification agents.",
			Href:   "#channels",
		}
	}
	if len(channels) > 0 {
		return readinessItem{Key: "channels", Label: "Delivery Channels", Status: "warn", Detail: "Channels are present but need credentials, mappings, or a restart.", Href: "#channels"}
	}
	return readinessItem{Key: "channels", Label: "Delivery Channels", Status: "fail", Detail: "Configure Telegram, Slack, Discord, WhatsApp, or another outbound channel.", Href: "#channels"}
}

func learningReadinessItem(learningAgents, enabledAgents int) readinessItem {
	if learningAgents > 0 {
		return readinessItem{
			Key:    "learning",
			Label:  "Learning Loop",
			Status: "ok",
			Detail: plural(learningAgents, "agent") + " can create reviewable learning proposals.",
			Href:   "#memory",
		}
	}
	if enabledAgents > 0 {
		return readinessItem{
			Key:    "learning",
			Label:  "Learning Loop",
			Status: "warn",
			Detail: "Learning is optional, but production agents should capture reviewable lessons from successful runs.",
			Href:   "#memory",
		}
	}
	return readinessItem{
		Key:    "learning",
		Label:  "Learning Loop",
		Status: "warn",
		Detail: "Enable learning after your first production agent is running.",
		Href:   "#memory",
	}
}

func releaseStatus(providersReady, enabledAgents, outbound int, updateManifest string) string {
	if providersReady == 0 || enabledAgents == 0 {
		return "fail"
	}
	if outbound == 0 || strings.TrimSpace(updateManifest) == "" {
		return "warn"
	}
	return "ok"
}

func releaseDetail(providersReady, enabledAgents, outbound int, updateManifest string) string {
	if providersReady == 0 {
		return "A production launch needs at least one tested model provider."
	}
	if enabledAgents == 0 {
		return "A production launch needs at least one enabled agent."
	}
	if outbound == 0 {
		return "Interactive runs can work now; add an outbound channel for cron and alert agents."
	}
	if strings.TrimSpace(updateManifest) == "" {
		return "Core runtime is usable; configure updates.manifest_url or SOULACY_UPDATE_MANIFEST for production upgrades."
	}
	return "Core launch path is ready: provider, enabled agent, delivery, monitoring, diagnostics, and update manifest."
}

func updateManifestHint(src string) string {
	if strings.TrimSpace(src) == "" {
		return "Set updates.manifest_url in config.yaml or SOULACY_UPDATE_MANIFEST to enable sy update check/install."
	}
	return "Run sy update check before rollout and sy update install --dry-run to verify the artifact."
}

func detailTemplates(count int) string {
	if count == 0 {
		return "No starter templates were loaded; first-run setup has nothing guided to deploy."
	}
	return plural(count, "template") + " available for guided setup."
}

func detailStudio(templateCount int) string {
	if templateCount == 0 {
		return "Studio is available, but starter templates are missing."
	}
	return "Studio is the command center for designing, testing, debugging, and saving agents."
}

func plural(n int, label string) string {
	if n == 1 {
		return "1 " + label
	}
	return strconv.Itoa(n) + " " + strings.TrimSpace(label) + "s"
}
