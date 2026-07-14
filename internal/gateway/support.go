package gateway

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/supportbundle"
)

func (s *Server) handleSupportBundle(c *fiber.Ctx) error {
	var buf bytes.Buffer
	_, err := supportbundle.Write(&buf, s.supportBundleOptions(c))
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "support bundle: "+err.Error())
	}
	filename := fmt.Sprintf("soulacy-support-%s.zip", time.Now().Format("20060102-150405"))
	c.Set(fiber.HeaderContentType, "application/zip")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, filename))
	return c.Send(buf.Bytes())
}

func (s *Server) supportBundleOptions(c *fiber.Ctx) supportbundle.Options {
	ws, _ := config.ResolveWorkspace()
	agentDirs := append([]string(nil), s.cfg.AgentDirs...)
	if len(agentDirs) == 0 && ws.Agents != "" {
		agentDirs = []string{ws.Agents}
	}
	logDirs := supportLogDirs(s.cfg, ws)
	return supportbundle.Options{
		GatewayURL: gatewayRequestURL(c),
		ConfigPath: supportConfigPath(s.cfgPath, ws),
		AgentDirs:  agentDirs,
		LogDirs:    logDirs,
		Workspace:  gatewayWorkspaceMap(ws),
		Doctor: fiber.Map{
			"providers": s.providerDoctorChecks(c),
			"channels":  s.channelDoctorChecks(),
		},
		ExtraJSON: map[string]any{
			"readiness":   s.readinessPayload(c),
			"run_ledger":  s.supportRunLedger(),
			"admin_audit": s.supportAdminAudit(),
			"release": fiber.Map{
				"version":         config.Version,
				"update_manifest": s.updateManifestSource(),
				"updates_ready":   s.updateManifestSource() != "",
				"dry_run_command": "sy update install --dry-run",
				"install_command": "sy update install --yes",
			},
		},
	}
}

func (s *Server) supportAdminAudit() fiber.Map {
	if s == nil || s.actions == nil {
		return fiber.Map{
			"available": false,
			"reason":    "action log disabled",
		}
	}
	q, ok := s.actions.(eventQuerier)
	if !ok {
		return fiber.Map{
			"available": false,
			"reason":    "durable action log backend does not support event queries",
		}
	}
	events, err := q.QueryEvents(adminAuditAgentID, "", 1000, adminAuditEventTypes())
	if err != nil {
		return fiber.Map{
			"available": false,
			"reason":    err.Error(),
		}
	}
	records := make([]adminAuditRecord, 0, len(events))
	for _, ev := range events {
		rec, ok := adminAuditRecordFromPayload(ev.Payload)
		if !ok {
			continue
		}
		if rec.Timestamp.IsZero() {
			rec.Timestamp = ev.Timestamp
		}
		records = append(records, rec)
	}
	return fiber.Map{
		"available": true,
		"source":    "action-log",
		"count":     len(records),
		"events":    records,
	}
}

func (s *Server) supportRunLedger() fiber.Map {
	if s == nil || s.actions == nil {
		return fiber.Map{
			"available": false,
			"reason":    "action log disabled",
		}
	}
	q, ok := s.actions.(eventQuerier)
	if !ok {
		return fiber.Map{
			"available": false,
			"reason":    "durable action log backend does not support event queries",
		}
	}
	events, err := q.QueryEvents("", "", 10000, runLedgerEventTypes())
	if err != nil {
		return fiber.Map{
			"available": false,
			"reason":    err.Error(),
		}
	}
	rows := s.buildRunLedger(events, 250)
	return fiber.Map{
		"available":   true,
		"source":      "action-log",
		"event_count": len(events),
		"count":       len(rows),
		"runs":        rows,
	}
}

func supportConfigPath(cfgPath string, ws config.Paths) string {
	if strings.TrimSpace(cfgPath) != "" {
		return cfgPath
	}
	if ws.ConfigFile != "" {
		if _, err := os.Stat(ws.ConfigFile); err == nil {
			return ws.ConfigFile
		}
	}
	return ""
}

func supportLogDirs(cfg *config.Config, ws config.Paths) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	if cfg != nil && strings.TrimSpace(cfg.Log.File) != "" {
		add(filepath.Dir(cfg.Log.File))
	}
	if ws.Logs != "" {
		add(ws.Logs)
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".soulacy", "logs"))
	}
	return dirs
}

func gatewayWorkspaceMap(ws config.Paths) map[string]string {
	if ws.Root == "" {
		return nil
	}
	return map[string]string{
		"root":       ws.Root,
		"agents":     ws.Agents,
		"logs":       ws.Logs,
		"skills":     ws.Skills,
		"mcpServers": filepath.Join(ws.Root, "mcp-servers"),
	}
}

func gatewayRequestURL(c *fiber.Ctx) string {
	if c == nil {
		return ""
	}
	proto := c.Protocol()
	host := c.Hostname()
	if host == "" {
		host = string(c.Request().Host())
	}
	if proto == "" || host == "" {
		return ""
	}
	return proto + "://" + host
}
