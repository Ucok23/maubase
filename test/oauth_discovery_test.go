package e2e_test

import (
	"net/http"
	"testing"

	"github.com/Ucok23/maubase/internal/testserver"
)

// Scenarios: spec/oauth-discovery.md

func TestOAuthDiscovery_AuthServerMetadata(t *testing.T) {
	baseURL := testserver.New(t)

	// DISC-01
	resp, err := http.Get(baseURL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	meta := decodeJSONMap(t, resp)

	required := []string{
		"issuer", "authorization_endpoint", "token_endpoint",
		"registration_endpoint", "revocation_endpoint", "jwks_uri",
		"scopes_supported", "grant_types_supported",
		"code_challenge_methods_supported",
	}
	for _, field := range required {
		if meta[field] == nil {
			t.Errorf("want %q in discovery metadata, got %v", field, meta)
		}
	}
	if meta["issuer"] != baseURL {
		t.Errorf("want issuer to be this server's own base URL %q, got %v", baseURL, meta["issuer"])
	}
}

func TestOAuthDiscovery_JWKS(t *testing.T) {
	baseURL := testserver.New(t)

	// DISC-02
	resp, err := http.Get(baseURL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("GET jwks: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body := decodeJSONMap(t, resp)
	keys, _ := body["keys"].([]any)
	if len(keys) == 0 {
		t.Fatalf("want at least one signing key published, got %v", body)
	}
	key, _ := keys[0].(map[string]any)
	for _, field := range []string{"kid", "kty", "n", "e"} {
		if key[field] == nil {
			t.Errorf("want %q on the published key, got %v", field, key)
		}
	}
}
