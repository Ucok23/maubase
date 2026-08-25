package e2e_test

import (
	"net/http"
	"testing"

	"baas/internal/testserver"
)

// Scenarios: spec/oauth-client-registration.md

func TestOAuthRegistration_Confidential(t *testing.T) {
	baseURL := testserver.New(t)

	// REG-01
	status, out := registerClient(t, baseURL, map[string]any{
		"redirect_uris":              []string{"http://localhost:9999/cb"},
		"token_endpoint_auth_method": "client_secret_basic",
	})
	if status != http.StatusCreated {
		t.Fatalf("want 201, got %d: %v", status, out)
	}
	if out["client_id"] == "" || out["client_id"] == nil {
		t.Fatalf("want a client_id, got %v", out)
	}
	if out["client_secret"] == "" || out["client_secret"] == nil {
		t.Fatalf("want a client_secret for a confidential client, got %v", out)
	}
}

func TestOAuthRegistration_Public(t *testing.T) {
	baseURL := testserver.New(t)

	// REG-02
	status, out := registerClient(t, baseURL, map[string]any{
		"redirect_uris":              []string{"http://localhost:9999/cb"},
		"token_endpoint_auth_method": "none",
	})
	if status != http.StatusCreated {
		t.Fatalf("want 201, got %d: %v", status, out)
	}
	if _, has := out["client_secret"]; has && out["client_secret"] != "" {
		t.Fatalf("want no client_secret for a public client, got %v", out["client_secret"])
	}
}

func TestOAuthRegistration_RequiresRedirectURI(t *testing.T) {
	baseURL := testserver.New(t)

	// REG-03
	status, out := registerClient(t, baseURL, map[string]any{
		"token_endpoint_auth_method": "none",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %v", status, out)
	}
	if out["error"] != "invalid_redirect_uri" {
		t.Fatalf("want error=invalid_redirect_uri, got %v", out["error"])
	}
}

func TestOAuthRegistration_RejectsUnsupportedGrantType(t *testing.T) {
	baseURL := testserver.New(t)

	// REG-04
	status, out := registerClient(t, baseURL, map[string]any{
		"redirect_uris": []string{"http://localhost:9999/cb"},
		"grant_types":   []string{"implicit"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %v", status, out)
	}
	if out["error"] != "invalid_client_metadata" {
		t.Fatalf("want error=invalid_client_metadata, got %v", out["error"])
	}
}

func TestOAuthRegistration_RejectsUnknownScope(t *testing.T) {
	baseURL := testserver.New(t)

	// REG-05
	status, out := registerClient(t, baseURL, map[string]any{
		"redirect_uris": []string{"http://localhost:9999/cb"},
		"scope":         "not-a-real-scope",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %v", status, out)
	}
	if out["error"] != "invalid_client_metadata" {
		t.Fatalf("want error=invalid_client_metadata, got %v", out["error"])
	}
}

func TestOAuthRegistration_Defaults(t *testing.T) {
	baseURL := testserver.New(t)

	// REG-06
	status, out := registerClient(t, baseURL, map[string]any{
		"redirect_uris": []string{"http://localhost:9999/cb"},
	})
	if status != http.StatusCreated {
		t.Fatalf("want 201, got %d: %v", status, out)
	}
	if out["token_endpoint_auth_method"] != "client_secret_basic" {
		t.Fatalf("want default token_endpoint_auth_method client_secret_basic, got %v", out["token_endpoint_auth_method"])
	}
	grantTypes, _ := out["grant_types"].([]any)
	if len(grantTypes) != 1 || grantTypes[0] != "authorization_code" {
		t.Fatalf("want default grant_types [authorization_code], got %v", out["grant_types"])
	}
	responseTypes, _ := out["response_types"].([]any)
	if len(responseTypes) != 1 || responseTypes[0] != "code" {
		t.Fatalf("want default response_types [code], got %v", out["response_types"])
	}
}
