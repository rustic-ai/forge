package guild

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/cbroglie/mustache"
	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"gopkg.in/yaml.v3"
)

// GuildBuilder provides a stateful, fluent API for constructing a GuildSpec.
// Fluent methods store the first error encountered. BuildSpec() checks for
// stored errors before proceeding, preserving method chaining while being
// idiomatic Go.
type GuildBuilder struct {
	spec    protocol.GuildSpec
	nameSet bool
	descSet bool
	err     error
}

// NewGuildBuilder creates an empty builder with an auto-generated ID.
func NewGuildBuilder() *GuildBuilder {
	return &GuildBuilder{
		spec: protocol.GuildSpec{
			ID:            idgen.NewShortUUID(),
			Properties:    map[string]interface{}{},
			Agents:        []protocol.AgentSpec{},
			DependencyMap: map[string]protocol.DependencySpec{},
		},
	}
}

// GuildBuilderFromSpec creates a builder pre-populated from an existing GuildSpec.
// The nameSet and descSet flags are inferred from the spec's fields.
func GuildBuilderFromSpec(spec *protocol.GuildSpec) *GuildBuilder {
	if spec == nil {
		return &GuildBuilder{err: fmt.Errorf("cannot create builder from nil spec")}
	}
	cp := *spec
	return &GuildBuilder{
		spec:    cp,
		nameSet: cp.Name != "",
		descSet: cp.Description != "",
	}
}

// GuildBuilderFromYAMLFile creates a builder from a YAML file path.
func GuildBuilderFromYAMLFile(path string) *GuildBuilder {
	spec, _, err := ParseFile(path)
	if err != nil {
		return &GuildBuilder{err: fmt.Errorf("failed to parse YAML file %s: %w", path, err)}
	}
	return GuildBuilderFromSpec(spec)
}

// GuildBuilderFromJSONFile creates a builder from a JSON file path.
func GuildBuilderFromJSONFile(path string) *GuildBuilder {
	content, err := os.ReadFile(path)
	if err != nil {
		return &GuildBuilder{err: fmt.Errorf("failed to read JSON file %s: %w", path, err)}
	}
	var spec protocol.GuildSpec
	if err := json.Unmarshal(content, &spec); err != nil {
		return &GuildBuilder{err: fmt.Errorf("failed to unmarshal JSON file %s: %w", path, err)}
	}
	return GuildBuilderFromSpec(&spec)
}

// GuildBuilderFromYAML creates a builder from a YAML string.
func GuildBuilderFromYAML(yamlStr string) *GuildBuilder {
	var raw interface{}
	if err := yaml.Unmarshal([]byte(yamlStr), &raw); err != nil {
		return &GuildBuilder{err: fmt.Errorf("failed to parse YAML string: %w", err)}
	}
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return &GuildBuilder{err: fmt.Errorf("failed to convert YAML to JSON: %w", err)}
	}
	var spec protocol.GuildSpec
	if err := json.Unmarshal(jsonBytes, &spec); err != nil {
		return &GuildBuilder{err: fmt.Errorf("failed to unmarshal YAML-derived JSON: %w", err)}
	}
	return GuildBuilderFromSpec(&spec)
}

// GuildBuilderFromJSON creates a builder from a JSON string.
func GuildBuilderFromJSON(jsonStr string) *GuildBuilder {
	var spec protocol.GuildSpec
	if err := json.Unmarshal([]byte(jsonStr), &spec); err != nil {
		return &GuildBuilder{err: fmt.Errorf("failed to unmarshal JSON string: %w", err)}
	}
	return GuildBuilderFromSpec(&spec)
}

// --- Fluent setters ---

// SetName sets the guild name. Must be 1-64 characters.
func (b *GuildBuilder) SetName(name string) *GuildBuilder {
	if b.err != nil {
		return b
	}
	if name == "" || len(name) > 64 {
		b.err = fmt.Errorf("guild name must be 1-64 characters")
		return b
	}
	b.spec.Name = name
	b.nameSet = true
	return b
}

// SetDescription sets the guild description. Must be non-empty.
func (b *GuildBuilder) SetDescription(desc string) *GuildBuilder {
	if b.err != nil {
		return b
	}
	if desc == "" {
		b.err = fmt.Errorf("guild description must not be empty")
		return b
	}
	b.spec.Description = desc
	b.descSet = true
	return b
}

// SetProperty sets a single property on the guild spec.
func (b *GuildBuilder) SetProperty(key string, value interface{}) *GuildBuilder {
	if b.err != nil {
		return b
	}
	if b.spec.Properties == nil {
		b.spec.Properties = make(map[string]interface{})
	}
	b.spec.Properties[key] = value
	return b
}

// SetExecutionEngine sets the execution engine class name.
func (b *GuildBuilder) SetExecutionEngine(className string) *GuildBuilder {
	return b.SetProperty("execution_engine", className)
}

// SetMessaging sets the messaging backend configuration.
func (b *GuildBuilder) SetMessaging(backendModule, backendClass string, backendConfig map[string]interface{}) *GuildBuilder {
	return b.SetProperty("messaging", map[string]interface{}{
		"backend_module": backendModule,
		"backend_class":  backendClass,
		"backend_config": backendConfig,
	})
}

// SetDependencyMap replaces the entire dependency map.
func (b *GuildBuilder) SetDependencyMap(deps map[string]protocol.DependencySpec) *GuildBuilder {
	if b.err != nil {
		return b
	}
	b.spec.DependencyMap = deps
	return b
}

// AddDependencyResolver adds a single dependency resolver entry.
func (b *GuildBuilder) AddDependencyResolver(key string, dep protocol.DependencySpec) *GuildBuilder {
	if b.err != nil {
		return b
	}
	if b.spec.DependencyMap == nil {
		b.spec.DependencyMap = make(map[string]protocol.DependencySpec)
	}
	b.spec.DependencyMap[key] = dep
	return b
}

// LoadDependencyMapFromYAML reads a YAML file and merges its dependencies
// (does not overwrite existing keys).
func (b *GuildBuilder) LoadDependencyMapFromYAML(filepath string) *GuildBuilder {
	if b.err != nil {
		return b
	}
	content, err := os.ReadFile(filepath)
	if err != nil {
		if os.IsNotExist(err) {
			return b
		}
		b.err = fmt.Errorf("failed to read dependency YAML %s: %w", filepath, err)
		return b
	}
	var deps map[string]protocol.DependencySpec
	if err := yaml.Unmarshal(content, &deps); err != nil {
		b.err = fmt.Errorf("failed to unmarshal dependency YAML %s: %w", filepath, err)
		return b
	}
	if b.spec.DependencyMap == nil {
		b.spec.DependencyMap = make(map[string]protocol.DependencySpec)
	}
	for k, v := range deps {
		if _, exists := b.spec.DependencyMap[k]; !exists {
			b.spec.DependencyMap[k] = v
		}
	}
	return b
}

// SetGateway sets the gateway configuration.
func (b *GuildBuilder) SetGateway(config *protocol.GatewayConfig) *GuildBuilder {
	if b.err != nil {
		return b
	}
	b.spec.Gateway = config
	return b
}

// AddAgentSpec appends an agent spec to the guild.
func (b *GuildBuilder) AddAgentSpec(agent protocol.AgentSpec) *GuildBuilder {
	if b.err != nil {
		return b
	}
	b.spec.Agents = append(b.spec.Agents, agent)
	return b
}

// SetRoutes replaces the routing slip.
func (b *GuildBuilder) SetRoutes(routes *protocol.RoutingSlip) *GuildBuilder {
	if b.err != nil {
		return b
	}
	b.spec.Routes = routes
	return b
}

// AddRoute appends a routing rule to the routing slip.
func (b *GuildBuilder) AddRoute(rule protocol.RoutingRule) *GuildBuilder {
	if b.err != nil {
		return b
	}
	if b.spec.Routes == nil {
		b.spec.Routes = &protocol.RoutingSlip{}
	}
	b.spec.Routes.Steps = append(b.spec.Routes.Steps, rule)
	return b
}

// --- Build methods ---

// Validate checks that required fields (name, description) have been set.
func (b *GuildBuilder) Validate() error {
	if b.err != nil {
		return b.err
	}
	var missing []string
	if !b.nameSet {
		missing = append(missing, "name")
	}
	if !b.descSet {
		missing = append(missing, "description")
	}
	if len(missing) > 0 {
		return fmt.Errorf("guild builder missing required fields: %v", missing)
	}
	return nil
}

// BuildSpec runs the full build pipeline: applyDefaults → mergeDependencyMap →
// resolveTemplates → validate → return a copy of the spec.
func (b *GuildBuilder) BuildSpec() (*protocol.GuildSpec, error) {
	if b.err != nil {
		return nil, b.err
	}

	b.applyDefaults()

	// Merge dependency configs: forge-home deps take priority over conf deps;
	// spec-level deps (already in DependencyMap) take priority over both.
	forgeHomeDeps := forgepath.Resolve(forgepath.DependencyConfigFile)
	if err := b.mergeDependencyMap(forgeHomeDeps); err != nil {
		return nil, fmt.Errorf("failed to merge forge-home dependencies: %w", err)
	}
	if err := b.mergeDependencyMap(forgepath.DependencyConfigPath()); err != nil {
		return nil, fmt.Errorf("failed to merge dependencies: %w", err)
	}

	if err := b.resolveTemplates(); err != nil {
		return nil, fmt.Errorf("failed to resolve templates: %w", err)
	}

	if err := Validate(&b.spec); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	out := b.spec
	return &out, nil
}

// --- Internal pipeline methods ---

func (b *GuildBuilder) applyDefaults() {
	if b.spec.Properties == nil {
		b.spec.Properties = make(map[string]interface{})
	}

	if _, ok := b.spec.Properties["execution_engine"]; !ok {
		b.spec.Properties["execution_engine"] = "rustic_ai.forge.ForgeExecutionEngine"
	}

	if _, ok := b.spec.Properties["messaging"]; !ok {
		b.spec.Properties["messaging"] = map[string]interface{}{
			"backend_module": "rustic_ai.forge.messaging.redis_backend",
			"backend_class":  "RedisMessagingBackend",
			"backend_config": map[string]interface{}{
				"url": "redis://localhost:6379",
			},
		}
	}

	for i := range b.spec.Agents {
		if b.spec.Agents[i].ListenToDefaultTopic == nil {
			b.spec.Agents[i].ListenToDefaultTopic = boolPtr(true)
		}
		if b.spec.Agents[i].ActOnlyWhenTagged == nil {
			b.spec.Agents[i].ActOnlyWhenTagged = boolPtr(false)
		}
	}

	if b.spec.Routes != nil {
		for i := range b.spec.Routes.Steps {
			if b.spec.Routes.Steps[i].RouteTimes == nil {
				b.spec.Routes.Steps[i].RouteTimes = intPtr(1)
			}
		}
	}

	const gatewayClassName = "rustic_ai.core.guild.g2g.gateway_agent.GatewayAgent"
	if b.spec.Gateway != nil && b.spec.Gateway.Enabled {
		hasGateway := slices.ContainsFunc(b.spec.Agents, func(a protocol.AgentSpec) bool {
			return a.ClassName == gatewayClassName
		})
		if !hasGateway {
			b.spec.Agents = append(b.spec.Agents, protocol.AgentSpec{
				ID:          "gateway",
				Name:        "Gateway",
				Description: "Automatic Gateway Agent",
				ClassName:   gatewayClassName,
				Properties: map[string]interface{}{
					"input_formats":    b.spec.Gateway.InputFormats,
					"output_formats":   b.spec.Gateway.OutputFormats,
					"returned_formats": b.spec.Gateway.ReturnedFormats,
				},
			})
		}
	}
}

func (b *GuildBuilder) mergeDependencyMap(configPath string) error {
	if b.spec.DependencyMap == nil {
		b.spec.DependencyMap = make(map[string]protocol.DependencySpec)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("error reading dependency config file %s: %w", configPath, err)
	}

	var globalDeps map[string]protocol.DependencySpec
	if err := yaml.Unmarshal(content, &globalDeps); err != nil {
		return fmt.Errorf("failed to unmarshal global dependencies: %w", err)
	}

	for name, globalDep := range globalDeps {
		if _, exists := b.spec.DependencyMap[name]; !exists {
			b.spec.DependencyMap[name] = globalDep
		}
	}

	return nil
}

func (b *GuildBuilder) resolveTemplates() error {
	if len(b.spec.Configuration) == 0 {
		return nil
	}

	for i, agent := range b.spec.Agents {
		agentBytes, err := json.Marshal(agent)
		if err != nil {
			return fmt.Errorf("failed to marshal agent %s for templating: %w", agent.Name, err)
		}

		renderedStr, err := mustache.Render(string(agentBytes), b.spec.Configuration)
		if err != nil {
			return fmt.Errorf("failed to render mustache template for agent %s: %w", agent.Name, err)
		}

		var newAgent protocol.AgentSpec
		if err := json.Unmarshal([]byte(renderedStr), &newAgent); err != nil {
			return fmt.Errorf("failed to unmarshal rendered template for agent %s: %w", agent.Name, err)
		}

		b.spec.Agents[i] = newAgent
	}

	if b.spec.Routes != nil {
		for i, rule := range b.spec.Routes.Steps {
			ruleBytes, err := json.Marshal(rule)
			if err != nil {
				return fmt.Errorf("failed to marshal routing rule for templating: %w", err)
			}

			renderedStr, err := mustache.Render(string(ruleBytes), b.spec.Configuration)
			if err != nil {
				return fmt.Errorf("failed to render mustache template for routing rule: %w", err)
			}

			var newRule protocol.RoutingRule
			if err := json.Unmarshal([]byte(renderedStr), &newRule); err != nil {
				return fmt.Errorf("failed to unmarshal rendered template for routing rule: %w", err)
			}

			b.spec.Routes.Steps[i] = newRule
		}
	}

	return nil
}
