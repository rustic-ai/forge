package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/api/contract"
	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/gateway"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

type Server struct {
	contract.UnimplementedServer

	store       store.Store
	redisClient *redis.Client
	msgClient   *messaging.Client
	fileStore   *filesystem.LocalFileStore
	localUI     *localUIState
	listenAddr  string
	server      *http.Server
}

func NewServer(db store.Store, rc *redis.Client, mc *messaging.Client, fs *filesystem.LocalFileStore, listenAddr string) *Server {
	return &Server{
		store:       db,
		redisClient: rc,
		msgClient:   mc,
		fileStore:   fs,
		localUI:     newLocalUIState(),
		listenAddr:  listenAddr,
	}
}

func (s *Server) Start(ctx context.Context) error {
	gin.SetMode(gin.ReleaseMode)
	router := s.buildRouter()

	s.server = &http.Server{
		Addr:    s.listenAddr,
		Handler: WithLogging(WithRecovery(WithCORS(WithJSONResponse(router)))),
	}

	errChan := make(chan error, 1)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		return s.server.Shutdown(context.Background())
	case err := <-errChan:
		return fmt.Errorf("http server error: %w", err)
	}
}

func (s *Server) buildRouter() *gin.Engine {
	router := gin.New()
	router.RedirectTrailingSlash = true
	router.GET("/ping", s.Healthz)

	router.POST("/manager/guilds/ensure", wrapHTTPWithPathValues(s.HandleManagerEnsureGuild))
	router.GET("/manager/guilds/:guild_id/spec", wrapHTTPWithPathValues(s.HandleManagerGetGuildSpec, "guild_id"))
	router.PATCH("/manager/guilds/:guild_id/status", wrapHTTPWithPathValues(s.HandleManagerUpdateGuildStatus, "guild_id"))
	router.POST("/manager/guilds/:guild_id/agents/ensure", wrapHTTPWithPathValues(s.HandleManagerEnsureAgent, "guild_id"))
	router.PATCH(
		"/manager/guilds/:guild_id/agents/:agent_id/status",
		wrapHTTPWithPathValues(s.HandleManagerUpdateAgentStatus, "guild_id", "agent_id"),
	)
	router.POST("/manager/guilds/:guild_id/routes", wrapHTTPWithPathValues(s.HandleManagerAddRoutingRule, "guild_id"))
	router.DELETE(
		"/manager/guilds/:guild_id/routes/:rule_hashid",
		wrapHTTPWithPathValues(s.HandleManagerRemoveRoutingRule, "guild_id", "rule_hashid"),
	)
	router.POST(
		"/manager/guilds/:guild_id/lifecycle/heartbeat",
		wrapHTTPWithPathValues(s.HandleManagerProcessHeartbeat, "guild_id"),
	)

	enablePublic := envBool("FORGE_ENABLE_PUBLIC_API", true)
	enableUI := envBool("FORGE_ENABLE_UI_API", true)
	identityMode := envString("FORGE_IDENTITY_MODE", "local")
	quotaMode := envString("FORGE_QUOTA_MODE", "local")

	// WebSocket endpoints remain manual and use a path-value adapter.
	gemGen, _ := protocol.NewGemstoneGenerator(1)
	if enablePublic {
		router.GET("/ws/guilds/:id/usercomms/:user_id/:user_name", wrapHTTPWithPathValues(gateway.UserCommsHandler(s.msgClient, s.store, gemGen), "id", "user_id", "user_name"))
		router.GET("/ws/guilds/:id/syscomms/:user_id", wrapHTTPWithPathValues(gateway.SysCommsHandler(s.msgClient, s.store, gemGen), "id", "user_id"))

		// Contract-first REST surface.
		contract.RegisterHandlersWithOptions(router, s, contract.GinServerOptions{
			ErrorHandler: func(c *gin.Context, err error, statusCode int) {
				ReplyError(c.Writer, statusCode, err.Error())
			},
		})
	}

	if enablePublic && identityMode == "local" {
		s.registerLocalIdentityRoutes(router)
	}
	if enablePublic && quotaMode == "local" {
		s.registerLocalQuotaRoutes(router)
	}
	if enableUI {
		s.registerRusticUIRoutes(router, gemGen)
		contract.RegisterHandlersWithOptions(router, s, contract.GinServerOptions{
			BaseURL: "/rustic",
			ErrorHandler: func(c *gin.Context, err error, statusCode int) {
				ReplyError(c.Writer, statusCode, err.Error())
			},
		})
	}
	return router
}

func wrapHTTPWithPathValues(handler http.HandlerFunc, params ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, p := range params {
			c.Request.SetPathValue(p, c.Param(p))
		}
		handler(c.Writer, c.Request)
	}
}

func envBool(name string, defaultVal bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return defaultVal
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return defaultVal
	}
}

func envString(name, defaultVal string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return defaultVal
	}
	return strings.ToLower(v)
}
