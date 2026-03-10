package registry

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type RuntimeType string

const (
	RuntimeUVX    RuntimeType = "uvx"
	RuntimeDocker RuntimeType = "docker"
	RuntimeBinary RuntimeType = "binary"
)

// FilesystemPermission specifies host-to-container bind mounts
type FilesystemPermission struct {
	Path string `yaml:"path"`
	Mode string `yaml:"mode"` // e.g., "ro" or "rw"
}

// AgentRegistryEntry represents a single verifiable agent template in the registry
type AgentRegistryEntry struct {
	ID               string                 `yaml:"id"`
	ClassName        string                 `yaml:"class_name"`
	Description      string                 `yaml:"description,omitempty"`
	Runtime          RuntimeType            `yaml:"runtime"`
	Package          string                 `yaml:"package,omitempty"`           // For uvx base package
	WithDependencies []string               `yaml:"with_dependencies,omitempty"` // For uvx additional dependencies (e.g., local paths)
	Image            string                 `yaml:"image,omitempty"`             // For docker
	Executable       string                 `yaml:"executable,omitempty"`        // For binary
	Args             []string               `yaml:"args,omitempty"`              // Additional args
	Secrets          []string               `yaml:"secrets,omitempty"`           // Secrets required by the agent
	Network          []string               `yaml:"network,omitempty"`           // Authorized egress hosts/networks
	Filesystem       []FilesystemPermission `yaml:"filesystem,omitempty"`        // Host bind mounts
}

// RegistryConfig maps the root yaml structure
type RegistryConfig struct {
	Entries []AgentRegistryEntry `yaml:"entries"`
}

// Registry manages the collection of known agent types
type Registry struct {
	entries map[string]AgentRegistryEntry
}

// Load reads and parses an agent registry yaml file
func Load(path string) (*Registry, error) {
	if path == "" {
		path = os.Getenv("FORGE_AGENT_REGISTRY")
		if path == "" {
			path = "conf/forge-agent-registry.yaml" // Default fallback
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read registry file %s: %w", path, err)
	}

	var config RegistryConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse registry YAML %s: %w", path, err)
	}

	r := &Registry{
		entries: make(map[string]AgentRegistryEntry),
	}

	for _, entry := range config.Entries {
		if entry.ClassName == "" {
			return nil, fmt.Errorf("invalid registry entry %s: class_name is required", entry.ID)
		}
		if entry.Runtime == "" {
			return nil, fmt.Errorf("invalid registry entry %s: runtime is required", entry.ID)
		}
		r.entries[entry.ClassName] = entry
	}

	return r, nil
}

// Lookup finds an agent registry entry by its fully qualified class name
func (r *Registry) Lookup(className string) (*AgentRegistryEntry, error) {
	entry, exists := r.entries[className]
	if !exists {
		return nil, fmt.Errorf("agent class not found in registry: %s", className)
	}
	return &entry, nil
}

// InjectFilesystem adds a host bind mount to the specified agent class dynamically
func (r *Registry) InjectFilesystem(className string, fs FilesystemPermission) error {
	entry, exists := r.entries[className]
	if !exists {
		return fmt.Errorf("class not found: %s", className)
	}
	entry.Filesystem = append(entry.Filesystem, fs)
	r.entries[className] = entry
	return nil
}

// InjectNetwork appends authorized egress hosts/networks to the specified agent class dynamically
func (r *Registry) InjectNetwork(className string, networks []string) error {
	entry, exists := r.entries[className]
	if !exists {
		return fmt.Errorf("class not found: %s", className)
	}
	entry.Network = append(entry.Network, networks...)
	r.entries[className] = entry
	return nil
}

// ClassNames returns all registered class names.
func (r *Registry) ClassNames() []string {
	classNames := make([]string, 0, len(r.entries))
	for className := range r.entries {
		classNames = append(classNames, className)
	}
	return classNames
}

// ResolveCommand generates the OS exec slice strings required to launch the given agent entry
func ResolveCommand(entry *AgentRegistryEntry) []string {
	var cmd []string

	switch entry.Runtime {
	case RuntimeUVX:
		cmd = append(cmd, ResolveUVXCommand())
		forgePkg := os.Getenv("FORGE_PYTHON_PKG")
		if forgePkg == "" {
			forgePkg = "rusticai-forge"
		}
		cmd = append(cmd, "--with", forgePkg)

		for _, dep := range entry.WithDependencies {
			cmd = append(cmd, "--with", dep)
		}
		if entry.Package != "" {
			for _, pkg := range strings.Split(entry.Package, ",") {
				cmd = append(cmd, "--with", strings.TrimSpace(pkg))
			}
		}
		cmd = append(cmd, "python", "-m", "rustic_ai.forge.agent_runner")

	case RuntimeDocker:
		cmd = append(cmd, "docker", "run", "--rm", entry.Image)

	case RuntimeBinary:
		if entry.Executable != "" {
			cmd = append(cmd, entry.Executable)
		}
		if len(entry.Args) > 0 {
			cmd = append(cmd, entry.Args...)
		}

	default:
		cmd = append(cmd, ResolveUVXCommand(), "python", "-m", "rustic_ai.forge.agent_runner")
	}

	return cmd
}
