package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"github.com/rustic-ai/forge/forge-go/protocol"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func RegisterCatalogRoutes(mux *http.ServeMux, s store.Store) {
	registerCatalogRoutes(mux, s, nil)
}

func RegisterCatalogRoutesWithRuntime(mux *http.ServeMux, s store.Store, pusher protocol.ControlPusher) {
	registerCatalogRoutes(mux, s, pusher)
}

func registerCatalogRoutes(mux *http.ServeMux, s store.Store, pusher protocol.ControlPusher) {
	mux.HandleFunc("POST /catalog/blueprints", handleCreateBlueprint(s))
	mux.HandleFunc("GET /catalog/blueprints", handleListBlueprints(s))
	mux.HandleFunc("GET /catalog/blueprints/{id}", handleGetBlueprint(s))
	mux.HandleFunc("GET /catalog/users/{user_id}/blueprints/accessible", handleGetAccessibleBlueprints(s))
	mux.HandleFunc("GET /catalog/organizations/{org_id}/blueprints/owned", handleGetOrganizationOwnedBlueprints(s))
	mux.HandleFunc("GET /catalog/users/{user_id}/blueprints/owned", handleGetUserOwnedBlueprints(s))
	mux.HandleFunc("GET /catalog/organizations/{org_id}/blueprints/shared", handleGetSharedBlueprintsByOrganization(s))
	mux.HandleFunc("GET /catalog/blueprints/{id}/organizations/shared", handleGetSharedOrganizationsByBlueprint(s))
	mux.HandleFunc("GET /catalog/guilds/{guild_id}/blueprints", handleGetBlueprintForGuild(s))

	mux.HandleFunc("POST /catalog/categories", handleCreateCategory(s))
	mux.HandleFunc("GET /catalog/categories", handleListCategories(s))
	mux.HandleFunc("GET /catalog/categories/{category_id}", handleGetCategoryByID(s))
	mux.HandleFunc("GET /catalog/categories/{category_name}/blueprints", handleGetBlueprintsByCategoryName(s))

	mux.HandleFunc("POST /catalog/blueprints/{id}/share", handleShareBlueprint(s))
	mux.HandleFunc("DELETE /catalog/blueprints/{id}/share/{org_id}", handleUnshareBlueprint(s))

	mux.HandleFunc("POST /catalog/blueprints/{id}/reviews", handleCreateReview(s))
	mux.HandleFunc("GET /catalog/blueprints/{id}/reviews", handleGetReviews(s))
	mux.HandleFunc("GET /catalog/blueprints/{id}/reviews/{review_id}", handleGetReviewByID(s))
	mux.HandleFunc("POST /catalog/blueprints/{id}/icons", handleAddBlueprintIcons(s))
	mux.HandleFunc("GET /catalog/blueprints/{id}/icons", handleGetBlueprintIcons(s))
	mux.HandleFunc("POST /catalog/blueprints/{id}/icons/{agent_name}", handleUpsertAgentIcon(s))
	mux.HandleFunc("GET /catalog/blueprints/{id}/icons/{agent_name}", handleGetAgentIcon(s))

	mux.HandleFunc("GET /catalog/tags", handleListTags(s))
	mux.HandleFunc("GET /catalog/tags/{tag}/blueprints", handleGetBlueprintsByTag(s))

	mux.HandleFunc("POST /catalog/agents", handleRegisterCatalogAgent(s))
	mux.HandleFunc("GET /catalog/agents", handleGetCatalogAgents(s))
	mux.HandleFunc("GET /catalog/agents/{class_name}", handleGetCatalogAgentByClassName(s))
	mux.HandleFunc("GET /catalog/agents/message_schema", handleGetMessageSchemaByFormat(s))

	mux.HandleFunc("POST /catalog/guilds/{guild_id}/users/{user_id}", handleAddUserToGuild(s))
	mux.HandleFunc("DELETE /catalog/guilds/{guild_id}/users/{user_id}", handleRemoveUserFromGuild(s))
	mux.HandleFunc("GET /catalog/guilds/{guild_id}/users", handleGetUsersForGuild(s))
	mux.HandleFunc("GET /catalog/users/{user_id}/guilds", handleGetGuildsForUser(s))
	mux.HandleFunc("GET /catalog/organizations/{org_id}/guilds", handleGetGuildsForOrg(s))

	mux.HandleFunc("POST /catalog/blueprints/{id}/guilds", handleLaunchGuildFromBlueprint(s, pusher))
}

func handleListBlueprints(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bps, err := s.ListBlueprints()
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		resp := make([]BlueprintInfoResponse, 0, len(bps))
		for i := range bps {
			resp = append(resp, convertBlueprintToInfo(&bps[i]))
		}

		ReplyJSON(w, http.StatusOK, resp)
	}
}

func handleCreateBlueprint(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req BlueprintCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json: "+err.Error())
			return
		}
		if req.Spec == nil {
			ReplyError(w, http.StatusUnprocessableEntity, "spec is required")
			return
		}

		if err := validateBlueprintSpecForCreate(req.Spec, s); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}

		tags, err := s.CreateOrGetTags(req.Tags)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to process tags: "+err.Error())
			return
		}

		var commands []store.BlueprintCommand
		for _, cmdStr := range req.Commands {
			commands = append(commands, store.BlueprintCommand{Command: cmdStr})
		}

		var prompts []store.BlueprintStarterPrompt
		for _, promptStr := range req.StarterPrompts {
			prompts = append(prompts, store.BlueprintStarterPrompt{Prompt: promptStr})
		}

		name := req.Name
		if name == "" {
			name, _ = req.Spec["name"].(string)
		}
		description := req.Description
		if description == "" {
			description, _ = req.Spec["description"].(string)
		}
		exposure := req.Exposure
		if exposure == "" {
			exposure = store.ExposurePrivate
		}

		bp := &store.Blueprint{
			Name:           name,
			Description:    description,
			Exposure:       exposure,
			AuthorID:       req.AuthorID,
			OrganizationID: req.OrganizationID,
			CategoryID:     req.CategoryID,
			Version:        req.Version,
			Icon:           req.Icon,
			IntroMsg:       req.IntroMsg,
			Spec:           req.Spec,
			Tags:           tags,
			Commands:       commands,
			StarterPrompts: prompts,
		}

		created, err := s.CreateBlueprint(bp)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to create blueprint: "+err.Error())
			return
		}

		ReplyJSON(w, http.StatusCreated, map[string]string{"id": created.ID})
	}
}

func validateBlueprintSpecForCreate(spec map[string]interface{}, s store.Store) error {
	_, hasCfgSchema := spec["configuration_schema"]
	_, hasCfg := spec["configuration"]
	if hasCfgSchema && !hasCfg {
		return errors.New("configuration is required")
	}
	if hasCfg && !hasCfgSchema {
		return errors.New("configuration_schema is required")
	}
	if hasCfgSchema {
		schema, ok := spec["configuration_schema"].(map[string]interface{})
		if !ok {
			return errors.New("configuration_schema must be an object")
		}
		cfg, ok := spec["configuration"].(map[string]interface{})
		if !ok {
			return errors.New("configuration must be an object")
		}
		if err := validateAgainstSchema(schema, cfg); err != nil {
			return fmt.Errorf("configuration and/or schema invalid. %w", err)
		}
	}

	if depMap, ok := spec["dependency_map"].(map[string]interface{}); ok {
		for _, rawDep := range depMap {
			if dep, ok := rawDep.(map[string]interface{}); ok {
				if props, exists := dep["properties"]; exists {
					if _, ok := props.(map[string]interface{}); !ok {
						return errors.New("invalid dependency_map.properties in spec")
					}
				}
			}
		}
	}

	rawAgents, ok := spec["agents"]
	if !ok {
		return nil
	}
	agents, ok := rawAgents.([]interface{})
	if !ok {
		return errors.New("spec.agents must be an array")
	}
	for _, raw := range agents {
		agent, ok := raw.(map[string]interface{})
		if !ok {
			return errors.New("spec.agents entries must be objects")
		}
		className, _ := agent["class_name"].(string)
		if className == "" {
			return errors.New("agent class_name is required")
		}
		if _, err := s.GetAgentByClassName(className); err != nil {
			return fmt.Errorf("agent not found for class_name: %s", className)
		}
		if depMap, ok := agent["dependency_map"].(map[string]interface{}); ok {
			for _, rawDep := range depMap {
				if dep, ok := rawDep.(map[string]interface{}); ok {
					if props, exists := dep["properties"]; exists {
						if _, ok := props.(map[string]interface{}); !ok {
							return errors.New("invalid agent dependency_map.properties in spec")
						}
					}
				}
			}
		}
	}

	// Validate spec via GuildBuilder (matches Python's GuildBuilder.from_spec())
	specCopy := make(map[string]interface{}, len(spec))
	for k, v := range spec {
		specCopy[k] = v
	}
	delete(specCopy, "id")
	specBytes, err := json.Marshal(specCopy)
	if err != nil {
		return fmt.Errorf("invalid guild spec: %w", err)
	}
	var guildSpec protocol.GuildSpec
	if err := json.Unmarshal(specBytes, &guildSpec); err != nil {
		return fmt.Errorf("invalid guild spec: %w", err)
	}
	if _, err := guild.GuildBuilderFromSpec(&guildSpec).BuildSpec(); err != nil {
		return fmt.Errorf("invalid guild spec: %w", err)
	}

	return nil
}

func validateAgainstSchema(schema map[string]interface{}, data map[string]interface{}) error {
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	c := jsonschema.NewCompiler()
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	if err := c.AddResource("schema.json", schemaDoc); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	sch, err := c.Compile("schema.json")
	if err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	if err := sch.Validate(data); err != nil {
		return err
	}
	return nil
}

func handleGetBlueprint(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			ReplyError(w, http.StatusBadRequest, "blueprint id is required")
			return
		}

		bp, err := s.GetBlueprint(id)
		if err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "blueprint not found")
			} else {
				ReplyError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}

		ReplyJSON(w, http.StatusOK, convertBlueprintToDetails(bp))
	}
}

func handleGetAccessibleBlueprints(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("user_id")
		orgIDParam := r.URL.Query().Get("org_id")
		var orgIDPtr *string
		if orgIDParam != "" {
			orgIDPtr = &orgIDParam
		}

		bps, err := s.GetAccessibleBlueprints(userID, orgIDPtr)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		results := make([]AccessibleBlueprintResponse, 0, len(bps))
		for i := range bps {
			results = append(results, convertBlueprintToAccessible(&bps[i], orgIDPtr))
		}

		ReplyJSON(w, http.StatusOK, results)
	}
}

func handleGetOrganizationOwnedBlueprints(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := r.PathValue("org_id")
		bps, err := s.GetBlueprintsByOrganization(orgID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := make([]BlueprintInfoResponse, 0, len(bps))
		for i := range bps {
			resp = append(resp, convertBlueprintToInfo(&bps[i]))
		}
		ReplyJSON(w, http.StatusOK, resp)
	}
}

func handleGetUserOwnedBlueprints(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("user_id")
		bps, err := s.GetBlueprintsByAuthor(userID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := make([]BlueprintInfoResponse, 0, len(bps))
		for i := range bps {
			resp = append(resp, convertBlueprintToInfo(&bps[i]))
		}
		ReplyJSON(w, http.StatusOK, resp)
	}
}

func handleGetSharedBlueprintsByOrganization(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := r.PathValue("org_id")
		bps, err := s.GetBlueprintsSharedWithOrganization(orgID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := make([]BlueprintInfoResponse, 0, len(bps))
		for i := range bps {
			resp = append(resp, convertBlueprintToInfo(&bps[i]))
		}
		ReplyJSON(w, http.StatusOK, resp)
	}
}

func handleGetSharedOrganizationsByBlueprint(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bpID := r.PathValue("id")
		orgIDs, err := s.GetOrganizationsWithSharedBlueprint(bpID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(orgIDs) == 0 {
			orgIDs = []string{}
		}
		ReplyJSON(w, http.StatusOK, orgIDs)
	}
}

func handleGetBlueprintForGuild(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guild_id")
		bp, err := s.GetBlueprintForGuild(guildID)
		if err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "blueprint not found")
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, convertBlueprintToDetails(bp))
	}
}

func handleCreateCategory(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req BlueprintCategoryCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json")
			return
		}
		if req.Name == "" || req.Description == "" {
			ReplyError(w, http.StatusUnprocessableEntity, "name and description are required")
			return
		}

		cat := &store.BlueprintCategory{
			Name:        req.Name,
			Description: req.Description,
		}

		created, err := s.CreateCategory(cat)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		ReplyJSON(w, http.StatusCreated, map[string]string{"id": created.ID})
	}
}

func handleListCategories(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		categories, err := s.ListCategories()
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		responses := make([]BlueprintCategoryResponse, 0, len(categories))
		for _, c := range categories {
			responses = append(responses, BlueprintCategoryResponse{
				ID:          c.ID,
				Name:        c.Name,
				Description: c.Description,
				CreatedAt:   c.CreatedAt,
				UpdatedAt:   c.UpdatedAt,
			})
		}

		ReplyJSON(w, http.StatusOK, responses)
	}
}

func handleGetCategoryByID(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		categoryID := r.PathValue("category_id")
		cat, err := s.GetCategory(categoryID)
		if err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "category not found")
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, BlueprintCategoryResponse{
			ID:          cat.ID,
			Name:        cat.Name,
			Description: cat.Description,
			CreatedAt:   cat.CreatedAt,
			UpdatedAt:   cat.UpdatedAt,
		})
	}
}

func handleGetBlueprintsByCategoryName(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		categoryName := r.PathValue("category_name")
		bps, err := s.GetBlueprintsByCategoryName(categoryName)
		if err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "category not found")
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		resp := make([]BlueprintInfoResponse, 0, len(bps))
		for i := range bps {
			resp = append(resp, convertBlueprintToInfo(&bps[i]))
		}

		ReplyJSON(w, http.StatusOK, resp)
	}
}

func handleShareBlueprint(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bpID := r.PathValue("id")

		var req struct {
			OrganizationID string `json:"organization_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json")
			return
		}
		if req.OrganizationID == "" {
			ReplyError(w, http.StatusUnprocessableEntity, "organization_id required")
			return
		}

		if err := s.ShareBlueprint(bpID, req.OrganizationID); err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "blueprint not found")
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func handleUnshareBlueprint(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bpID := r.PathValue("id")
		orgID := r.PathValue("org_id")

		if err := s.UnshareBlueprint(bpID, orgID); err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "share not found")
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func handleCreateReview(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bpID := r.PathValue("id")
		var req BlueprintReviewCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json")
			return
		}
		if _, err := s.GetBlueprint(bpID); err != nil {
			ReplyError(w, http.StatusNotFound, "blueprint not found")
			return
		}

		rev := &store.BlueprintReview{
			BlueprintID: bpID,
			UserID:      req.UserID,
			Rating:      req.Rating,
			Review:      req.Review,
		}

		created, err := s.CreateBlueprintReview(rev)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		ReplyJSON(w, http.StatusCreated, BlueprintReviewResponse{
			ID:          created.ID,
			BlueprintID: created.BlueprintID,
			UserID:      created.UserID,
			Rating:      created.Rating,
			Review:      created.Review,
			CreatedAt:   created.CreatedAt,
			UpdatedAt:   created.UpdatedAt,
		})
	}
}

func handleGetReviews(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bpID := r.PathValue("id")
		if _, err := s.GetBlueprint(bpID); err != nil {
			ReplyError(w, http.StatusNotFound, "blueprint not found")
			return
		}
		reviews, err := s.GetBlueprintReviews(bpID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		res := make([]BlueprintReviewResponse, 0, len(reviews))
		var totalRating int
		for _, rev := range reviews {
			res = append(res, BlueprintReviewResponse{
				ID:          rev.ID,
				BlueprintID: rev.BlueprintID,
				UserID:      rev.UserID,
				Rating:      rev.Rating,
				Review:      rev.Review,
				CreatedAt:   rev.CreatedAt,
				UpdatedAt:   rev.UpdatedAt,
			})
			totalRating += rev.Rating
		}

		var avgRating float64
		if len(reviews) > 0 {
			avgRating = float64(totalRating) / float64(len(reviews))
		}

		ReplyJSON(w, http.StatusOK, BlueprintReviewsResponse{
			Reviews:       res,
			AverageRating: avgRating,
			TotalReviews:  len(reviews),
		})
	}
}

func handleGetReviewByID(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bpID := r.PathValue("id")
		reviewID := r.PathValue("review_id")
		review, err := s.GetBlueprintReview(reviewID)
		if err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "review not found")
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if review.BlueprintID != bpID {
			ReplyError(w, http.StatusNotFound, "review not found")
			return
		}
		ReplyJSON(w, http.StatusOK, BlueprintReviewResponse{
			ID:          review.ID,
			BlueprintID: review.BlueprintID,
			UserID:      review.UserID,
			Rating:      review.Rating,
			Review:      review.Review,
			CreatedAt:   review.CreatedAt,
			UpdatedAt:   review.UpdatedAt,
		})
	}
}

func handleAddBlueprintIcons(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blueprintID := r.PathValue("id")
		var req BlueprintAgentsIconReqRes
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json")
			return
		}
		if len(req.AgentIcons) == 0 {
			ReplyJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "No agent icons provided"})
			return
		}
		bp, err := s.GetBlueprint(blueprintID)
		if err != nil {
			ReplyError(w, http.StatusNotFound, "blueprint not found")
			return
		}
		validNames := map[string]bool{}
		if agents, ok := bp.Spec["agents"].([]interface{}); ok {
			for _, raw := range agents {
				agentMap, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				if name, _ := agentMap["name"].(string); name != "" {
					validNames[name] = true
				}
			}
		}
		var icons []store.BlueprintAgentIcon
		for _, icon := range req.AgentIcons {
			if !validNames[icon.AgentName] {
				ReplyJSON(w, http.StatusBadRequest, map[string]string{
					"detail": "Provided agent names are not a subset of the agent names in the blueprint",
				})
				return
			}
			icons = append(icons, store.BlueprintAgentIcon{
				BlueprintID: blueprintID,
				AgentName:   icon.AgentName,
				Icon:        icon.Icon,
			})
		}
		if err := s.UpsertBlueprintAgentIcons(blueprintID, icons); err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleUpsertAgentIcon(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blueprintID := r.PathValue("id")
		agentName := r.PathValue("agent_name")
		var req struct {
			AgentIcon string `json:"agent_icon"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json")
			return
		}
		if req.AgentIcon == "" {
			ReplyError(w, http.StatusUnprocessableEntity, "agent_icon required")
			return
		}
		bp, err := s.GetBlueprint(blueprintID)
		if err != nil {
			ReplyError(w, http.StatusNotFound, "blueprint not found")
			return
		}
		if !blueprintHasAgent(bp, agentName) {
			ReplyError(w, http.StatusBadRequest, "agent not in blueprint")
			return
		}
		icon := &store.BlueprintAgentIcon{
			BlueprintID: blueprintID,
			AgentName:   agentName,
			Icon:        req.AgentIcon,
		}
		if err := s.UpsertBlueprintAgentIcon(icon); err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleGetAgentIcon(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blueprintID := r.PathValue("id")
		agentName := r.PathValue("agent_name")
		icon, err := s.GetBlueprintAgentIcon(blueprintID, agentName)
		if err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "agent icon not found")
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, AgentNameWithIcon{AgentName: icon.AgentName, Icon: icon.Icon})
	}
}

func handleGetBlueprintIcons(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blueprintID := r.PathValue("id")
		if _, err := s.GetBlueprint(blueprintID); err != nil {
			ReplyError(w, http.StatusNotFound, "blueprint not found")
			return
		}
		icons, err := s.GetBlueprintAgentIcons(blueprintID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		agentIcons := make([]AgentNameWithIcon, 0, len(icons))
		for _, icon := range icons {
			agentIcons = append(agentIcons, AgentNameWithIcon{
				AgentName: icon.AgentName,
				Icon:      icon.Icon,
			})
		}
		ReplyJSON(w, http.StatusOK, BlueprintAgentsIconReqRes{AgentIcons: agentIcons})
	}
}

func handleListTags(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tags, err := s.GetTags()
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(tags) == 0 {
			tags = []string{}
		}
		ReplyJSON(w, http.StatusOK, tags)
	}
}

func handleGetBlueprintsByTag(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tag := r.PathValue("tag")
		bps, err := s.GetBlueprintsByTag(tag)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := make([]BlueprintInfoResponse, 0, len(bps))
		for i := range bps {
			resp = append(resp, convertBlueprintToInfo(&bps[i]))
		}
		ReplyJSON(w, http.StatusOK, resp)
	}
}

func handleRegisterCatalogAgent(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			QualifiedClassName string                   `json:"qualified_class_name"`
			AgentName          string                   `json:"agent_name"`
			AgentDoc           *string                  `json:"agent_doc"`
			AgentPropsSchema   map[string]interface{}   `json:"agent_props_schema"`
			MessageHandlers    map[string]interface{}   `json:"message_handlers"`
			AgentDependencies  []map[string]interface{} `json:"agent_dependencies"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json")
			return
		}
		if req.QualifiedClassName == "" {
			ReplyError(w, http.StatusUnprocessableEntity, "qualified_class_name required")
			return
		}
		if req.AgentName == "" {
			ReplyError(w, http.StatusUnprocessableEntity, "agent_name required")
			return
		}
		if req.AgentPropsSchema == nil {
			ReplyError(w, http.StatusUnprocessableEntity, "agent_props_schema required")
			return
		}
		if req.MessageHandlers == nil {
			ReplyError(w, http.StatusUnprocessableEntity, "message_handlers required")
			return
		}

		agentDeps := map[string]interface{}{}
		for _, dep := range req.AgentDependencies {
			if dep == nil {
				continue
			}
			key, _ := dep["dependency_key"].(string)
			if key == "" {
				ReplyError(w, http.StatusUnprocessableEntity, "agent_dependencies[].dependency_key required")
				return
			}
			agentDeps[key] = dep
		}

		entry := &store.CatalogAgentEntry{
			QualifiedClassName: req.QualifiedClassName,
			AgentName:          req.AgentName,
			AgentDoc:           req.AgentDoc,
			AgentPropsSchema:   req.AgentPropsSchema,
			MessageHandlers:    req.MessageHandlers,
			AgentDependencies:  agentDeps,
		}
		if err := s.RegisterAgent(entry); err != nil {
			ReplyError(w, http.StatusConflict, "agent already exists")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

var hiddenAgents = map[string]bool{
	"rustic_ai.forge.agents.system.guild_manager_agent.GuildManagerAgent": true,
	"rustic_ai.agents.utils.probe_agent.ProbeAgent":                       true,
	"rustic_ai.agents.utils.probe_agent.EssentialProbeAgent":              true,
}

func handleGetCatalogAgents(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		classNames := r.URL.Query()["class_names"]
		agents, err := s.GetAgents()
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		include := map[string]bool{}
		for _, c := range classNames {
			include[c] = true
		}
		resp := map[string]AgentEntryResponse{}
		for _, a := range agents {
			if len(include) > 0 && !include[a.QualifiedClassName] {
				continue
			}
			if hiddenAgents[a.QualifiedClassName] {
				continue
			}
			resp[a.QualifiedClassName] = catalogAgentToResponse(&a)
		}
		ReplyJSON(w, http.StatusOK, resp)
	}
}

func handleGetCatalogAgentByClassName(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		className := r.PathValue("class_name")
		agent, err := s.GetAgentByClassName(className)
		if err != nil {
			ReplyError(w, http.StatusNotFound, "agent not found")
			return
		}
		ReplyJSON(w, http.StatusOK, catalogAgentToResponse(agent))
	}
}

func catalogAgentToResponse(a *store.CatalogAgentEntry) AgentEntryResponse {
	// Convert AgentDependencies map to list of values (matching Python's list(map.values()))
	var deps []interface{}
	if a.AgentDependencies != nil {
		deps = make([]interface{}, 0, len(a.AgentDependencies))
		for _, v := range a.AgentDependencies {
			deps = append(deps, v)
		}
	}
	if deps == nil {
		deps = []interface{}{}
	}
	return AgentEntryResponse{
		QualifiedClassName: a.QualifiedClassName,
		AgentName:          a.AgentName,
		AgentDoc:           a.AgentDoc,
		AgentPropsSchema:   a.AgentPropsSchema,
		MessageHandlers:    a.MessageHandlers,
		AgentDependencies:  deps,
	}
}

func handleGetMessageSchemaByFormat(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		msgFormat := r.URL.Query().Get("message_format")
		if msgFormat == "" {
			ReplyJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
				"detail": []map[string]interface{}{
					{
						"msg":   "Field required",
						"type":  "missing",
						"loc":   []string{"query", "message_format"},
						"input": nil,
					},
				},
			})
			return
		}
		schema, err := s.GetAgentMessageSchema(msgFormat)
		if err != nil {
			ReplyError(w, http.StatusNotFound, "message format not found")
			return
		}
		ReplyJSON(w, http.StatusOK, schema)
	}
}

func handleAddUserToGuild(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guild_id")
		userID := r.PathValue("user_id")
		if err := s.AddUserToGuild(guildID, userID); err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, err.Error())
				return
			}
			ReplyError(w, http.StatusConflict, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleRemoveUserFromGuild(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guild_id")
		userID := r.PathValue("user_id")
		if err := s.RemoveUserFromGuild(guildID, userID); err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, err.Error())
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleGetUsersForGuild(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guild_id")
		userIDs, err := s.GetUsersForGuild(guildID)
		if err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, err.Error())
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, userIDs)
	}
}

func handleGetGuildsForUser(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("user_id")
		orgIDParam := r.URL.Query().Get("org_id")
		var orgID *string
		if orgIDParam != "" {
			orgID = &orgIDParam
		}
		statuses, err := parseStatuses(r)
		if err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		guilds, err := s.GetGuildsForUser(userID, orgID, statuses)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, buildGuildListWithBlueprints(s, guilds))
	}
}

func handleGetGuildsForOrg(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := r.PathValue("org_id")
		statuses, err := parseStatuses(r)
		if err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		guilds, err := s.GetGuildsForOrg(orgID, statuses)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, buildGuildListWithBlueprints(s, guilds))
	}
}

func handleLaunchGuildFromBlueprint(s store.Store, pusher protocol.ControlPusher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blueprintID := r.PathValue("id")
		var req LaunchGuildFromBlueprintRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json")
			return
		}
		if req.GuildName == "" || req.UserID == "" || req.OrgID == "" {
			ReplyError(w, http.StatusUnprocessableEntity, "guild_name, user_id and org_id are required")
			return
		}

		blueprint, err := s.GetBlueprint(blueprintID)
		if err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "Blueprint not found")
				return
			}
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		allowed, err := canLaunchBlueprint(s, blueprint, req.UserID, req.OrgID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !allowed {
			ReplyJSON(w, http.StatusForbidden, map[string]string{"detail": "Insufficient permissions to launch"})
			return
		}

		specMap := map[string]interface{}{}
		if b, err := json.Marshal(blueprint.Spec); err == nil {
			_ = json.Unmarshal(b, &specMap)
		}

		if rawSchema, ok := specMap["configuration_schema"]; ok {
			schema, ok := rawSchema.(map[string]interface{})
			if !ok {
				ReplyError(w, http.StatusUnprocessableEntity, "configuration_schema must be object")
				return
			}
			baseCfg := map[string]interface{}{}
			if rawCfg, ok := specMap["configuration"]; ok {
				if cast, ok := rawCfg.(map[string]interface{}); ok {
					baseCfg = cast
				}
			}
			mergedCfg := map[string]interface{}{}
			for k, v := range baseCfg {
				mergedCfg[k] = v
			}
			for k, v := range req.Configuration {
				mergedCfg[k] = v
			}
			if err := validateAgainstSchema(schema, mergedCfg); err != nil {
				ReplyError(w, http.StatusUnprocessableEntity, "configuration and/or schema invalid. "+err.Error())
				return
			}
			specMap["configuration"] = mergedCfg
		}

		specBytes, _ := json.Marshal(specMap)
		var guildSpec protocol.GuildSpec
		if err := json.Unmarshal(specBytes, &guildSpec); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid guild spec")
			return
		}
		if req.GuildID != nil {
			guildSpec.ID = *req.GuildID
		}
		if guildSpec.ID == "" {
			guildSpec.ID = idgen.NewShortUUID()
		}
		guildSpec.Name = req.GuildName
		if req.Description != nil {
			guildSpec.Description = *req.Description
		}

		var model *store.GuildModel
		if pusher != nil {
			model, err = guild.Bootstrap(r.Context(), s, pusher, nil, &guildSpec, req.OrgID, dependencyConfigPath())
			if err != nil {
				ReplyError(w, http.StatusInternalServerError, "failed to create guild: "+err.Error())
				return
			}
		} else {
			model = store.FromGuildSpec(&guildSpec, req.OrgID)
			if err := s.CreateGuild(model); err != nil {
				ReplyError(w, http.StatusInternalServerError, "failed to create guild: "+err.Error())
				return
			}
		}
		if err := s.AddUserToGuild(model.ID, req.UserID); err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to add user to guild: "+err.Error())
			return
		}
		if err := s.AddGuildToBlueprint(blueprint.ID, model.ID); err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to associate blueprint with guild: "+err.Error())
			return
		}

		ReplyJSON(w, http.StatusCreated, map[string]string{"id": model.ID})
	}
}

func canLaunchBlueprint(s store.Store, bp *store.Blueprint, userID string, orgID string) (bool, error) {
	switch bp.Exposure {
	case store.ExposurePublic:
		return true, nil
	case store.ExposurePrivate:
		return bp.AuthorID == userID, nil
	case store.ExposureOrganization:
		return bp.OrganizationID != nil && *bp.OrganizationID == orgID, nil
	case store.ExposureShared:
		return s.IsBlueprintSharedWithOrg(bp.ID, orgID)
	default:
		return false, nil
	}
}

// blueprintHasAgent checks whether a blueprint's spec contains an agent with the given name.
func blueprintHasAgent(bp *store.Blueprint, agentName string) bool {
	agents, ok := bp.Spec["agents"].([]interface{})
	if !ok {
		return false
	}
	for _, raw := range agents {
		agentMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if name, _ := agentMap["name"].(string); name == agentName {
			return true
		}
	}
	return false
}

var validGuildStatuses = map[string]bool{
	string(store.GuildStatusRequested):     true,
	string(store.GuildStatusStarting):      true,
	string(store.GuildStatusRunning):       true,
	string(store.GuildStatusStopped):       true,
	string(store.GuildStatusStopping):      true,
	string(store.GuildStatusUnknown):       true,
	string(store.GuildStatusWarning):       true,
	string(store.GuildStatusBacklogged):    true,
	string(store.GuildStatusError):         true,
	string(store.GuildStatusPendingLaunch): true,
}

func parseStatuses(r *http.Request) ([]string, error) {
	param := r.URL.Query().Get("statuses")
	if param == "" {
		return nil, nil
	}
	parts := strings.Split(param, ",")
	statuses := make([]string, 0, len(parts))
	for _, s := range parts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !validGuildStatuses[s] {
			return nil, fmt.Errorf("invalid status: %s", s)
		}
		statuses = append(statuses, s)
	}
	return statuses, nil
}

// buildGuildListWithBlueprints enriches a list of guild models with their
// associated blueprint ID and icon.
func buildGuildListWithBlueprints(s store.Store, guilds []store.GuildModel) []map[string]interface{} {
	resp := make([]map[string]interface{}, 0, len(guilds))
	for _, g := range guilds {
		entry := map[string]interface{}{
			"id":     g.ID,
			"name":   g.Name,
			"icon":   nil,
			"status": g.Status,
		}
		if bp, err := s.GetBlueprintForGuild(g.ID); err == nil {
			entry["blueprint_id"] = bp.ID
			entry["icon"] = bp.Icon
		} else {
			entry["blueprint_id"] = nil
		}
		resp = append(resp, entry)
	}
	return resp
}

func convertBlueprintToInfo(bp *store.Blueprint) BlueprintInfoResponse {
	var catName *string
	if bp.Category != nil {
		catName = &bp.Category.Name
	}
	return BlueprintInfoResponse{
		ID:             bp.ID,
		Name:           bp.Name,
		Description:    bp.Description,
		Version:        bp.Version,
		Exposure:       bp.Exposure,
		AuthorID:       bp.AuthorID,
		CreatedAt:      bp.CreatedAt,
		UpdatedAt:      bp.UpdatedAt,
		Icon:           bp.Icon,
		OrganizationID: bp.OrganizationID,
		CategoryID:     bp.CategoryID,
		CategoryName:   catName,
	}
}

func convertBlueprintToAccessible(bp *store.Blueprint, orgID *string) AccessibleBlueprintResponse {
	var accessOrgID *string
	if orgID != nil && (bp.Exposure == store.ExposureOrganization || bp.Exposure == store.ExposureShared) {
		accessOrgID = orgID
	}
	return AccessibleBlueprintResponse{
		BlueprintInfoResponse: convertBlueprintToInfo(bp),
		AccessOrganizationID:  accessOrgID,
	}
}

func convertBlueprintToDetails(bp *store.Blueprint) BlueprintDetailsResponse {
	tags := make([]string, 0, len(bp.Tags))
	for _, t := range bp.Tags {
		tags = append(tags, t.Tag)
	}

	commands := make([]string, 0, len(bp.Commands))
	for _, c := range bp.Commands {
		commands = append(commands, c.Command)
	}

	prompts := make([]string, 0, len(bp.StarterPrompts))
	for _, p := range bp.StarterPrompts {
		prompts = append(prompts, p.Prompt)
	}

	return BlueprintDetailsResponse{
		BlueprintInfoResponse: convertBlueprintToInfo(bp),
		Spec:                  bp.Spec,
		Tags:                  tags,
		Commands:              commands,
		StarterPrompts:        prompts,
		IntroMsg:              bp.IntroMsg,
	}
}
