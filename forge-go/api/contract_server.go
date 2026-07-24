package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rustic-ai/forge/forge-go/api/contract"
	"github.com/rustic-ai/forge/forge-go/telemetry"
)

var _ contract.ServerInterface = (*Server)(nil)

func (s *Server) dispatch(c *gin.Context, handler http.HandlerFunc, pathValues map[string]string) {
	for k, v := range pathValues {
		c.Request.SetPathValue(k, v)
	}
	handler(c.Writer, c.Request)
}

func (s *Server) HealthCheckHealthGet(c *gin.Context) {
	ReplyJSON(c.Writer, http.StatusOK, map[string]string{"message": "All is well"})
}

func (s *Server) Healthz(c *gin.Context) {
	ReplyJSON(c.Writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) Readyz(c *gin.Context) {
	ReplyJSON(c.Writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) GetOpenapiJson(c *gin.Context) {
	s.HandleOpenAPI(c.Writer, c.Request)
}

func (s *Server) GetOpenapiSha256(c *gin.Context) {
	s.HandleOpenAPISha(c.Writer, c.Request)
}

func (s *Server) GetMetrics(c *gin.Context) {
	telemetry.PrometheusHandler().ServeHTTP(c.Writer, c.Request)
}

func (s *Server) RegisterNode(c *gin.Context) {
	s.dispatch(c, RegisterNodeHandler, nil)
}

func (s *Server) NodeHeartbeat(c *gin.Context, nodeID string) {
	s.dispatch(c, NodeHeartbeatHandler, map[string]string{"node_id": nodeID})
}

func (s *Server) ListNodes(c *gin.Context) {
	s.dispatch(c, ListNodesHandler, nil)
}

func (s *Server) GetBoards(c *gin.Context, _ contract.GetBoardsParams) {
	s.dispatch(c, s.HandleGetBoards, nil)
}

func (s *Server) CreateBoard(c *gin.Context, _ contract.CreateBoardParams) {
	s.dispatch(c, s.HandleCreateBoard, nil)
}

func (s *Server) GetBoardMessageIds(c *gin.Context, boardID string, _ contract.GetBoardMessageIdsParams) {
	s.dispatch(c, s.HandleGetBoardMessageIDs, map[string]string{"board_id": boardID})
}

func (s *Server) AddMessageToBoard(c *gin.Context, boardID string, _ contract.AddMessageToBoardParams) {
	s.dispatch(c, s.HandleAddMessageToBoard, map[string]string{"board_id": boardID})
}

func (s *Server) RemoveMessageFromBoard(c *gin.Context, boardID, messageID string, _ contract.RemoveMessageFromBoardParams) {
	s.dispatch(c, s.HandleRemoveMessageFromBoard, map[string]string{"board_id": boardID, "message_id": messageID})
}

func (s *Server) CreateGuild(c *gin.Context) {
	s.dispatch(c, s.HandleCreateGuild, nil)
}

func (s *Server) GetGuildDetailsById(c *gin.Context, guildID string, _ contract.GetGuildDetailsByIdParams) {
	s.dispatch(c, s.HandleGetGuild, map[string]string{"id": guildID})
}

func (s *Server) ListFilesForAgent(c *gin.Context, guildID, agentID string, _ contract.ListFilesForAgentParams) {
	s.dispatch(c, s.HandleAgentFileList, map[string]string{"id": guildID, "agent_id": agentID})
}

func (s *Server) UploadFileForAgent(c *gin.Context, guildID, agentID string, _ contract.UploadFileForAgentParams) {
	s.dispatch(c, s.HandleAgentFileUpload, map[string]string{"id": guildID, "agent_id": agentID})
}

func (s *Server) DeleteAgentFile(c *gin.Context, guildID, agentID, filename string, _ contract.DeleteAgentFileParams) {
	s.dispatch(c, s.HandleAgentFileDelete, map[string]string{"id": guildID, "agent_id": agentID, "filename": filename})
}

func (s *Server) GetFileForAgent(c *gin.Context, guildID, agentID, filename string, _ contract.GetFileForAgentParams) {
	s.dispatch(c, s.HandleAgentFileDownload, map[string]string{"id": guildID, "agent_id": agentID, "filename": filename})
}

func (s *Server) ListFilesForGuild(c *gin.Context, guildID string, _ contract.ListFilesForGuildParams) {
	if isRusticPath(c) {
		s.dispatchRusticGuildFiles(c, guildID)
		return
	}
	s.dispatch(c, s.HandleFileList, map[string]string{"id": guildID})
}

func (s *Server) UploadFileForGuild(c *gin.Context, guildID string, _ contract.UploadFileForGuildParams) {
	if isRusticPath(c) {
		s.dispatchRusticGuildFileUpload(c, guildID)
		return
	}
	s.dispatch(c, s.HandleFileUpload, map[string]string{"id": guildID})
}

func (s *Server) DeleteGuildFile(c *gin.Context, guildID, filename string, _ contract.DeleteGuildFileParams) {
	s.dispatch(c, s.HandleFileDelete, map[string]string{"id": guildID, "filename": filename})
}

func (s *Server) GetFileForGuild(c *gin.Context, guildID, filename string, _ contract.GetFileForGuildParams) {
	s.dispatch(c, s.HandleFileDownload, map[string]string{"id": guildID, "filename": filename})
}

func (s *Server) RelaunchGuild(c *gin.Context, guildID string, _ contract.RelaunchGuildParams) {
	s.dispatch(c, s.HandleRelaunchGuild, map[string]string{"id": guildID})
}

func (s *Server) GetHistoricalUserMessages(c *gin.Context, guildID, userID string, _ contract.GetHistoricalUserMessagesParams) {
	if isRusticPath(c) {
		s.dispatchRusticHistoricalMessages(c, guildID, userID)
		return
	}
	s.dispatch(c, s.HandleGetHistoricalMessages, map[string]string{"id": guildID, "user_id": userID})
}

func (s *Server) GetAgentsByClass(c *gin.Context, _ contract.GetAgentsByClassParams) {
	s.dispatch(c, s.HandleListAgents, nil)
}

func (s *Server) GetMessageSchemaByClass(c *gin.Context, _ contract.GetMessageSchemaByClassParams) {
	s.dispatch(c, s.HandleGetMessageSchemaByClass, nil)
}

func (s *Server) GetAgents(c *gin.Context, _ contract.GetAgentsParams) {
	s.dispatch(c, handleGetCatalogAgents(s.store), nil)
}

func (s *Server) RegisterAgent(c *gin.Context, _ contract.RegisterAgentParams) {
	s.dispatch(c, handleRegisterCatalogAgent(s.store), nil)
}

func (s *Server) GetMessageSchemaByFormat(c *gin.Context, _ contract.GetMessageSchemaByFormatParams) {
	s.dispatch(c, handleGetMessageSchemaByFormat(s.store), nil)
}

func (s *Server) GetAgentByClassName(c *gin.Context, className string, _ contract.GetAgentByClassNameParams) {
	s.dispatch(c, handleGetCatalogAgentByClassName(s.store), map[string]string{"class_name": className})
}

func (s *Server) ListBlueprints(c *gin.Context, _ contract.ListBlueprintsParams) {
	s.dispatch(c, handleListBlueprints(s.store), nil)
}

func (s *Server) CreateBlueprint(c *gin.Context, _ contract.CreateBlueprintParams) {
	s.dispatch(c, handleCreateBlueprint(s.store), nil)
}

func (s *Server) GetBlueprintById(c *gin.Context, blueprintID string, _ contract.GetBlueprintByIdParams) {
	s.dispatch(c, handleGetBlueprint(s.store), map[string]string{"id": blueprintID})
}

func (s *Server) LaunchGuildFromBlueprint(c *gin.Context, blueprintID string, _ contract.LaunchGuildFromBlueprintParams) {
	s.dispatch(c, handleLaunchGuildFromBlueprint(s.store, s.controlPusher), map[string]string{"id": blueprintID})
}

func (s *Server) GetBlueprintAgentIcons(c *gin.Context, blueprintID string, _ contract.GetBlueprintAgentIconsParams) {
	s.dispatch(c, handleGetBlueprintIcons(s.store), map[string]string{"id": blueprintID})
}

func (s *Server) AddBlueprintAgentIcons(c *gin.Context, blueprintID string, _ contract.AddBlueprintAgentIconsParams) {
	s.dispatch(c, handleAddBlueprintIcons(s.store), map[string]string{"id": blueprintID})
}

func (s *Server) GetBlueprintAgentIconByName(c *gin.Context, blueprintID, agentName string, _ contract.GetBlueprintAgentIconByNameParams) {
	s.dispatch(c, handleGetAgentIcon(s.store), map[string]string{"id": blueprintID, "agent_name": agentName})
}

func (s *Server) AddBlueprintAgentIconByName(c *gin.Context, blueprintID, agentName string, _ contract.AddBlueprintAgentIconByNameParams) {
	s.dispatch(c, handleUpsertAgentIcon(s.store), map[string]string{"id": blueprintID, "agent_name": agentName})
}

func (s *Server) GetSharedOrganizationsByBlueprintId(c *gin.Context, blueprintID string, _ contract.GetSharedOrganizationsByBlueprintIdParams) {
	s.dispatch(c, handleGetSharedOrganizationsByBlueprint(s.store), map[string]string{"id": blueprintID})
}

func (s *Server) GetReviewsByBlueprintId(c *gin.Context, blueprintID string, _ contract.GetReviewsByBlueprintIdParams) {
	s.dispatch(c, handleGetReviews(s.store), map[string]string{"id": blueprintID})
}

func (s *Server) CreateBlueprintReview(c *gin.Context, blueprintID string, _ contract.CreateBlueprintReviewParams) {
	s.dispatch(c, handleCreateReview(s.store), map[string]string{"id": blueprintID})
}

func (s *Server) GetBlueprintReview(c *gin.Context, blueprintID, reviewID string, _ contract.GetBlueprintReviewParams) {
	s.dispatch(c, handleGetReviewByID(s.store), map[string]string{"id": blueprintID, "review_id": reviewID})
}

func (s *Server) ShareBlueprintWithOrganization(c *gin.Context, blueprintID string, _ contract.ShareBlueprintWithOrganizationParams) {
	s.dispatch(c, handleShareBlueprint(s.store), map[string]string{"id": blueprintID})
}

func (s *Server) UnshareBlueprintWithOrganization(c *gin.Context, blueprintID, organizationID string, _ contract.UnshareBlueprintWithOrganizationParams) {
	s.dispatch(c, handleUnshareBlueprint(s.store), map[string]string{"id": blueprintID, "org_id": organizationID})
}

func (s *Server) ListCategories(c *gin.Context, _ contract.ListCategoriesParams) {
	s.dispatch(c, handleListCategories(s.store), nil)
}

func (s *Server) CreateCategory(c *gin.Context, _ contract.CreateCategoryParams) {
	s.dispatch(c, handleCreateCategory(s.store), nil)
}

func (s *Server) GetCategoryById(c *gin.Context, categoryID string, _ contract.GetCategoryByIdParams) {
	s.dispatch(c, handleGetCategoryByID(s.store), map[string]string{"category_id": categoryID})
}

func (s *Server) GetBlueprintsByCategoryName(c *gin.Context, categoryName string, _ contract.GetBlueprintsByCategoryNameParams) {
	s.dispatch(c, handleGetBlueprintsByCategoryName(s.store), map[string]string{"category_name": categoryName})
}

func (s *Server) GetBlueprintForGuild(c *gin.Context, guildID string, _ contract.GetBlueprintForGuildParams) {
	s.dispatch(c, handleGetBlueprintForGuild(s.store), map[string]string{"guild_id": guildID})
}

func (s *Server) GetUsersAddedToGuild(c *gin.Context, guildID string, _ contract.GetUsersAddedToGuildParams) {
	s.dispatch(c, handleGetUsersForGuild(s.store), map[string]string{"guild_id": guildID})
}

func (s *Server) RemoveUserFromGuild(c *gin.Context, guildID, userID string, _ contract.RemoveUserFromGuildParams) {
	s.dispatch(c, handleRemoveUserFromGuild(s.store), map[string]string{"guild_id": guildID, "user_id": userID})
}

func (s *Server) AddUserToGuild(c *gin.Context, guildID, userID string, _ contract.AddUserToGuildParams) {
	s.dispatch(c, handleAddUserToGuild(s.store), map[string]string{"guild_id": guildID, "user_id": userID})
}

func (s *Server) GetOrganizationBlueprints(c *gin.Context, organizationID string, _ contract.GetOrganizationBlueprintsParams) {
	s.dispatch(c, handleGetOrganizationOwnedBlueprints(s.store), map[string]string{"org_id": organizationID})
}

func (s *Server) GetSharedBlueprintsByOrganizationId(c *gin.Context, organizationID string, _ contract.GetSharedBlueprintsByOrganizationIdParams) {
	s.dispatch(c, handleGetSharedBlueprintsByOrganization(s.store), map[string]string{"org_id": organizationID})
}

func (s *Server) GetGuildsForOrganization(c *gin.Context, organizationID string, _ contract.GetGuildsForOrganizationParams) {
	s.dispatch(c, handleGetGuildsForOrg(s.store), map[string]string{"org_id": organizationID})
}

func (s *Server) ListTags(c *gin.Context, _ contract.ListTagsParams) {
	s.dispatch(c, handleListTags(s.store), nil)
}

func (s *Server) GetBlueprintsByTag(c *gin.Context, tag string, _ contract.GetBlueprintsByTagParams) {
	s.dispatch(c, handleGetBlueprintsByTag(s.store), map[string]string{"tag": tag})
}

func (s *Server) GetAccessibleBlueprintsByUserId(c *gin.Context, userID string, _ contract.GetAccessibleBlueprintsByUserIdParams) {
	s.dispatch(c, handleGetAccessibleBlueprints(s.store), map[string]string{"user_id": userID})
}

func (s *Server) GetUserBlueprints(c *gin.Context, userID string, _ contract.GetUserBlueprintsParams) {
	s.dispatch(c, handleGetUserOwnedBlueprints(s.store), map[string]string{"user_id": userID})
}

func (s *Server) GetGuildsForUser(c *gin.Context, userID string, _ contract.GetGuildsForUserParams) {
	s.dispatch(c, handleGetGuildsForUser(s.store), map[string]string{"user_id": userID})
}

// Secrets and OAuth are optional features: their routes are always part of the
// generated contract, but the handlers return 404 unless the corresponding
// manager has been configured (e.g. desktop mode).

func (s *Server) secretsEnabled(c *gin.Context) bool {
	if s.secretManager == nil {
		ReplyError(c.Writer, http.StatusNotFound, "secrets API is not enabled")
		return false
	}
	return true
}

func (s *Server) ListOrganizationSecrets(c *gin.Context, orgID string) {
	if !s.secretsEnabled(c) {
		return
	}
	s.dispatch(c, s.handleListSecrets(), map[string]string{"org_id": orgID})
}

func (s *Server) CreateOrganizationSecret(c *gin.Context, orgID string) {
	if !s.secretsEnabled(c) {
		return
	}
	s.dispatch(c, s.handleCreateSecret(), map[string]string{"org_id": orgID})
}

func (s *Server) UpdateOrganizationSecret(c *gin.Context, orgID, name string) {
	if !s.secretsEnabled(c) {
		return
	}
	s.dispatch(c, s.handleUpdateSecret(), map[string]string{"org_id": orgID, "name": name})
}

func (s *Server) DeleteOrganizationSecret(c *gin.Context, orgID, name string) {
	if !s.secretsEnabled(c) {
		return
	}
	s.dispatch(c, s.handleDeleteSecret(), map[string]string{"org_id": orgID, "name": name})
}

func (s *Server) oauthEnabled(c *gin.Context) bool {
	if s.oauthManager == nil {
		ReplyError(c.Writer, http.StatusNotFound, "oauth API is not enabled")
		return false
	}
	return true
}

func (s *Server) ListOAuthProviders(c *gin.Context, orgID string) {
	if !s.oauthEnabled(c) {
		return
	}
	s.dispatch(c, s.handleOAuthListProviders(), map[string]string{"org_id": orgID})
}

func (s *Server) AuthorizeOAuthProvider(c *gin.Context, orgID, providerID string) {
	if !s.oauthEnabled(c) {
		return
	}
	s.dispatch(c, s.handleOAuthAuthorize(), map[string]string{"org_id": orgID, "provider_id": providerID})
}

func (s *Server) OauthCallback(c *gin.Context, _ contract.OauthCallbackParams) {
	if !s.oauthEnabled(c) {
		return
	}
	s.dispatch(c, s.handleOAuthCallback(), nil)
}

func (s *Server) GetOAuthProviderStatus(c *gin.Context, orgID, providerID string) {
	if !s.oauthEnabled(c) {
		return
	}
	s.dispatch(c, s.handleOAuthStatus(), map[string]string{"org_id": orgID, "provider_id": providerID})
}

func (s *Server) DisconnectOAuthProvider(c *gin.Context, orgID, providerID string) {
	if !s.oauthEnabled(c) {
		return
	}
	s.dispatch(c, s.handleOAuthDisconnect(), map[string]string{"org_id": orgID, "provider_id": providerID})
}
