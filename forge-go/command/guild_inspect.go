package command

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/spf13/cobra"
)

func inspectGuild(cmd *cobra.Command, args []string) error {
	guildFile := args[0]

	fmt.Printf("📋 Inspecting guild spec: %s\n\n", guildFile)

	// Read file and check if it's a blueprint wrapper
	content, err := os.ReadFile(guildFile)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Check if this is a blueprint wrapper (has a "spec" field)
	var checker map[string]interface{}
	json.Unmarshal(content, &checker)

	var spec *protocol.GuildSpec
	if specField, hasSpec := checker["spec"]; hasSpec && specField != nil {
		// This is a blueprint wrapper - extract the nested spec
		var wrapper struct {
			Spec json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal(content, &wrapper); err != nil {
			return fmt.Errorf("failed to parse wrapper: %w", err)
		}

		var nestedSpec protocol.GuildSpec
		if err := json.Unmarshal(wrapper.Spec, &nestedSpec); err != nil {
			return fmt.Errorf("failed to parse nested spec: %w", err)
		}
		spec = &nestedSpec
	} else {
		// This is a direct guild spec
		var err error
		spec, _, err = guild.ParseFile(guildFile)
		if err != nil {
			return fmt.Errorf("failed to parse guild spec: %w", err)
		}
	}

	// Display basic info
	fmt.Println("Guild Information:")
	fmt.Printf("  Name: %s\n", spec.Name)
	if spec.Description != "" {
		fmt.Printf("  Description: %s\n", spec.Description)
	}
	if spec.ID != "" {
		fmt.Printf("  ID: %s\n", spec.ID)
	}
	fmt.Println()

	// Display agents
	fmt.Printf("Agents (%d):\n", len(spec.Agents))
	for i, agent := range spec.Agents {
		fmt.Printf("  %d. %s (%s)\n", i+1, agent.Name, agent.ID)
		fmt.Printf("     Class: %s\n", agent.ClassName)
		if agent.Description != "" {
			fmt.Printf("     Description: %s\n", agent.Description)
		}
		if len(agent.AdditionalTopics) > 0 {
			fmt.Printf("     Additional Topics: %v\n", agent.AdditionalTopics)
		}
		fmt.Println()
	}

	// Display routing rules
	if spec.Routes != nil && len(spec.Routes.Steps) > 0 {
		fmt.Printf("Routing Rules (%d):\n", len(spec.Routes.Steps))
		for i, rule := range spec.Routes.Steps {
			fmt.Printf("  %d. ", i+1)
			if rule.Agent != nil {
				if rule.Agent.Name != nil && *rule.Agent.Name != "" {
					fmt.Printf("Agent: %s", *rule.Agent.Name)
				} else if rule.Agent.ID != nil && *rule.Agent.ID != "" {
					fmt.Printf("Agent ID: %s", *rule.Agent.ID)
				}
			}
			if rule.MethodName != nil && *rule.MethodName != "" {
				fmt.Printf(" | Method: %s", *rule.MethodName)
			}
			fmt.Println()

			if rule.OriginFilter != nil {
				if rule.OriginFilter.OriginMessageFormat != nil {
					fmt.Printf("     Origin Format: %s\n", *rule.OriginFilter.OriginMessageFormat)
				}
				if rule.OriginFilter.OriginTopic != nil {
					fmt.Printf("     Origin Topic: %s\n", *rule.OriginFilter.OriginTopic)
				}
			}

			if rule.Destination != nil {
				if !rule.Destination.Topics.IsZero() {
					fmt.Printf("     Destination Topics: %v\n", rule.Destination.Topics.ToSlice())
				}
				if len(rule.Destination.RecipientList) > 0 {
					fmt.Printf("     Recipients: %d agents\n", len(rule.Destination.RecipientList))
				}
			}

			if rule.Transformer != nil {
				fmt.Printf("     Transformer: present\n")
			}

			fmt.Println()
		}
	}

	// Display dependencies
	if spec.DependencyMap != nil && len(spec.DependencyMap) > 0 {
		fmt.Printf("Dependencies (%d):\n", len(spec.DependencyMap))
		for name, dep := range spec.DependencyMap {
			fmt.Printf("  - %s: %s\n", name, dep.ClassName)
		}
		fmt.Println()
	}

	// Display properties
	if spec.Properties != nil && len(spec.Properties) > 0 {
		fmt.Println("Properties:")
		propsJSON, _ := json.MarshalIndent(spec.Properties, "  ", "  ")
		fmt.Printf("  %s\n", string(propsJSON))
		fmt.Println()
	}

	return nil
}
