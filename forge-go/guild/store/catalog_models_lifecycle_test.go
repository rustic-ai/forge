package store

import "testing"

func TestAllGormModels_LifecycleAndDefaults(t *testing.T) {
	s, err := NewGormStore(DriverSQLite, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("init gorm store: %v", err)
	}
	defer s.Close()

	gs, ok := s.(*gormStore)
	if !ok {
		t.Fatalf("expected *gormStore implementation")
	}

	// Core guild models
	guild := &GuildModel{ID: "gorm-all-guild", Name: "All Models Guild", OrganizationID: "org-1"}
	if err := gs.db.Create(guild).Error; err != nil {
		t.Fatalf("create guild: %v", err)
	}

	agent := &AgentModel{ID: "gorm-all-guild#a-0", GuildID: &guild.ID, Name: "Echo Agent", ClassName: "EchoAgent"}
	if err := gs.db.Create(agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	route := &GuildRoutes{
		GuildID:    &guild.ID,
		AgentType:  strPtr("rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent"),
		MethodName: strPtr("unwrap_and_forward_message"),
	}
	if err := gs.db.Create(route).Error; err != nil {
		t.Fatalf("create route: %v", err)
	}
	if route.ID == "" {
		t.Fatalf("expected route id auto-generated")
	}

	relaunch := &GuildRelaunchModel{GuildID: guild.ID}
	if err := gs.db.Create(relaunch).Error; err != nil {
		t.Fatalf("create relaunch row: %v", err)
	}
	if relaunch.ID == "" {
		t.Fatalf("expected relaunch id auto-generated")
	}

	// Catalog models
	category := &BlueprintCategory{Name: "Tests", Description: "Test category"}
	if err := gs.db.Create(category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	if category.ID == "" {
		t.Fatalf("expected category id auto-generated")
	}

	tag := &Tag{Tag: "echo"}
	if err := gs.db.Create(tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}

	agentEntry := &CatalogAgentEntry{
		QualifiedClassName: "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
		AgentName:          "EchoAgent",
	}
	if err := gs.db.Create(agentEntry).Error; err != nil {
		t.Fatalf("create agent entry: %v", err)
	}

	bp := &Blueprint{
		Name:           "Simple Echo",
		Description:    "Echo blueprint",
		Exposure:       ExposurePublic,
		AuthorID:       "dummyuserid",
		OrganizationID: strPtr("acmeorganizationid"),
		CategoryID:     &category.ID,
	}
	if err := gs.db.Create(bp).Error; err != nil {
		t.Fatalf("create blueprint: %v", err)
	}
	if bp.ID == "" {
		t.Fatalf("expected blueprint id auto-generated")
	}

	command := &BlueprintCommand{BlueprintID: bp.ID, Command: "/echo"}
	if err := gs.db.Create(command).Error; err != nil {
		t.Fatalf("create blueprint command: %v", err)
	}
	if command.ID == "" {
		t.Fatalf("expected command id auto-generated")
	}

	prompt := &BlueprintStarterPrompt{BlueprintID: bp.ID, Prompt: "Say hello"}
	if err := gs.db.Create(prompt).Error; err != nil {
		t.Fatalf("create starter prompt: %v", err)
	}
	if prompt.ID == "" {
		t.Fatalf("expected starter prompt id auto-generated")
	}

	reviewText := "Works well"
	review := &BlueprintReview{
		BlueprintID: bp.ID,
		UserID:      "dummyuserid",
		Rating:      5,
		Review:      &reviewText,
	}
	if err := gs.db.Create(review).Error; err != nil {
		t.Fatalf("create review: %v", err)
	}
	if review.ID == "" {
		t.Fatalf("expected review id auto-generated")
	}

	shared := &BlueprintSharedWithOrganization{BlueprintID: bp.ID, OrganizationID: "acmeorganizationid"}
	if err := gs.db.Create(shared).Error; err != nil {
		t.Fatalf("create shared org row: %v", err)
	}

	link := &BlueprintAgentLink{BlueprintID: bp.ID, QualifiedClassName: agentEntry.QualifiedClassName}
	if err := gs.db.Create(link).Error; err != nil {
		t.Fatalf("create blueprint agent link: %v", err)
	}

	agentIcon := &AgentIcon{AgentClass: agentEntry.QualifiedClassName, Icon: "https://example.com/icon.svg"}
	if err := gs.db.Create(agentIcon).Error; err != nil {
		t.Fatalf("create agent icon: %v", err)
	}

	bpAgentIcon := &BlueprintAgentIcon{BlueprintID: bp.ID, AgentName: "Echo Agent", Icon: "https://example.com/echo.svg"}
	if err := gs.db.Create(bpAgentIcon).Error; err != nil {
		t.Fatalf("create blueprint agent icon: %v", err)
	}

	bpGuild := &BlueprintGuild{BlueprintID: bp.ID, GuildID: guild.ID}
	if err := gs.db.Create(bpGuild).Error; err != nil {
		t.Fatalf("create blueprint guild row: %v", err)
	}

	userGuild := &UserGuild{GuildID: guild.ID, UserID: "dummyuserid"}
	if err := gs.db.Create(userGuild).Error; err != nil {
		t.Fatalf("create user guild row: %v", err)
	}

	// Explicit join table row
	bpTag := &BlueprintTag{BlueprintID: bp.ID, TagID: tag.ID}
	if err := gs.db.Create(bpTag).Error; err != nil {
		t.Fatalf("create blueprint tag row: %v", err)
	}

	var fetched Blueprint
	if err := gs.db.Preload("Tags").Preload("Commands").Preload("StarterPrompts").Preload("Reviews").First(&fetched, "id = ?", bp.ID).Error; err != nil {
		t.Fatalf("fetch blueprint: %v", err)
	}

	if fetched.Spec == nil {
		t.Fatalf("expected non-nil blueprint spec")
	}
	if fetched.Tags == nil || fetched.Commands == nil || fetched.StarterPrompts == nil || fetched.Reviews == nil || fetched.Agents == nil {
		t.Fatalf("expected non-nil blueprint relationship slices")
	}
}

func strPtr(s string) *string { return &s }
