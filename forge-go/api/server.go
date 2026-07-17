package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rustic-ai/forge/forge-go/api/contract"
	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/gateway"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/keychain"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/modelfit"
	"github.com/rustic-ai/forge/forge-go/oauth"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

type Server struct {
	contract.UnimplementedServer

	store            store.Store
	statusStore      supervisor.AgentStatusStore
	controlPusher    protocol.ControlPusher
	msgClient        messaging.Backend
	infraPublisher   *infraevents.Publisher
	fileStore        *filesystem.LocalFileStore
	localUI          *localUIState
	observeService   *observeService
	modelFit         *modelFitService
	oauthManager     *oauth.Manager
	oauthRoutePrefix string
	secretManager    *secrets.Manager
	listenAddr       string
	server           *http.Server
}

func NewServer(db store.Store, statusStore supervisor.AgentStatusStore, controlPusher protocol.ControlPusher, mc messaging.Backend, fs *filesystem.LocalFileStore, listenAddr string) *Server {
	var infraPublisher *infraevents.Publisher
	if mc != nil {
		infraPublisher, _ = infraevents.NewPublisher(mc)
	}
	s := &Server{
		store:          db,
		statusStore:    statusStore,
		controlPusher:  controlPusher,
		msgClient:      mc,
		infraPublisher: infraPublisher,
		fileStore:      fs,
		localUI:        newLocalUIState(),
		listenAddr:     listenAddr,
	}
	return s
}

// WithOAuth initialises OAuth support. Both store backends are selected from the
// environment: FORGE_OAUTH_TOKEN_STORE for tokens and FORGE_OAUTH_CLIENT_STORE for
// DCR client credentials, each "memory" (default) or "keychain".
func (s *Server) WithOAuth() *Server {
	kind := os.Getenv("FORGE_OAUTH_TOKEN_STORE")
	cfg, err := oauth.LoadProvidersConfig(forgepath.OAuthProvidersConfigPath())
	if err != nil {
		fmt.Printf("WARN: failed to load OAuth providers config: %v\n", err)
		return s
	}
	store, err := oauth.NewTokenStore(kind)
	if err != nil {
		fmt.Printf("WARN: %v; falling back to in-memory token store\n", err)
		store, _ = oauth.NewTokenStore("memory")
	}
	// Client credentials use their own backend (empty -> in-memory).
	credStore, err := oauth.NewClientCredentialsStore(os.Getenv("FORGE_OAUTH_CLIENT_STORE"))
	if err != nil {
		fmt.Printf("WARN: %v; falling back to in-memory client credentials store\n", err)
		credStore, _ = oauth.NewClientCredentialsStore("memory")
	}
	// FORGE_OAUTH_CLIENT_NAME / FORGE_OAUTH_CLIENT_URI override the client_name
	// and client_uri registered with DCR providers; empty keeps the defaults
	// (name "Forge", uri omitted).
	s.oauthManager = oauth.NewManagerWithStores(cfg, store, credStore,
		oauth.WithDynamicClient(os.Getenv("FORGE_OAUTH_CLIENT_NAME"), os.Getenv("FORGE_OAUTH_CLIENT_URI")))
	keychain.SetOAuthManager(s.oauthManager)
	return s
}

// OAuthManager returns the oauth.Manager initialised by WithOAuth, or nil if OAuth is not configured.
func (s *Server) OAuthManager() *oauth.Manager {
	return s.oauthManager
}

// WithSecrets initialises org-scoped secret management. The store backend is
// selected from FORGE_SECRET_STORE ("memory" (default) or "keychain"), mirroring
// the OAuth token store. With the keychain backend, secrets are stored under
// "secret:orgID|name" and resolve through the keychain SecretProvider directly —
// no delegation is needed since the stored value is the secret itself.
func (s *Server) WithSecrets() *Server {
	store, err := secrets.NewSecretStore(os.Getenv("FORGE_SECRET_STORE"))
	if err != nil {
		fmt.Printf("WARN: %v; falling back to in-memory secret store\n", err)
		store, _ = secrets.NewSecretStore("memory")
	}
	s.secretManager = secrets.NewManager(store)
	return s
}

// SecretManager returns the secrets.Manager initialised by WithSecrets, or nil
// if secret management is not configured.
func (s *Server) SecretManager() *secrets.Manager {
	return s.secretManager
}

func (s *Server) WithObservability(mode, sqliteDBPath string) *Server {
	s.observeService = newObserveService(mode, sqliteDBPath)
	return s
}

func (s *Server) WithModelFit(catalogPath, dependencyConfigPath string, profiler modelfit.Profiler) *Server {
	if profiler == nil {
		profiler = modelfit.DefaultProfiler{}
	}
	if strings.TrimSpace(catalogPath) == "" {
		catalogPath = forgepath.LocalModelCatalogPath()
	}
	if strings.TrimSpace(dependencyConfigPath) == "" {
		dependencyConfigPath = forgepath.DependencyConfigPath()
	}
	s.modelFit = newModelFitService(catalogPath, dependencyConfigPath, profiler)
	return s
}

func (s *Server) Start(ctx context.Context) error {
	gin.SetMode(gin.ReleaseMode)
	router := s.buildRouter()

	s.server = &http.Server{
		Addr:    s.listenAddr,
		Handler: WithLogging(WithRecovery(WithCORS(WithJSONResponse(WithTelemetry("forge.http", router))))),
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
	router.DELETE("/nodes/:node_id", wrapHTTPWithPathValues(NodeDeregisterHandler, "node_id"))

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
		router.GET("/catalog/blueprints/:blueprint_id/dependencies", wrapHTTPWithPathValues(handleGetBlueprintDependencies(s.store), "blueprint_id"))
		router.GET("/dependencies", wrapHTTP(handleListConfiguredDependencies()))
		router.GET("/dependencies/provided-type/:provided_type", wrapHTTPWithPathValues(handleListConfiguredDependencies(), "provided_type"))
		router.GET("/catalog/agents/:class_name/dependencies", wrapHTTPWithPathValues(handleGetCatalogAgentDependenciesByClassName(s.store), "class_name"))
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
		if s.oauthManager != nil {
			s.registerOAuthRoutes(router, "/rustic")
		}
		if s.secretManager != nil {
			s.registerSecretRoutes(router, "/rustic")
		}
		router.GET("/rustic/modelfit/local-models", wrapHTTP(s.handleListLocalModelFits()))
		router.GET("/rustic/modelfit/capabilities", wrapHTTP(s.handleGetModelFitCapabilities()))
		router.GET("/rustic/observe/guilds/:guild_id/messages/:msg_id/spans", wrapHTTPWithPathValues(s.handleObserveMessageSpans(), "guild_id", "msg_id"))
		router.GET("/rustic/catalog/blueprints/:blueprint_id/dependencies", wrapHTTPWithPathValues(handleGetBlueprintDependencies(s.store), "blueprint_id"))
		router.GET("/rustic/dependencies", wrapHTTP(handleListConfiguredDependencies()))
		router.GET("/rustic/dependencies/provided-type/:provided_type", wrapHTTPWithPathValues(handleListConfiguredDependencies(), "provided_type"))
		router.GET("/rustic/catalog/agents/:class_name/dependencies", wrapHTTPWithPathValues(handleGetCatalogAgentDependenciesByClassName(s.store), "class_name"))
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

func wrapHTTP(handler http.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
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
