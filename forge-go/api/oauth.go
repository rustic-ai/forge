package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

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

func (s *Server) registerOAuthRoutes(router *gin.Engine, prefix string) {
	s.oauthRoutePrefix = prefix
	router.GET(prefix+"/oauth/providers", wrapHTTP(s.handleOAuthListProviders()))
	router.POST(prefix+"/oauth/providers/:provider_id/authorize", wrapHTTPWithPathValues(s.handleOAuthAuthorize(), "provider_id"))
	router.GET(prefix+"/oauth/providers/:provider_id/callback", wrapHTTPWithPathValues(s.handleOAuthCallback(), "provider_id"))
	router.GET(prefix+"/oauth/providers/:provider_id/status", wrapHTTPWithPathValues(s.handleOAuthStatus(), "provider_id"))
	router.DELETE(prefix+"/oauth/providers/:provider_id", wrapHTTPWithPathValues(s.handleOAuthDisconnect(), "provider_id"))
}

func (s *Server) handleOAuthListProviders() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := extractUserID(r)
		ReplyJSON(w, http.StatusOK, s.oauthManager.ListProviders(userID, s.publicBaseURL()+s.oauthRoutePrefix))
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
		if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.ClientSecret) == "" {
			ReplyError(w, http.StatusUnprocessableEntity, "clientId and clientSecret are required")
			return
		}

		redirectURL := req.RedirectURL
		if redirectURL == "" {
			redirectURL = s.publicBaseURL() + s.oauthRoutePrefix + "/oauth/providers/" + providerID + "/callback"
		}

		userID := extractUserID(r)
		authURL, _, err := s.oauthManager.GetAuthURL(userID, providerID, req.ClientID, req.ClientSecret, redirectURL)
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
		providerID := strings.TrimSpace(r.PathValue("provider_id"))
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

		// userID is recovered from pendingFlow via state — no header needed here
		// since the callback is driven by the browser redirect.
		if err := s.oauthManager.ExchangeCode(r.Context(), code, state); err != nil {
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

		userID := extractUserID(r)
		ReplyJSON(w, http.StatusOK, map[string]bool{
			"isConnected": s.oauthManager.IsConnected(userID, providerID),
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

		userID := extractUserID(r)
		disconnected := s.oauthManager.Disconnect(userID, providerID)
		ReplyJSON(w, http.StatusOK, map[string]interface{}{
			"providerId":   providerID,
			"disconnected": disconnected,
		})
	}
}

// extractUserID resolves the caller's user ID from the Authorization header.
// TODO: parse JWT claims from the Bearer token for real user identity once an
// identity provider is wired up (FORGE_IDENTITY_MODE != "local").
func extractUserID(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" || strings.EqualFold(auth, "bearer dummy token") {
		return localDummyUserID
	}
	// Fall back to dummy user until JWT parsing is implemented.
	return localDummyUserID
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
