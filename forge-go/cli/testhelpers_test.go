package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newMiniredis starts an in-memory redis and returns it plus a stop func.
func newMiniredis(t *testing.T) (*miniredis.Miniredis, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	return mr, mr.Close
}

// newRedisClient builds a *redis.Client pointed at mr, closed on test cleanup.
func newRedisClient(t *testing.T, mr *miniredis.Miniredis) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// newMiniredisRuntime returns a GuildRuntime wired to an in-memory miniredis
// instance, suitable for exercising the redis-backed methods. The miniredis
// handle is returned so tests can seed keys directly (mr.Set(...)).
func newMiniredisRuntime(t *testing.T) (*GuildRuntime, *miniredis.Miniredis) {
	t.Helper()
	mr, stop := newMiniredis(t)
	t.Cleanup(stop)

	return &GuildRuntime{
		config:      RuntimeConfig{UserID: "test-user", OrgID: "test-org"},
		redisClient: newRedisClient(t, mr),
		ctx:         context.Background(),
		agentNames:  make(map[string]string),
	}, mr
}

// newHTTPRuntime returns a GuildRuntime whose catalog/server base URLs point at
// an httptest.Server driven by handler.
func newHTTPRuntime(t *testing.T, handler http.Handler) *GuildRuntime {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &GuildRuntime{
		config:     RuntimeConfig{UserID: "test-user", OrgID: "test-org"},
		serverBase: ts.URL,
		rusticBase: ts.URL,
		ctx:        context.Background(),
		agentNames: make(map[string]string),
	}
}

// statusKey builds the redis key GetAgentStatuses reads:
// forge:agent:status:<guildID>:<agentID>.
func statusKey(guildID, agentID string) string {
	return "forge:agent:status:" + guildID + ":" + agentID
}

// seedStatus writes value at key in mr, failing the test if the seed fails.
func seedStatus(t *testing.T, mr *miniredis.Miniredis, key, value string) {
	t.Helper()
	if err := mr.Set(key, value); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}
