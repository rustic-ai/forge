package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
)

type authorizeRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	RedirectURL  string `json:"redirectUrl"`
}

type authorizeResponse struct {
	AuthURL string `json:"authUrl"`
}

// validateOrgID rejects empty IDs and values containing | (the StoreKey
// delimiter) or control characters, which would interfere with key parsing
// and safe storage of the resulting key.
func validateOrgID(id string) error {
	if id == "" {
		return fmt.Errorf("org_id is required")
	}
	if strings.ContainsFunc(id, func(r rune) bool {
		return r == '|' || unicode.IsControl(r)
	}) {
		return fmt.Errorf("org_id contains invalid characters")
	}
	return nil
}

func (s *Server) registerOAuthRoutes(router *gin.Engine, prefix string) {
	s.oauthRoutePrefix = prefix
	router.GET(prefix+"/oauth/organizations/:org_id/providers", wrapHTTPWithPathValues(s.handleOAuthListProviders(), "org_id"))
	router.POST(prefix+"/oauth/organizations/:org_id/providers/:provider_id/authorize", wrapHTTPWithPathValues(s.handleOAuthAuthorize(), "org_id", "provider_id"))
	// Single, provider- and org-agnostic callback for every provider: the flow is
	// identified entirely by the opaque state inside ExchangeCode.
	router.GET(prefix+"/oauth/callback", wrapHTTP(s.handleOAuthCallback()))
	router.GET(prefix+"/oauth/organizations/:org_id/providers/:provider_id/status", wrapHTTPWithPathValues(s.handleOAuthStatus(), "org_id", "provider_id"))
	router.DELETE(prefix+"/oauth/organizations/:org_id/providers/:provider_id", wrapHTTPWithPathValues(s.handleOAuthDisconnect(), "org_id", "provider_id"))
}

func (s *Server) handleOAuthListProviders() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := strings.TrimSpace(r.PathValue("org_id"))
		if err := validateOrgID(orgID); err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, s.oauthManager.ListProviders(orgID, s.publicBaseURL()+s.oauthRoutePrefix))
	}
}

func (s *Server) handleOAuthAuthorize() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerID := strings.TrimSpace(r.PathValue("provider_id"))
		if !s.oauthManager.ProviderExists(providerID) {
			ReplyError(w, http.StatusNotFound, "unknown provider: "+providerID)
			return
		}

		var req authorizeRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		clientID := strings.TrimSpace(req.ClientID)
		clientSecret := strings.TrimSpace(req.ClientSecret)
		// Validation depends on the provider's auth mode. Static providers
		// require caller-supplied credentials; providers using Dynamic Client
		// Registration register their own and must not be sent any.
		if s.oauthManager.RequiresClientCredentials(providerID) {
			if clientID == "" || clientSecret == "" {
				ReplyError(w, http.StatusUnprocessableEntity, "clientId and clientSecret are required")
				return
			}
		} else if clientID != "" || clientSecret != "" {
			ReplyError(w, http.StatusUnprocessableEntity, "this provider uses dynamic client registration; do not send clientId or clientSecret")
			return
		}

		orgID := strings.TrimSpace(r.PathValue("org_id"))
		if err := validateOrgID(orgID); err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		redirectURL := req.RedirectURL
		if redirectURL == "" {
			// Single constant callback for all providers; the flow is identified
			// by state at callback time.
			redirectURL = s.publicBaseURL() + s.oauthRoutePrefix + "/oauth/callback"
		}

		authURL, _, err := s.oauthManager.GetAuthURL(r.Context(), orgID, providerID, clientID, clientSecret, redirectURL)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(authorizeResponse{AuthURL: authURL}) //nolint:errcheck
	}
}

func (s *Server) handleOAuthCallback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		state := strings.TrimSpace(r.URL.Query().Get("state"))

		if code == "" || state == "" {
			if errMsg := r.URL.Query().Get("error"); errMsg != "" {
				writeCallbackPage(w, false, "Authorization denied: "+errMsg)
				return
			}
			writeCallbackPage(w, false, "Missing code or state parameter")
			return
		}

		// The org and provider are recovered from the pendingFlow via state, so
		// the callback URL itself carries no path parameters.
		providerID, err := s.oauthManager.ExchangeCode(r.Context(), code, state)
		if err != nil {
			writeCallbackPage(w, false, "Failed to connect: "+err.Error())
			return
		}

		writeCallbackPage(w, true, s.oauthManager.ProviderDisplayName(providerID))
	}
}

func (s *Server) handleOAuthStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerID := strings.TrimSpace(r.PathValue("provider_id"))
		if !s.oauthManager.ProviderExists(providerID) {
			ReplyError(w, http.StatusNotFound, "unknown provider: "+providerID)
			return
		}

		orgID := strings.TrimSpace(r.PathValue("org_id"))
		if err := validateOrgID(orgID); err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, map[string]bool{
			"isConnected": s.oauthManager.IsConnected(orgID, providerID),
		})
	}
}

func (s *Server) handleOAuthDisconnect() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerID := strings.TrimSpace(r.PathValue("provider_id"))
		if !s.oauthManager.ProviderExists(providerID) {
			ReplyError(w, http.StatusNotFound, "unknown provider: "+providerID)
			return
		}

		orgID := strings.TrimSpace(r.PathValue("org_id"))
		if err := validateOrgID(orgID); err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		disconnected := s.oauthManager.Disconnect(orgID, providerID)
		ReplyJSON(w, http.StatusOK, map[string]interface{}{
			"providerId":   providerID,
			"disconnected": disconnected,
		})
	}
}

// publicBaseURL returns the externally reachable base URL for this server.
// It prefers FORGE_MANAGER_API_BASE_URL (set via --manager-api-base-url) and
// falls back to deriving from the bind address.
func (s *Server) publicBaseURL() string {
	if base := strings.TrimRight(os.Getenv("FORGE_MANAGER_API_BASE_URL"), "/"); base != "" {
		return base
	}
	return listenAddrToBaseURL(s.listenAddr)
}

func listenAddrToBaseURL(listenAddr string) string {
	host := listenAddr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	return "http://" + host
}

func writeCallbackPage(w http.ResponseWriter, success bool, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if success {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Connected</title></head><body>` +
			`<h2>Connected to ` + detail + `</h2>` +
			`<p>Authentication successful. You can close this tab and return to the app.</p>` +
			`</body></html>`))
	} else {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Error</title></head><body>` +
			`<h2>Connection failed</h2><p>` + detail + `</p>` +
			`</body></html>`))
	}
}
