// Package plugin defines the plugin interface for Soulacy.
// Plugins can contribute channel adapters, LLM providers, memory backends,
// and tool libraries. A plugin is a directory with a plugin.yaml manifest
// and optional Go binary or Python package.
package plugin

// Manifest is loaded from plugin.yaml at the root of a plugin directory.
type Manifest struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Version     string   `yaml:"version"`
	Description string   `yaml:"description"`
	Author      string   `yaml:"author"`
	License     string   `yaml:"license"`

	// What this plugin contributes
	Channels  []string `yaml:"channels,omitempty"`  // channel adapter IDs
	Providers []string `yaml:"providers,omitempty"` // LLM provider IDs
	Tools     []string `yaml:"tools,omitempty"`     // tool library IDs

	// Python package to install alongside this plugin
	PythonPackage string `yaml:"python_package,omitempty"`

	// Minimum Soulacy version required
	RequiresVersion string `yaml:"requires_version,omitempty"`
}

// Plugin is the runtime representation of a loaded plugin.
type Plugin interface {
	// ID returns the unique plugin identifier.
	ID() string

	// Name returns the human-readable name.
	Name() string

	// Init is called once when the plugin is loaded.
	// The registry is passed so the plugin can register its contributions.
	Init(registry Registry) error

	// Shutdown is called when Soulacy is stopping.
	Shutdown() error
}

// Registry is the interface plugins use to register their contributions.
type Registry interface {
	RegisterChannel(id string, factory ChannelFactory) error
	RegisterProvider(id string, factory ProviderFactory) error
	RegisterToolLibrary(id string, lib ToolLibrary) error
}

// ChannelFactory creates a new channel adapter instance given config.
type ChannelFactory func(config map[string]any) (any, error)

// ProviderFactory creates a new LLM provider instance given config.
type ProviderFactory func(config map[string]any) (any, error)

// ToolLibrary is a named set of tools contributed by a plugin.
type ToolLibrary interface {
	Name() string
	Tools() []ToolSpec
}

// ToolSpec describes a single tool in a library.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Handler     string         `json:"handler"` // "python:path/to/file.py::function_name"
}
