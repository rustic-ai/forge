package command

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/spf13/cobra"
)

func validateGuild(cmd *cobra.Command, args []string) error {
	guildFile := args[0]

	fmt.Printf("🔍 Validating guild spec: %s\n\n", guildFile)

	// Read file and check if it's a blueprint wrapper
	content, err := os.ReadFile(guildFile)
	if err != nil {
		fmt.Printf("❌ Validation FAILED: %v\n", err)
		return err
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
			fmt.Printf("❌ Validation FAILED: %v\n", err)
			return err
		}

		var nestedSpec protocol.GuildSpec
		if err := json.Unmarshal(wrapper.Spec, &nestedSpec); err != nil {
			fmt.Printf("❌ Validation FAILED: %v\n", err)
			return err
		}
		spec = &nestedSpec
	} else {
		// This is a direct guild spec
		var err error
		spec, _, err = guild.ParseFile(guildFile)
		if err != nil {
			fmt.Printf("❌ Validation FAILED: %v\n", err)
			return err
		}
	}

	errors := []string{}
	warnings := []string{}

	// Validate basic fields
	if spec.Name == "" {
		errors = append(errors, "Guild name is required")
	}

	if len(spec.Agents) == 0 {
		errors = append(errors, "Guild must have at least one agent")
	}

	// Validate agents
	for i, agent := range spec.Agents {
		if agent.ClassName == "" {
			errors = append(errors, fmt.Sprintf("Agent %d (%s) missing class_name", i+1, agent.Name))
		}
		if agent.ID == "" {
			warnings = append(warnings, fmt.Sprintf("Agent %d (%s) has no ID", i+1, agent.Name))
		}
	}

	// Validate routing rules
	if spec.Routes != nil {
		for i, rule := range spec.Routes.Steps {
			if rule.Agent == nil && rule.AgentType == nil {
				warnings = append(warnings, fmt.Sprintf("Routing rule %d has no agent or agent_type specified", i+1))
			}
		}
	}

	// Display results
	if len(errors) > 0 {
		fmt.Println("❌ Validation Errors:")
		for _, err := range errors {
			fmt.Printf("   • %s\n", err)
		}
		fmt.Println()
	}

	if len(warnings) > 0 {
		fmt.Println("⚠️  Validation Warnings:")
		for _, warn := range warnings {
			fmt.Printf("   • %s\n", warn)
		}
		fmt.Println()
	}

	if len(errors) == 0 {
		fmt.Println("✅ Validation PASSED")
		if len(warnings) > 0 {
			fmt.Printf("   (with %d warnings)\n", len(warnings))
		}
		return nil
	}

	return fmt.Errorf("validation failed with %d errors", len(errors))
}
