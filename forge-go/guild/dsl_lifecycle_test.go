package guild

import (
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
)

func TestDSL_Lifecycle_ParseBuildValidateStoreRoundTrip(t *testing.T) {
	db, err := store.NewGormStore(store.DriverSQLite, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer func() { _ = db.Close() }()

	files, err := filepath.Glob("testdata/*.yaml")
	if err != nil {
		t.Fatalf("glob testdata yaml: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no dsl fixtures found")
	}

	for _, p := range files {
		t.Run(filepath.Base(p), func(t *testing.T) {
			parsedSpec, _, err := ParseFile(p)
			if err != nil {
				t.Fatalf("parse dsl: %v", err)
			}

			built, err := GuildBuilderFromSpec(parsedSpec).BuildSpec()
			if err != nil {
				t.Fatalf("build dsl: %v", err)
			}

			if err := Validate(built); err != nil {
				t.Fatalf("validate built dsl: %v", err)
			}

			model := store.FromGuildSpec(built, "org-dsl")
			if err := db.CreateGuild(model); err != nil {
				t.Fatalf("persist built spec: %v", err)
			}
			defer func() {
				if err := db.PurgeGuild(model); err != nil {
					t.Fatalf("purge guild: %v", err)
				}
			}()

			fetched, err := db.GetGuild(built.ID)
			if err != nil {
				t.Fatalf("fetch persisted guild: %v", err)
			}

			round := store.ToGuildSpec(fetched)

			if round.ID != built.ID || round.Name != built.Name {
				t.Fatalf("dsl lifecycle changed identity fields: built=(%s,%s) round=(%s,%s)", built.ID, built.Name, round.ID, round.Name)
			}
			if len(round.Agents) != len(built.Agents) {
				t.Fatalf("dsl lifecycle changed agent count: built=%d round=%d", len(built.Agents), len(round.Agents))
			}
			if built.Routes != nil {
				if round.Routes == nil {
					t.Fatalf("dsl lifecycle dropped routes")
				}
				if len(round.Routes.Steps) != len(built.Routes.Steps) {
					t.Fatalf("dsl lifecycle changed route steps: built=%d round=%d", len(built.Routes.Steps), len(round.Routes.Steps))
				}
			}
		})
	}
}
