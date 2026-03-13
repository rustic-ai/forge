package contract

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// UnimplementedServer provides default 501 responses for all generated operations.
type UnimplementedServer struct{}

func (UnimplementedServer) HealthCheckHealthGet(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) Healthz(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetMetrics(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) ListNodes(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) RegisterNode(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) NodeHeartbeat(c *gin.Context, nodeId string) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetOpenapiJson(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetOpenapiSha256(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) Readyz(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetBoards(c *gin.Context, params GetBoardsParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) CreateBoard(c *gin.Context, params CreateBoardParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetBoardMessageIds(c *gin.Context, boardId string, params GetBoardMessageIdsParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) AddMessageToBoard(c *gin.Context, boardId string, params AddMessageToBoardParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) RemoveMessageFromBoard(c *gin.Context, boardId string, messageId string, params RemoveMessageFromBoardParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) CreateGuild(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetGuildDetailsById(c *gin.Context, guildId string, params GetGuildDetailsByIdParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) ListFilesForAgent(c *gin.Context, guildId string, agentId string, params ListFilesForAgentParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) UploadFileForAgent(c *gin.Context, guildId string, agentId string, params UploadFileForAgentParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) DeleteAgentFile(c *gin.Context, guildId string, agentId string, filename string, params DeleteAgentFileParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetFileForAgent(c *gin.Context, guildId string, agentId string, filename string, params GetFileForAgentParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) ListFilesForGuild(c *gin.Context, guildId string, params ListFilesForGuildParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) UploadFileForGuild(c *gin.Context, guildId string, params UploadFileForGuildParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) DeleteGuildFile(c *gin.Context, guildId string, filename string, params DeleteGuildFileParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetFileForGuild(c *gin.Context, guildId string, filename string, params GetFileForGuildParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) RelaunchGuild(c *gin.Context, guildId string, params RelaunchGuildParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetHistoricalUserMessages(c *gin.Context, guildId string, userId string, params GetHistoricalUserMessagesParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetAgentsByClass(c *gin.Context, params GetAgentsByClassParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetMessageSchemaByClass(c *gin.Context, params GetMessageSchemaByClassParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetAgents(c *gin.Context, params GetAgentsParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) RegisterAgent(c *gin.Context, params RegisterAgentParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetMessageSchemaByFormat(c *gin.Context, params GetMessageSchemaByFormatParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetAgentByClassName(c *gin.Context, className string, params GetAgentByClassNameParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) ListBlueprints(c *gin.Context, params ListBlueprintsParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) CreateBlueprint(c *gin.Context, params CreateBlueprintParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetBlueprintById(c *gin.Context, blueprintId string, params GetBlueprintByIdParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) LaunchGuildFromBlueprint(c *gin.Context, blueprintId string, params LaunchGuildFromBlueprintParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetBlueprintAgentIcons(c *gin.Context, blueprintId string, params GetBlueprintAgentIconsParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) AddBlueprintAgentIcons(c *gin.Context, blueprintId string, params AddBlueprintAgentIconsParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetBlueprintAgentIconByName(c *gin.Context, blueprintId string, agentName string, params GetBlueprintAgentIconByNameParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) AddBlueprintAgentIconByName(c *gin.Context, blueprintId string, agentName string, params AddBlueprintAgentIconByNameParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetSharedOrganizationsByBlueprintId(c *gin.Context, blueprintId string, params GetSharedOrganizationsByBlueprintIdParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetReviewsByBlueprintId(c *gin.Context, blueprintId string, params GetReviewsByBlueprintIdParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) CreateBlueprintReview(c *gin.Context, blueprintId string, params CreateBlueprintReviewParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetBlueprintReview(c *gin.Context, blueprintId string, reviewId string, params GetBlueprintReviewParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) ShareBlueprintWithOrganization(c *gin.Context, blueprintId string, params ShareBlueprintWithOrganizationParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) UnshareBlueprintWithOrganization(c *gin.Context, blueprintId string, organizationId string, params UnshareBlueprintWithOrganizationParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) ListCategories(c *gin.Context, params ListCategoriesParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) CreateCategory(c *gin.Context, params CreateCategoryParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetCategoryById(c *gin.Context, categoryId string, params GetCategoryByIdParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetBlueprintsByCategoryName(c *gin.Context, categoryName string, params GetBlueprintsByCategoryNameParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetBlueprintForGuild(c *gin.Context, guildId string, params GetBlueprintForGuildParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetUsersAddedToGuild(c *gin.Context, guildId string, params GetUsersAddedToGuildParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) RemoveUserFromGuild(c *gin.Context, guildId string, userId string, params RemoveUserFromGuildParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) AddUserToGuild(c *gin.Context, guildId string, userId string, params AddUserToGuildParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetOrganizationBlueprints(c *gin.Context, organizationId string, params GetOrganizationBlueprintsParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetSharedBlueprintsByOrganizationId(c *gin.Context, organizationId string, params GetSharedBlueprintsByOrganizationIdParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetGuildsForOrganization(c *gin.Context, organizationId string, params GetGuildsForOrganizationParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) ListTags(c *gin.Context, params ListTagsParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetBlueprintsByTag(c *gin.Context, tag string, params GetBlueprintsByTagParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetAccessibleBlueprintsByUserId(c *gin.Context, userId string, params GetAccessibleBlueprintsByUserIdParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetUserBlueprints(c *gin.Context, userId string, params GetUserBlueprintsParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}

func (UnimplementedServer) GetGuildsForUser(c *gin.Context, userId string, params GetGuildsForUserParams) {
	c.JSON(http.StatusNotImplemented, gin.H{"detail": "not implemented"})
}
