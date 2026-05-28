package builder

// CapabilityGap describes a single capability that the agent spec requires
// but that is not satisfied by any currently-installed tool.
type CapabilityGap struct {
	Required    string   `json:"required"`    // capability label from the spec (e.g. "web search")
	Available   []string `json:"available"`   // currently installed tools that partially satisfy it
	Suggestions []MCPRef `json:"suggestions"` // MCP tools from the registry that would fill the gap
}

// MCPRef is a reference to an entry in the offline MCP registry.
type MCPRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	InstallCmd  string `json:"install_cmd,omitempty"` // e.g. "npx -y @modelcontextprotocol/server-brave-search"
}

// GapAnalyzer compares an agent spec's declared capabilities against the live
// tool catalog and the offline MCP registry.
type GapAnalyzer struct {
	catalog *Registry
}

// NewGapAnalyzer creates a GapAnalyzer backed by the given Registry.
func NewGapAnalyzer(r *Registry) *GapAnalyzer {
	return &GapAnalyzer{catalog: r}
}

// Analyze returns the list of gaps for the given capability labels.
// capabilityLabels are free-form strings extracted from the agent spec
// (e.g. ["web search", "send telegram", "read PDF"]).
func (a *GapAnalyzer) Analyze(capabilityLabels []string) []CapabilityGap {
	var gaps []CapabilityGap
	for _, label := range capabilityLabels {
		matches := a.catalog.Search(label)
		if len(matches) > 0 {
			// At least one installed tool satisfies this capability — no gap.
			continue
		}
		// No installed tool found; collect suggestions from the offline registry.
		available := []string{}
		suggestions := SearchOffline(label)
		if suggestions == nil {
			suggestions = []MCPRef{}
		}

		gaps = append(gaps, CapabilityGap{
			Required:    label,
			Available:   available,
			Suggestions: suggestions,
		})
	}
	return gaps
}
