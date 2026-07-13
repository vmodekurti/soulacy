package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/mcp"
)

type browserAutomationReadiness struct {
	Status      string                    `json:"status"`
	Score       int                       `json:"score"`
	Ready       int                       `json:"ready"`
	Total       int                       `json:"total"`
	Checks      []browserAutomationCheck  `json:"checks"`
	Sidecars    []browserAutomationServer `json:"sidecars"`
	NextActions []executorAction          `json:"next_actions,omitempty"`
}

type browserAutomationCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Href   string `json:"href,omitempty"`
}

type browserAutomationServer struct {
	ID       string   `json:"id"`
	Mode     string   `json:"mode"`
	Status   string   `json:"status"`
	Command  string   `json:"command,omitempty"`
	Tools    []string `json:"tools,omitempty"`
	Headless bool     `json:"headless"`
	Detail   string   `json:"detail,omitempty"`
}

func (s *Server) handleBrowserStatus(c *fiber.Ctx) error {
	return c.JSON(s.browserAutomationReadiness())
}

func (s *Server) browserAutomationReadiness() browserAutomationReadiness {
	servers := s.browserAutomationServers()
	hasSidecar, connectedSidecar, hasHeadless, hasTools := false, false, false, false
	for _, srv := range servers {
		hasSidecar = true
		if srv.Status == "ok" {
			connectedSidecar = true
		}
		if srv.Headless {
			hasHeadless = true
		}
		if len(srv.Tools) > 0 {
			hasTools = true
		}
	}
	actionLog := s != nil && s.actions != nil
	checks := []browserAutomationCheck{
		{
			Key:    "sidecar",
			Label:  "MCP sidecar",
			Status: statusIf(hasSidecar, "ok", "warn"),
			Detail: nextIf(hasSidecar, "A browser-capable MCP server is configured.", "Add the Browser headless MCP quick-start from the MCP page."),
			Href:   "#mcp",
		},
		{
			Key:    "connected",
			Label:  "Connected tools",
			Status: statusIf(connectedSidecar && hasTools, "ok", "warn"),
			Detail: nextIf(connectedSidecar && hasTools, "Browser tools are connected and available to agents.", "Restart the gateway or fix the MCP server until browser tools connect."),
			Href:   "#mcp",
		},
		{
			Key:    "headless",
			Label:  "Headless default",
			Status: statusIf(hasHeadless, "ok", "warn"),
			Detail: nextIf(hasHeadless, "At least one browser sidecar runs headless for scheduled/background agents.", "Use the Browser headless quick-start for unattended agents; keep visible browser only for debugging."),
			Href:   "#mcp",
		},
		{
			Key:    "trace",
			Label:  "Trace replay",
			Status: statusIf(actionLog, "ok", "warn"),
			Detail: nextIf(actionLog, "Action logging can reconstruct browser traces, screenshots, and policy decisions.", "Enable the action log so browser runs can be replayed after failures."),
			Href:   "#browser",
		},
		{
			Key:    "policy",
			Label:  "Domain policy",
			Status: "ok",
			Detail: "The Browser page shows each agent's allow/deny/prompt policy next to the trace.",
			Href:   "#browser",
		},
	}

	ready := 0
	actions := make([]executorAction, 0, 3)
	for _, chk := range checks {
		if chk.Status == "ok" {
			ready++
			continue
		}
		if len(actions) < 3 {
			actions = append(actions, executorAction{Label: chk.Label, Detail: chk.Detail, Href: chk.Href})
		}
	}
	score := int(float64(ready) / float64(len(checks)) * 100)
	status := "ok"
	if score < 80 {
		status = "warn"
	}
	if ready == 0 {
		status = "fail"
	}
	return browserAutomationReadiness{
		Status:      status,
		Score:       score,
		Ready:       ready,
		Total:       len(checks),
		Checks:      checks,
		Sidecars:    servers,
		NextActions: actions,
	}
}

func (s *Server) browserAutomationServers() []browserAutomationServer {
	if s == nil || s.mcp == nil {
		return nil
	}
	snap := s.mcp.ServersSnapshot()
	out := make([]browserAutomationServer, 0, len(snap))
	for _, srv := range snap {
		if !looksLikeBrowserServer(srv) {
			continue
		}
		tools := browserToolNames(srv.Tools)
		status := "warn"
		if srv.Connected && len(tools) > 0 {
			status = "ok"
		}
		out = append(out, browserAutomationServer{
			ID:       srv.ID,
			Mode:     browserServerMode(srv),
			Status:   status,
			Command:  strings.TrimSpace(srv.Command + " " + strings.Join(srv.Args, " ")),
			Tools:    tools,
			Headless: browserServerHeadless(srv),
			Detail:   srv.Detail,
		})
	}
	return out
}

func looksLikeBrowserServer(srv mcp.ServerStatus) bool {
	hay := strings.ToLower(strings.Join(append([]string{srv.ID, srv.Command, srv.URL}, srv.Args...), " "))
	if strings.Contains(hay, "playwright") || strings.Contains(hay, "puppeteer") || strings.Contains(hay, "browser") {
		return true
	}
	return len(browserToolNames(srv.Tools)) > 0
}

func browserToolNames(tools []mcp.ToolSummary) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		hay := strings.ToLower(t.Name + " " + t.FullName + " " + t.Description)
		if strings.Contains(hay, "browser") || strings.Contains(hay, "page") || strings.Contains(hay, "screenshot") || strings.Contains(hay, "navigate") {
			out = append(out, t.FullName)
		}
	}
	if len(out) > 8 {
		return out[:8]
	}
	return out
}

func browserServerHeadless(srv mcp.ServerStatus) bool {
	hay := strings.ToLower(strings.Join(append([]string{srv.ID, srv.Command}, srv.Args...), " "))
	return strings.Contains(hay, "--headless") || strings.Contains(hay, "headless")
}

func browserServerMode(srv mcp.ServerStatus) string {
	if browserServerHeadless(srv) {
		return "headless"
	}
	if srv.Transport == "http" || srv.URL != "" {
		return "remote"
	}
	return "visible"
}

func parityBrowserAutomation(b browserAutomationReadiness) parityArea {
	if b.Status == "ok" {
		return parityArea{Key: "browser", Label: "Browser Automation", Status: "ok", Score: maxInt(b.Score, 82), Detail: "Browser automation has MCP sidecar readiness, headless setup, policy visibility, and trace replay.", Next: "Add live session viewing and reusable browser skills for the last mile of parity.", Benchmark: "OpenClaw/Hermes", Href: "#browser"}
	}
	if b.Score >= 60 {
		return parityArea{Key: "browser", Label: "Browser Automation", Status: "warn", Score: b.Score, Detail: "Browser automation is partially ready, but one or more sidecar, headless, connection, or trace checks need attention.", Next: firstBrowserNextAction(b), Benchmark: "OpenClaw/Hermes", Href: "#browser"}
	}
	return parityArea{Key: "browser", Label: "Browser Automation", Status: "fail", Score: maxInt(b.Score, 30), Detail: "Browser automation is not production-ready yet.", Next: firstBrowserNextAction(b), Benchmark: "OpenClaw/Hermes", Href: "#mcp"}
}

func firstBrowserNextAction(b browserAutomationReadiness) string {
	if len(b.NextActions) > 0 && strings.TrimSpace(b.NextActions[0].Detail) != "" {
		return b.NextActions[0].Detail
	}
	return "Add Browser headless from the MCP page, restart, and verify a trace appears on the Browser page."
}
