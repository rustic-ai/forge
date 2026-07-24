package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rustic-ai/forge/forge-go/secrets"
)

// maxSecretValueBytes caps the size of a stored secret value.
const maxSecretValueBytes = 64 * 1024

type createSecretRequest struct {
	Name string `json:"name"`
	// Value is the secret, base64-encoded (standard encoding). The handler
	// decodes it before storage; the raw bytes are never persisted encoded.
	Value string `json:"value"`
}

type updateSecretRequest struct {
	// Value is the secret, base64-encoded (standard encoding). See
	// createSecretRequest.Value.
	Value string `json:"value"`
}

// validateSecretName restricts secret names to non-empty alphanumeric and
// underscore characters. This keeps names safe as keychain accounts and free of
// the "|" StoreKey delimiter.
func validateSecretName(name string) error {
	if name == "" {
		return fmt.Errorf("secret name is required")
	}
	if len(name) > 255 {
		return fmt.Errorf("secret name must be at most 255 characters")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return fmt.Errorf("secret name must contain only alphanumeric characters and underscores")
		}
	}
	return nil
}

// decodeSecretValue decodes the base64-encoded value carried in create/update
// requests and enforces the size limit on the decoded bytes (what is actually
// stored), not on the inflated base64 representation.
func decodeSecretValue(encoded string) (string, error) {
	if encoded == "" {
		return "", fmt.Errorf("secret value is required")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("secret value must be valid base64")
	}
	if len(decoded) == 0 {
		return "", fmt.Errorf("secret value is required")
	}
	// Reject an all-whitespace value: it is almost always a fat-finger and
	// behaves like an empty secret. Internal/leading/trailing whitespace on an
	// otherwise non-blank value is preserved, since it can be significant.
	if strings.TrimSpace(string(decoded)) == "" {
		return "", fmt.Errorf("secret value must not be blank")
	}
	if len(decoded) > maxSecretValueBytes {
		return "", fmt.Errorf("secret value must be at most %d bytes", maxSecretValueBytes)
	}
	return string(decoded), nil
}

// The org-scoped secret CRUD endpoints are part of the generated contract
// (see api/contract_server.go). There is deliberately no endpoint that returns
// a secret value; values are only ever read internally through the
// SecretProvider chain.

func (s *Server) handleListSecrets() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := strings.TrimSpace(r.PathValue("org_id"))
		if err := validateOrgID(orgID); err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		names, err := s.secretManager.List(orgID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, map[string]interface{}{"secrets": names})
	}
}

func (s *Server) handleCreateSecret() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := strings.TrimSpace(r.PathValue("org_id"))
		if err := validateOrgID(orgID); err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		var req createSecretRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		name := strings.TrimSpace(req.Name)
		if err := validateSecretName(name); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		value, err := decodeSecretValue(req.Value)
		if err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		err = s.secretManager.Set(orgID, name, value)
		if errors.Is(err, secrets.ErrSecretExists) {
			ReplyError(w, http.StatusConflict, "secret already exists: "+name)
			return
		}
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ReplyJSON(w, http.StatusCreated, map[string]interface{}{"name": name})
	}
}

func (s *Server) handleUpdateSecret() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := strings.TrimSpace(r.PathValue("org_id"))
		if err := validateOrgID(orgID); err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		name := strings.TrimSpace(r.PathValue("name"))
		if err := validateSecretName(name); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		var req updateSecretRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		value, err := decodeSecretValue(req.Value)
		if err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		err = s.secretManager.Update(orgID, name, value)
		if errors.Is(err, secrets.ErrSecretNotFound) {
			ReplyError(w, http.StatusNotFound, "secret not found: "+name)
			return
		}
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ReplyJSON(w, http.StatusOK, map[string]interface{}{"name": name})
	}
}

func (s *Server) handleDeleteSecret() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := strings.TrimSpace(r.PathValue("org_id"))
		if err := validateOrgID(orgID); err != nil {
			ReplyError(w, http.StatusBadRequest, err.Error())
			return
		}
		name := strings.TrimSpace(r.PathValue("name"))
		if err := validateSecretName(name); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if !s.secretManager.Delete(orgID, name) {
			ReplyError(w, http.StatusNotFound, "secret not found: "+name)
			return
		}
		ReplyJSON(w, http.StatusOK, map[string]interface{}{
			"name":    name,
			"deleted": true,
		})
	}
}
