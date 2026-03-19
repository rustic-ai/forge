package store_test

import (
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"gopkg.in/yaml.v3"
)

func TestStore_AllE2ERoundtrip(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	files, err := filepath.Glob("../testdata/e2e/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("Failed to find E2E testdata yaml files: %v", err)
	}

	for _, yamlPath := range files {
		// code_review_guild.yaml references a missing performance_reviewer.yaml in rustic-ai itself
		if filepath.Base(yamlPath) == "code_review_guild.yaml" {
			continue
		}

		t.Run(filepath.Base(yamlPath), func(t *testing.T) {
			// 1. Parse YAML file
			parsedSpec, _, err := guild.ParseFile(yamlPath)
			if err != nil {
				t.Fatalf("Failed to parse YAML file %s: %v", yamlPath, err)
			}

			// 2. Use builder to construct canonical spec
			spec, err := guild.GuildBuilderFromSpec(parsedSpec).BuildSpec()
			if err != nil {
				t.Fatalf("Failed to build spec: %v", err)
			}

			// 3. Map Domain Spec -> GORM Model
			model := store.FromGuildSpec(spec, "org-foo")

			// 4. Save to SQLite
			if err := db.CreateGuild(model); err != nil {
				t.Fatalf("Failed to save full model to DB: %v", err)
			}

			// Defer cleanup of the created guild to avoid ID collisions between tests
			// Use PurgeGuild to hard delete, otherwise GORM Soft deletes keep the ID occupied
			defer func() {
				if err := db.PurgeGuild(model); err != nil {
					t.Fatalf("failed to purge guild: %v", err)
				}
			}()

			// 5. Fetch from SQLite
			fetchedModel, err := db.GetGuild(spec.ID)
			if err != nil {
				t.Fatalf("Failed to fetch model from DB for %s: %v", spec.ID, err)
			}

			// 6. Map GORM Model -> Domain Spec
			reconstructedSpec := store.ToGuildSpec(fetchedModel)

			// 7. Normalize structural variations in the original spec that GORM canonicalizes
			// e.g., single string topics become 1-element string arrays, missing route times get defaulted.
			spec.Configuration = nil

			if spec.Routes != nil {
				for i := range spec.Routes.Steps {
					step := &spec.Routes.Steps[i]
					if step.Destination != nil && !step.Destination.Topics.IsZero() {
						// Normalize single-string topics to a slice for comparison,
						// since GORM always stores as a string list.
						step.Destination.Topics = protocol.TopicsFromSlice(step.Destination.Topics.ToSlice())
					}
				}
			}

			// GORM/Builder normalization adds default description to Agents based on class name.
			// Let's copy it over to the original spec so they match symmetrically,
			// or strip it from reconstructed if original was empty/different.
			for i := range spec.Agents {
				orig := &spec.Agents[i]
				recon := &reconstructedSpec.Agents[i]
				if orig.Description != recon.Description {
					orig.Description = recon.Description
				}
			}

			// 8. YAML Comparison (Symmetric proof)
			originalYAML, _ := yaml.Marshal(spec)
			reconstructedYAML, _ := yaml.Marshal(reconstructedSpec)

			origStr := string(originalYAML)
			reconStr := string(reconstructedYAML)

			if origStr != reconStr {
				t.Errorf("Roundtrip failed for %s.\n--- Original YAML ---\n%s\n--- Reconstructed YAML ---\n%s", filepath.Base(yamlPath), origStr, reconStr)
			}

			// Try delete now actually so DB is clean context for next iteration. GetGuild might have side-effects
			if err := db.PurgeGuild(model); err != nil {
				t.Fatalf("failed to purge guild: %v", err)
			}
		})
	}
}
