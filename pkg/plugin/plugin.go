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

	// Permissions declares the host capabilities this plugin requests.
	// Plugins are default-deny security principals: anything not listed here
	// is refused at the host-API boundary. Validation and enforcement live in
	// internal/caps; this is pure manifest data.
	Permissions []Permission `yaml:"permissions,omitempty"`

	// Credentials declares the vault secrets this plugin's sidecars need.
	// Only the listed secrets are injected into the sidecar environment at
	// spawn; the vault path namespace must equal the plugin ID. Validation
	// and delegation live in internal/plugins (E6).
	Credentials []CredentialRef `yaml:"credentials,omitempty"`
}

// CredentialRef requests one vault secret for the plugin's sidecars.
//
//	credentials:
//	  - key: MATRIX_TOKEN        # env var name in the sidecar
//	    from: matrix-suite/token # vault path: <plugin-id>/<secret-key>
//
// Key must be an uppercase env-var name; From is `<namespace>/<key>` where
// the namespace MUST be the plugin's own ID — plugins can never reference
// another plugin's (or an agent's) secrets.
type CredentialRef struct {
	Key  string `yaml:"key" json:"key"`
	From string `yaml:"from" json:"from"`
}

// Permission is one capability grant requested in plugin.yaml.
//
// Cap uses the `resource.action` grammar (e.g. "vector.search",
// "channel.send", "events.subscribe"). Exactly one scope list applies per
// capability — which one is determined by the capability's registered scope
// kind. An empty scope list grants the capability unscoped; "*" as a list
// entry matches any value.
//
//	permissions:
//	  - cap: vector.search
//	    agents: [support-bot]
//	  - cap: channel.send
//	    channels: [matrix]
//	  - cap: events.subscribe
//	    types: [run.finished]
type Permission struct {
	Cap      string   `yaml:"cap" json:"cap"`
	Agents   []string `yaml:"agents,omitempty" json:"agents,omitempty"`
	Channels []string `yaml:"channels,omitempty" json:"channels,omitempty"`
	Types    []string `yaml:"types,omitempty" json:"types,omitempty"`
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
