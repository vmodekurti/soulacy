// mcp_registry.go — client for the official MCP server registry at
// registry.modelcontextprotocol.io.
//
// The registry exposes two endpoints used here:
//
//	GET  /v0/servers?q=<query>&limit=<n>&cursor=<cursor>   — paginated search
//	GET  /v0/servers/<name>/versions/latest                 — server detail
//
// SearchMCPRegistry proxies the search endpoint for the GUI (avoiding CORS).
// FetchMCPRegistryServer fetches a server's installation spec and translates it
// into a GlamaProvisionSpec so the existing provision pipeline can install it
// without any new save/hot-connect logic.
package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	mcpRegistryBase    = "https://registry.modelcontextprotocol.io"
	mcpRegistryTimeout = 8 * time.Second
	mcpRegistryMax     = 20
)

// MCPRegistryServer is a summarized entry returned by SearchMCPRegistry.
// It flattens the registry's nested JSON into a flat view suitable for
// rendering in the GUI's server picker.
type MCPRegistryServer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Publisher   string `json:"publisher"` // namespace before the slash in name (e.g. "io.modelcontextprotocol")
	Runtime     string `json:"runtime"`   // npm | pypi | docker | go (derived from first package)
}

// ── wire types ────────────────────────────────────────────────────────────────

type mcpRegListResponse struct {
	Servers []struct {
		// Official registry wraps each entry under a "server" key.
		Server *mcpRegEntry `json:"server"`
		// Flat format (some sub-registries):
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
	} `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

type mcpRegEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

type mcpRegDetailResponse struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Version     string       `json:"version"`
	Packages    []mcpRegPkg  `json:"packages"`
}

type mcpRegPkg struct {
	RegistryName         string         `json:"registry_name"` // npm | pypi | docker | go
	Name                 string         `json:"name"`
	Version              string         `json:"version"`
	RuntimeArguments     []string       `json:"runtime_arguments"`
	EnvironmentVariables []mcpRegEnvVar `json:"environment_variables"`
}

type mcpRegEnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// ── Search ────────────────────────────────────────────────────────────────────

// SearchMCPRegistry queries registry.modelcontextprotocol.io for MCP servers
// matching query. Pagination is cursor-based: pass cursor="" for the first
// page and use the returned nextCursor for subsequent pages (empty string
// indicates the last page). limit is capped at mcpRegistryMax (20).
func SearchMCPRegistry(query, cursor string, limit int) ([]MCPRegistryServer, string, error) {
	if limit <= 0 || limit > mcpRegistryMax {
		limit = mcpRegistryMax
	}

	endpoint := fmt.Sprintf("%s/v0/servers?limit=%d", mcpRegistryBase, limit)
	if query != "" {
		endpoint += "&q=" + url.QueryEscape(query)
	}
	if cursor != "" {
		endpoint += "&cursor=" + url.QueryEscape(cursor)
	}

	ctx, cancel := context.WithTimeout(context.Background(), mcpRegistryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "soulacy/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("registry search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("registry returned HTTP %d", resp.StatusCode)
	}

	var body mcpRegListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, "", fmt.Errorf("decode registry response: %w", err)
	}

	var out []MCPRegistryServer
	for _, item := range body.Servers {
		id, name, desc, ver := item.ID, item.Name, item.Description, item.Version
		if item.Server != nil {
			if item.Server.Name != "" {
				name = item.Server.Name
			}
			if item.Server.ID != "" {
				id = item.Server.ID
			}
			if item.Server.Description != "" {
				desc = item.Server.Description
			}
			if item.Server.Version != "" {
				ver = item.Server.Version
			}
		}
		if name == "" {
			continue
		}
		if id == "" {
			id = slugify(name)
		}
		out = append(out, MCPRegistryServer{
			ID:          id,
			Name:        name,
			Description: desc,
			Version:     ver,
			Publisher:   mcpPublisher(name),
		})
	}
	return out, body.Metadata.NextCursor, nil
}

// ── Provision ─────────────────────────────────────────────────────────────────

// FetchMCPRegistryServer fetches the latest published version of an MCP server
// from the official registry and translates it into a GlamaProvisionSpec.
// Reusing GlamaProvisionSpec means the existing provision pipeline
// (handleProvisionMCPRegistry) can save and hot-connect registry servers
// without any new storage or connection logic.
//
// The first package entry in the registry response is used to derive the
// install command; remaining packages contribute environment variable schemas.
func FetchMCPRegistryServer(name string) (*GlamaProvisionSpec, error) {
	encoded := url.PathEscape(name)
	endpoint := fmt.Sprintf("%s/v0/servers/%s/versions/latest", mcpRegistryBase, encoded)

	ctx, cancel := context.WithTimeout(context.Background(), mcpRegistryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "soulacy/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("server %q not found in official registry", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned HTTP %d for %q", resp.StatusCode, name)
	}

	var detail mcpRegDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, fmt.Errorf("decode registry detail: %w", err)
	}
	if len(detail.Packages) == 0 {
		return nil, fmt.Errorf("server %q has no installable packages in registry", name)
	}

	pkg := detail.Packages[0]
	command, args := mcpRuntimeToCommand(pkg)

	var envSchema []EnvVarSpec
	seen := map[string]bool{}
	for _, p := range detail.Packages {
		for _, ev := range p.EnvironmentVariables {
			if seen[ev.Name] {
				continue
			}
			seen[ev.Name] = true
			envSchema = append(envSchema, EnvVarSpec{
				Name:        ev.Name,
				Description: ev.Description,
				Required:    ev.Required,
			})
		}
	}

	displayName := detail.Name
	if displayName == "" {
		displayName = name
	}
	id := slugify(displayName)
	if id == "" {
		id = slugify(name)
	}

	return &GlamaProvisionSpec{
		ID:          id,
		Name:        displayName,
		Description: detail.Description,
		Command:     command,
		Args:        args,
		RegistryURL: fmt.Sprintf("%s/#/servers/%s", mcpRegistryBase, url.PathEscape(name)),
		EnvSchema:   envSchema,
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// mcpRuntimeToCommand maps a registry package entry to the (command, args)
// pair that will launch the MCP server as a stdio child process.
//
// Runtime-to-command mapping table:
//
//	npm    → npx -y <name> [runtime_arguments...]
//	pypi   → uvx <name> [runtime_arguments...]
//	docker → docker run -i --rm <name> [runtime_arguments...]
//	go     → go run <name> [runtime_arguments...]
//	other  → <name> [runtime_arguments...]  (treat name as executable)
//
// npx and uvx handle package installation automatically (the -y flag on npx
// suppresses the interactive install prompt). Docker is run with -i so the
// container's stdin/stdout are wired for the stdio MCP transport.
func mcpRuntimeToCommand(pkg mcpRegPkg) (command string, args []string) {
	extra := pkg.RuntimeArguments
	switch strings.ToLower(pkg.RegistryName) {
	case "npm":
		return "npx", append([]string{"-y", pkg.Name}, extra...)
	case "pypi":
		return "uvx", append([]string{pkg.Name}, extra...)
	case "docker":
		return "docker", append([]string{"run", "-i", "--rm", pkg.Name}, extra...)
	case "go":
		return "go", append([]string{"run", pkg.Name}, extra...)
	default:
		return pkg.Name, extra
	}
}

func mcpPublisher(name string) string {
	if i := strings.Index(name, "/"); i > 0 {
		return name[:i]
	}
	return ""
}
