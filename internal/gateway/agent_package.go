package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/pkgregistry"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

type agentPackageFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Text   string `json:"text,omitempty"`
}

type agentPackageSecret struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type agentPackageManifest struct {
	AgentID       string               `json:"agent_id"`
	Name          string               `json:"name"`
	Version       string               `json:"version,omitempty"`
	Description   string               `json:"description,omitempty"`
	Trigger       string               `json:"trigger"`
	Surfaces      []string             `json:"surfaces,omitempty"`
	Providers     []string             `json:"providers,omitempty"`
	Models        []string             `json:"models,omitempty"`
	Channels      []string             `json:"channels,omitempty"`
	Skills        []string             `json:"skills,omitempty"`
	Knowledge     []string             `json:"knowledge,omitempty"`
	PeerAgents    []string             `json:"peer_agents,omitempty"`
	Builtins      []string             `json:"builtins,omitempty"`
	MCPServers    []string             `json:"mcp_servers,omitempty"`
	MCPTools      []string             `json:"mcp_tools,omitempty"`
	Env           []string             `json:"env,omitempty"`
	Capabilities  []string             `json:"capabilities,omitempty"`
	Secrets       []agentPackageSecret `json:"secrets,omitempty"`
	FilesRequired []string             `json:"files_required,omitempty"`
	EvalSuites    []string             `json:"eval_suites,omitempty"`
	SamplePrompts []string             `json:"sample_prompts,omitempty"`
	Warnings      []string             `json:"warnings,omitempty"`
}

type agentPackageIntegrity struct {
	Algorithm string `json:"algorithm,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Signature string `json:"signature,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Verified  bool   `json:"verified,omitempty"`
}

type agentPackageResponse struct {
	SchemaVersion string                `json:"schema_version"`
	ExportedAt    time.Time             `json:"exported_at"`
	Manifest      agentPackageManifest  `json:"manifest"`
	SOULYAML      string                `json:"soul_yaml"`
	Files         []agentPackageFile    `json:"files,omitempty"`
	Integrity     agentPackageIntegrity `json:"integrity,omitempty"`
}

type agentPackageRequirement struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

type agentPackageInspectResponse struct {
	SchemaVersion string                    `json:"schema_version"`
	Manifest      agentPackageManifest      `json:"manifest"`
	Agent         agent.Definition          `json:"agent"`
	Validation    agentvalidate.Report      `json:"validation"`
	Requirements  []agentPackageRequirement `json:"requirements"`
	Files         []agentPackageFile        `json:"files,omitempty"`
	Integrity     agentPackageIntegrity     `json:"integrity,omitempty"`
	Warnings      []string                  `json:"warnings,omitempty"`
	Importable    bool                      `json:"importable"`
}

type agentPackageImportRequest struct {
	Package    agentPackageResponse `json:"package"`
	IDOverride string               `json:"id_override,omitempty"`
	Overwrite  bool                 `json:"overwrite,omitempty"`
	Disabled   bool                 `json:"disabled,omitempty"`
}

func (s *Server) handleGetAgentPackage(c *fiber.Ctx) error {
	id := c.Params("id")
	def := s.loader.Get(id)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}

	pkg, err := buildAgentPackage(def)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	c.Set(fiber.HeaderContentType, "application/json")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+safeDownloadName(def.ID)+`.soulacy-agent.json"`)
	return c.JSON(pkg)
}

func (s *Server) handleInspectAgentPackage(c *fiber.Ctx) error {
	pkg, err := parseAgentPackageBody(c.Body())
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	inspected, err := s.inspectAgentPackage(pkg)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(inspected)
}

func (s *Server) handleImportAgentPackage(c *fiber.Ctx) error {
	var req agentPackageImportRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	pkg := &req.Package
	if pkg.SchemaVersion == "" && strings.TrimSpace(pkg.SOULYAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "package is required")
	}
	if req.IDOverride != "" {
		var def agent.Definition
		if err := yaml.Unmarshal([]byte(pkg.SOULYAML), &def); err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
		def.ID = strings.TrimSpace(req.IDOverride)
		raw, err := yaml.Marshal(&def)
		if err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
		pkg = &agentPackageResponse{
			SchemaVersion: pkg.SchemaVersion,
			ExportedAt:    pkg.ExportedAt,
			Manifest:      pkg.Manifest,
			SOULYAML:      string(raw),
			Files:         pkg.Files,
		}
		pkg.Manifest.AgentID = def.ID
		pkg.Manifest.Name = def.Name
		sum, err := packageContentChecksum(pkg)
		if err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
		pkg.Integrity = agentPackageIntegrity{Algorithm: "sha256", SHA256: sum}
	}
	inspected, err := s.inspectAgentPackage(pkg)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if !inspected.Validation.Valid {
		return c.Status(fiber.StatusBadRequest).JSON(inspected)
	}
	if isProtectedSystemAgent(inspected.Agent.ID) {
		return protectedSystemAgentResponse(c)
	}
	if s.loader.Get(inspected.Agent.ID) != nil && !req.Overwrite {
		return s.errMsg(c, fiber.StatusConflict, "agent already exists; set overwrite=true or choose a different id")
	}

	def := inspected.Agent.Clone()
	if def.LLM.Provider == "" {
		def.LLM.Provider = s.cfg.LLM.DefaultProvider
	}
	if req.Disabled {
		def.Enabled = false
	} else if !def.Enabled {
		def.Enabled = true
	}

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if dir == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "no agent directory configured")
	}
	if err := writePackageFiles(dir, def.ID, inspected.Files); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if err := s.loader.Upsert(dir, def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if err := s.scheduler.RegisterAgent(def); err != nil {
		s.log.Warn("scheduler registration failed", zap.String("agent", def.ID), zap.Error(err))
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"agent":        def,
		"requirements": inspected.Requirements,
		"warnings":     inspected.Warnings,
	})
}

func parseAgentPackageBody(body []byte) (*agentPackageResponse, error) {
	var wrapper struct {
		Package agentPackageResponse `json:"package"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && wrapper.Package.SchemaVersion != "" {
		return &wrapper.Package, nil
	}
	var pkg agentPackageResponse
	if err := json.Unmarshal(body, &pkg); err != nil {
		return nil, err
	}
	return &pkg, nil
}

func (s *Server) inspectAgentPackage(pkg *agentPackageResponse) (*agentPackageInspectResponse, error) {
	if pkg == nil {
		return nil, errors.New("package is required")
	}
	if pkg.SchemaVersion != "soulacy.agent.package/v1" {
		return nil, fmt.Errorf("unsupported package schema %q", pkg.SchemaVersion)
	}
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(pkg.SOULYAML), &def); err != nil {
		return nil, fmt.Errorf("SOUL.yaml parse failed: %w", err)
	}
	report := agentvalidate.Bytes([]byte(pkg.SOULYAML), "package:SOUL.yaml", s.agentValidationOptions(context.TODO()))
	requirements := s.agentPackageRequirements(pkg, &def)
	warnings := append([]string(nil), pkg.Manifest.Warnings...)
	integrity := pkg.Integrity
	if integrity.SHA256 != "" {
		actual, err := packageContentChecksum(pkg)
		if err != nil {
			return nil, fmt.Errorf("package integrity checksum failed: %w", err)
		}
		if !strings.EqualFold(actual, integrity.SHA256) {
			return nil, fmt.Errorf("package integrity mismatch: expected %s, got %s", integrity.SHA256, actual)
		}
		if integrity.Signature != "" {
			if integrity.PublicKey == "" {
				return nil, errors.New("package signature is present but integrity.public_key is missing")
			}
			pub, err := packageSigningKey(integrity.PublicKey)
			if err != nil {
				return nil, err
			}
			if err := pkgregistry.VerifySignature(pub, integrity.SHA256, integrity.Signature); err != nil {
				return nil, err
			}
			integrity.Verified = true
		}
	}
	for _, file := range pkg.Files {
		if strings.TrimSpace(file.Path) == "" {
			warnings = append(warnings, "package contains a file with an empty path")
		}
		if strings.Contains(file.Path, "..") || filepath.IsAbs(file.Path) {
			warnings = append(warnings, "package file path is unsafe and will not be imported: "+file.Path)
		}
	}
	return &agentPackageInspectResponse{
		SchemaVersion: pkg.SchemaVersion,
		Manifest:      pkg.Manifest,
		Agent:         def,
		Validation:    report,
		Requirements:  requirements,
		Files:         pkg.Files,
		Integrity:     integrity,
		Warnings:      sortedUnique(warnings),
		Importable:    report.Valid,
	}, nil
}

func (s *Server) agentPackageRequirements(pkg *agentPackageResponse, def *agent.Definition) []agentPackageRequirement {
	var reqs []agentPackageRequirement
	addReq := func(kind, name, status, desc string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		reqs = append(reqs, agentPackageRequirement{Kind: kind, Name: name, Status: status, Description: desc})
	}
	for _, provider := range sortedUnique(pkg.Manifest.Providers) {
		status := "missing"
		if s.cfg != nil {
			if _, ok := s.cfg.LLM.Providers[provider]; ok {
				status = "configured"
			}
		}
		if s.llmRouter != nil && s.llmRouter.Provider(provider) != nil {
			status = "available"
		}
		addReq("provider", provider, status, "LLM provider used by this agent.")
	}
	for _, ch := range sortedUnique(pkg.Manifest.Channels) {
		status := "missing"
		if ch == "http" {
			status = "built_in"
		} else if s.channels != nil {
			if statuses := s.channels.Statuses(); statuses[ch].Connected || statuses[ch].Detail != "" {
				status = "available"
			}
		} else if s.cfg != nil {
			if _, ok := s.cfg.Channels[ch]; ok {
				status = "configured"
			}
		}
		addReq("channel", ch, status, "Channel adapter or bot mapping used by this agent.")
	}
	for _, skill := range sortedUnique(pkg.Manifest.Skills) {
		status := "declared"
		if hasSkill(s.skillLoader, skill) {
			status = "available"
		}
		addReq("skill", skill, status, "Install this skill if the agent depends on its instructions.")
	}
	for _, kb := range sortedUnique(pkg.Manifest.Knowledge) {
		addReq("knowledge", kb, "verify", "Create or map this knowledge base on the target runtime.")
	}
	for _, peer := range sortedUnique(pkg.Manifest.PeerAgents) {
		status := "missing"
		if s.loader.Get(peer) != nil {
			status = "available"
		}
		addReq("peer_agent", peer, status, "Import or create this peer agent if the package invokes it.")
	}
	for _, secret := range pkg.Manifest.Secrets {
		addReq("secret", secret.Name, "verify", secret.Description)
	}
	for _, file := range sortedUnique(append(append([]string{}, pkg.Manifest.FilesRequired...), append(pkg.Manifest.EvalSuites, pkg.Manifest.SamplePrompts...)...)) {
		status := "missing"
		for _, packaged := range pkg.Files {
			if filepath.ToSlash(packaged.Path) == filepath.ToSlash(file) {
				status = "packaged"
				break
			}
		}
		addReq("file", file, status, "Portable file bundled with this package, such as a tool, eval suite, or sample prompt.")
	}
	if def != nil && s.loader.Get(def.ID) != nil {
		addReq("agent_id", def.ID, "conflict", "An agent with this ID already exists.")
	}
	sort.Slice(reqs, func(i, j int) bool {
		if reqs[i].Kind == reqs[j].Kind {
			return reqs[i].Name < reqs[j].Name
		}
		return reqs[i].Kind < reqs[j].Kind
	})
	return reqs
}

func hasSkill(loader runtime.SkillLoader, id string) bool {
	if loader == nil || strings.TrimSpace(id) == "" {
		return false
	}
	if loader.Get(id) != nil {
		return true
	}
	for _, skill := range loader.All() {
		if skill.Name == id {
			return true
		}
	}
	return false
}

func writePackageFiles(agentRoot, agentID string, files []agentPackageFile) error {
	base := filepath.Join(agentRoot, agentID)
	for _, file := range files {
		rel := filepath.Clean(filepath.FromSlash(file.Path))
		if rel == "." || rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			continue
		}
		full := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return err
		}
		data := []byte(file.Text)
		if file.SHA256 != "" {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != file.SHA256 {
				return fmt.Errorf("package file %s checksum mismatch", file.Path)
			}
		}
		if err := os.WriteFile(full, data, 0644); err != nil {
			return err
		}
	}
	return nil
}

func buildAgentPackage(def *agent.Definition) (*agentPackageResponse, error) {
	clone := def.Clone()
	manifest := agentPackageManifest{
		AgentID:      clone.ID,
		Name:         clone.Name,
		Version:      clone.Version,
		Description:  clone.Description,
		Trigger:      string(clone.Trigger),
		Surfaces:     sortedUnique(clone.EffectiveSurfaces()),
		Skills:       sortedUnique(clone.Skills),
		Knowledge:    sortedUnique(clone.Knowledge),
		PeerAgents:   sortedUnique(clone.Agents),
		Env:          sortedUnique(clone.Env),
		Capabilities: sortedUnique(agentPackageCapabilities(clone)),
	}

	addString(&manifest.Providers, clone.LLM.Provider)
	addString(&manifest.Models, clone.LLM.Model)
	for _, provider := range clone.LLM.AllowedProviders {
		addString(&manifest.Providers, provider)
	}
	for _, ch := range clone.Channels {
		addString(&manifest.Channels, ch)
	}
	if clone.Schedule != nil && clone.Schedule.Output != nil {
		addString(&manifest.Channels, clone.Schedule.Output.Channel)
	}
	if clone.NotifyOnFailure != nil {
		addString(&manifest.Channels, clone.NotifyOnFailure.Channel)
	}
	if clone.Builtins != nil {
		manifest.Builtins = sortedUnique(*clone.Builtins)
	}
	if clone.MCPServers != nil {
		manifest.MCPServers = sortedUnique(*clone.MCPServers)
	}
	if clone.MCPTools != nil {
		manifest.MCPTools = sortedUnique(*clone.MCPTools)
	}
	manifest.Providers = sortedUnique(manifest.Providers)
	manifest.Models = sortedUnique(manifest.Models)
	manifest.Channels = sortedUnique(manifest.Channels)

	for _, provider := range manifest.Providers {
		manifest.Secrets = append(manifest.Secrets, agentPackageSecret{
			Kind:        "provider_api_key",
			Name:        "llm.providers." + provider + ".api_key",
			Description: "Configure this provider in Soulacy before running the package.",
		})
	}
	for _, ch := range manifest.Channels {
		if ch != "" && ch != "http" {
			manifest.Secrets = append(manifest.Secrets, agentPackageSecret{
				Kind:        "channel_credentials",
				Name:        "channels." + ch,
				Description: "Configure the channel adapter or mapped bot before using this package.",
			})
		}
	}
	if clone.Security != nil && clone.Security.Passphrase != "" {
		manifest.Secrets = append(manifest.Secrets, agentPackageSecret{
			Kind:        "agent_passphrase",
			Name:        "security.passphrase",
			Description: "Set a passphrase after import; the exported package redacts it.",
		})
		clone.Security.Passphrase = ""
	}
	for _, env := range manifest.Env {
		manifest.Secrets = append(manifest.Secrets, agentPackageSecret{
			Kind:        "environment",
			Name:        env,
			Description: "This environment variable must exist in the target runtime.",
		})
	}
	for _, tool := range clone.Tools {
		addString(&manifest.FilesRequired, tool.PythonFile)
	}

	files, warnings := packageToolFiles(clone)
	supportFiles, supportWarnings := packageAgentSupportFiles(clone)
	files = mergePackageFiles(files, supportFiles)
	warnings = append(warnings, supportWarnings...)
	for _, file := range supportFiles {
		switch {
		case strings.HasPrefix(file.Path, "evals/"):
			addString(&manifest.EvalSuites, file.Path)
		case strings.HasPrefix(file.Path, "prompts/"), strings.HasPrefix(file.Path, "samples/"):
			addString(&manifest.SamplePrompts, file.Path)
		}
	}
	manifest.FilesRequired = sortedUnique(manifest.FilesRequired)
	manifest.EvalSuites = sortedUnique(manifest.EvalSuites)
	manifest.SamplePrompts = sortedUnique(manifest.SamplePrompts)
	manifest.Warnings = append(manifest.Warnings, warnings...)

	raw, err := yaml.Marshal(clone)
	if err != nil {
		return nil, err
	}
	pkg := &agentPackageResponse{
		SchemaVersion: "soulacy.agent.package/v1",
		ExportedAt:    time.Now().UTC(),
		Manifest:      manifest,
		SOULYAML:      string(raw),
		Files:         files,
	}
	sum, err := packageContentChecksum(pkg)
	if err != nil {
		return nil, err
	}
	pkg.Integrity = agentPackageIntegrity{Algorithm: "sha256", SHA256: sum}
	return pkg, nil
}

func packageContentChecksum(pkg *agentPackageResponse) (string, error) {
	if pkg == nil {
		return "", errors.New("package is required")
	}
	clone := *pkg
	clone.Integrity = agentPackageIntegrity{}
	data, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func packageSigningKey(hexKey string) ([]byte, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil {
		return nil, fmt.Errorf("package integrity public_key is not hex: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("package integrity public_key must be 32 bytes (got %d)", len(raw))
	}
	return raw, nil
}

func packageToolFiles(def *agent.Definition) ([]agentPackageFile, []string) {
	var files []agentPackageFile
	var warnings []string
	base := filepath.Dir(def.SourcePath)
	if def.SourcePath == "" || base == "." {
		base = ""
	}
	seen := map[string]bool{}
	for _, tool := range def.Tools {
		p := strings.TrimSpace(tool.PythonFile)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if base == "" {
			warnings = append(warnings, "tool "+tool.Name+" references "+p+" but the agent has no source directory")
			continue
		}
		full := filepath.Clean(p)
		if !filepath.IsAbs(full) {
			full = filepath.Join(base, full)
		}
		rel, err := filepath.Rel(base, full)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
			warnings = append(warnings, "tool "+tool.Name+" references a file outside the agent directory: "+p)
			continue
		}
		data, err := readSmallPackageFile(full)
		if err != nil {
			warnings = append(warnings, "tool "+tool.Name+" file could not be packaged: "+p+" ("+err.Error()+")")
			continue
		}
		sum := sha256.Sum256(data)
		files = append(files, agentPackageFile{
			Path:   filepath.ToSlash(rel),
			SHA256: hex.EncodeToString(sum[:]),
			Bytes:  len(data),
			Text:   string(data),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, warnings
}

func packageAgentSupportFiles(def *agent.Definition) ([]agentPackageFile, []string) {
	var files []agentPackageFile
	var warnings []string
	base := filepath.Dir(def.SourcePath)
	if def.SourcePath == "" || base == "." {
		return nil, nil
	}
	specs := []struct {
		dir  string
		exts map[string]bool
	}{
		{dir: "evals", exts: map[string]bool{".yaml": true, ".yml": true, ".json": true}},
		{dir: "prompts", exts: map[string]bool{".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true}},
		{dir: "samples", exts: map[string]bool{".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true}},
	}
	for _, spec := range specs {
		root := filepath.Join(base, spec.dir)
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				if strings.HasPrefix(info.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !spec.exts[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			rel, err := filepath.Rel(base, path)
			if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
				warnings = append(warnings, "support file is outside the agent directory: "+path)
				return nil
			}
			data, err := readSmallPackageFile(path)
			if err != nil {
				warnings = append(warnings, "support file could not be packaged: "+filepath.ToSlash(rel)+" ("+err.Error()+")")
				return nil
			}
			sum := sha256.Sum256(data)
			files = append(files, agentPackageFile{
				Path:   filepath.ToSlash(rel),
				SHA256: hex.EncodeToString(sum[:]),
				Bytes:  len(data),
				Text:   string(data),
			})
			return nil
		})
		if err != nil {
			warnings = append(warnings, "support directory could not be scanned: "+spec.dir+" ("+err.Error()+")")
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, warnings
}

func mergePackageFiles(groups ...[]agentPackageFile) []agentPackageFile {
	seen := map[string]bool{}
	var out []agentPackageFile
	for _, group := range groups {
		for _, file := range group {
			key := filepath.ToSlash(file.Path)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			file.Path = key
			out = append(out, file)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func readSmallPackageFile(path string) ([]byte, error) {
	const maxPackageFileBytes = 512 * 1024
	data, err := osReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxPackageFileBytes {
		return nil, fiber.NewError(fiber.StatusRequestEntityTooLarge, "file exceeds 512 KiB package limit")
	}
	return data, nil
}

var osReadFile = os.ReadFile

func agentPackageCapabilities(def *agent.Definition) []string {
	out := append([]string(nil), def.Capabilities...)
	if def.SystemTools || def.AllowShell {
		out = append(out, "system")
	}
	if def.Unattended {
		out = append(out, "unattended")
	}
	return out
}

func addString(xs *[]string, v string) {
	v = strings.TrimSpace(v)
	if v != "" {
		*xs = append(*xs, v)
	}
}

func sortedUnique(xs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func safeDownloadName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "agent"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "agent"
	}
	return out
}
