package oauth

import "testing"

func TestWellKnownURL(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		suffix string
		want   string
	}{
		{
			name:   "host with path (RFC 9728 path insertion)",
			base:   "https://example.com/mcp",
			suffix: "oauth-protected-resource",
			want:   "https://example.com/.well-known/oauth-protected-resource/mcp",
		},
		{
			name:   "bare host",
			base:   "https://example.com",
			suffix: "oauth-authorization-server",
			want:   "https://example.com/.well-known/oauth-authorization-server",
		},
		{
			name:   "trailing slash is trimmed",
			base:   "https://example.com/a/b/",
			suffix: "oauth-authorization-server",
			want:   "https://example.com/.well-known/oauth-authorization-server/a/b",
		},
		{
			name:   "query and fragment are dropped",
			base:   "https://example.com/mcp?x=1#frag",
			suffix: "oauth-protected-resource",
			want:   "https://example.com/.well-known/oauth-protected-resource/mcp",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wellKnownURL(tc.base, tc.suffix)
			if err != nil {
				t.Fatalf("wellKnownURL(%q) error: %v", tc.base, err)
			}
			if got != tc.want {
				t.Errorf("wellKnownURL(%q, %q) = %q, want %q", tc.base, tc.suffix, got, tc.want)
			}
		})
	}
}

func TestWellKnownURL_Invalid(t *testing.T) {
	for _, base := range []string{"", "not-a-url", "/relative/path", "mcp.notion.com/mcp"} {
		if _, err := wellKnownURL(base, "oauth-protected-resource"); err == nil {
			t.Errorf("wellKnownURL(%q) expected error, got nil", base)
		}
	}
}

func TestResolvedProvider_AuthMethodSelection(t *testing.T) {
	cases := []struct {
		name       string
		methods    []string
		wantPublic bool
		wantMethod string
	}{
		{"public preferred", []string{"client_secret_basic", "client_secret_post", "none"}, true, "none"},
		{"post fallback", []string{"client_secret_basic", "client_secret_post"}, false, "client_secret_post"},
		{"basic fallback", []string{"client_secret_basic"}, false, "client_secret_basic"},
		{"empty defaults to basic", nil, false, "client_secret_basic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &resolvedProvider{authMethods: tc.methods}
			if got := r.supportsPublicClient(); got != tc.wantPublic {
				t.Errorf("supportsPublicClient() = %v, want %v", got, tc.wantPublic)
			}
			if got := r.tokenEndpointAuthMethod(); got != tc.wantMethod {
				t.Errorf("tokenEndpointAuthMethod() = %q, want %q", got, tc.wantMethod)
			}
		})
	}
}

func TestProviderConfig_RequiresClientCredentials(t *testing.T) {
	static := ProviderConfig{AuthURL: "https://x/a", TokenURL: "https://x/t"}
	if !static.RequiresClientCredentials() {
		t.Error("static provider should require client credentials")
	}
	dcr := ProviderConfig{ResourceURL: "https://mcp.example.com/mcp", UseDCRP: true}
	if dcr.RequiresClientCredentials() {
		t.Error("DCR provider should not require client credentials")
	}
}
