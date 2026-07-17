package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/secrets"
)

// b64 encodes a plaintext secret value the way API clients must send it.
func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// capturingStore wraps the in-memory store to expose the last value passed to
// Save, so tests can assert the handler stores the decoded (not base64) value.
type capturingStore struct {
	*secrets.InMemorySecretStore
	lastValue string
}

func (c *capturingStore) Save(orgID, name, value string) error {
	c.lastValue = value
	return c.InMemorySecretStore.Save(orgID, name, value)
}

func newSecretsServer() *Server {
	return &Server{secretManager: secrets.NewManager(secrets.NewInMemorySecretStore())}
}

func doSecretReq(t *testing.T, h http.HandlerFunc, method, path, body string, pathVals map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	rr := httptest.NewRecorder()
	h(rr, r)
	return rr
}

func TestHandleCreateSecret_Success(t *testing.T) {
	store := &capturingStore{InMemorySecretStore: secrets.NewInMemorySecretStore()}
	s := &Server{secretManager: secrets.NewManager(store)}
	rr := doSecretReq(t, s.handleCreateSecret(), http.MethodPost,
		"/rustic/organizations/org1/secrets",
		`{"name":"API_KEY","value":"`+b64("s3cr3t")+`"}`,
		map[string]string{"org_id": "org1"})

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "s3cr3t") {
		t.Fatal("response must never echo the secret value")
	}
	if !s.secretManager.Exists("org1", "API_KEY") {
		t.Fatal("secret not stored")
	}
	if store.lastValue != "s3cr3t" {
		t.Fatalf("stored value must be the decoded secret, got %q", store.lastValue)
	}
}

func TestHandleCreateSecret_InvalidBase64(t *testing.T) {
	s := newSecretsServer()
	rr := doSecretReq(t, s.handleCreateSecret(), http.MethodPost,
		"/rustic/organizations/org1/secrets",
		`{"name":"K","value":"not!base64"}`,
		map[string]string{"org_id": "org1"})

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestHandleCreateSecret_DuplicateConflict(t *testing.T) {
	s := newSecretsServer()
	s.secretManager.Set("org1", "API_KEY", "a") //nolint:errcheck

	rr := doSecretReq(t, s.handleCreateSecret(), http.MethodPost,
		"/rustic/organizations/org1/secrets",
		`{"name":"API_KEY","value":"`+b64("b")+`"}`,
		map[string]string{"org_id": "org1"})

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

func TestHandleCreateSecret_InvalidName(t *testing.T) {
	s := newSecretsServer()
	rr := doSecretReq(t, s.handleCreateSecret(), http.MethodPost,
		"/rustic/organizations/org1/secrets",
		`{"name":"bad name!","value":"v"}`,
		map[string]string{"org_id": "org1"})

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestHandleCreateSecret_EmptyValue(t *testing.T) {
	s := newSecretsServer()
	rr := doSecretReq(t, s.handleCreateSecret(), http.MethodPost,
		"/rustic/organizations/org1/secrets",
		`{"name":"K","value":""}`,
		map[string]string{"org_id": "org1"})

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestHandleCreateSecret_BlankValue(t *testing.T) {
	s := newSecretsServer()
	rr := doSecretReq(t, s.handleCreateSecret(), http.MethodPost,
		"/rustic/organizations/org1/secrets",
		`{"name":"K","value":"`+b64("   \t\n")+`"}`,
		map[string]string{"org_id": "org1"})

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestHandleUpdateSecret_Success(t *testing.T) {
	s := newSecretsServer()
	s.secretManager.Set("org1", "K", "v1") //nolint:errcheck

	rr := doSecretReq(t, s.handleUpdateSecret(), http.MethodPut,
		"/rustic/organizations/org1/secrets/K",
		`{"value":"`+b64("v2")+`"}`,
		map[string]string{"org_id": "org1", "name": "K"})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !s.secretManager.Exists("org1", "K") {
		t.Fatal("secret should still exist after update")
	}
}

func TestHandleUpdateSecret_NotFound(t *testing.T) {
	s := newSecretsServer()
	rr := doSecretReq(t, s.handleUpdateSecret(), http.MethodPut,
		"/rustic/organizations/org1/secrets/NOPE",
		`{"value":"`+b64("v")+`"}`,
		map[string]string{"org_id": "org1", "name": "NOPE"})

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestHandleDeleteSecret(t *testing.T) {
	s := newSecretsServer()
	s.secretManager.Set("org1", "K", "v") //nolint:errcheck

	rr := doSecretReq(t, s.handleDeleteSecret(), http.MethodDelete,
		"/rustic/organizations/org1/secrets/K", "",
		map[string]string{"org_id": "org1", "name": "K"})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Second delete should 404.
	rr = doSecretReq(t, s.handleDeleteSecret(), http.MethodDelete,
		"/rustic/organizations/org1/secrets/K", "",
		map[string]string{"org_id": "org1", "name": "K"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d", rr.Code)
	}
}

func TestHandleListSecrets_NoValues(t *testing.T) {
	s := newSecretsServer()
	s.secretManager.Set("org1", "A", "secretA") //nolint:errcheck
	s.secretManager.Set("org1", "B", "secretB") //nolint:errcheck

	rr := doSecretReq(t, s.handleListSecrets(), http.MethodGet,
		"/rustic/organizations/org1/secrets", "",
		map[string]string{"org_id": "org1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "secretA") || strings.Contains(body, "secretB") {
		t.Fatalf("list must not include secret values: %s", body)
	}

	var got struct {
		Secrets []string `json:"secrets"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(got.Secrets) != 2 || got.Secrets[0] != "A" || got.Secrets[1] != "B" {
		t.Fatalf("unexpected list: %+v", got.Secrets)
	}
}

func TestHandleCreateSecret_InvalidOrg(t *testing.T) {
	s := newSecretsServer()
	rr := doSecretReq(t, s.handleCreateSecret(), http.MethodPost,
		"/rustic/organizations//secrets",
		`{"name":"K","value":"v"}`,
		map[string]string{"org_id": ""})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}
