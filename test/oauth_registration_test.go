package e2e_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Ucok23/maubase/internal/testserver"
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

func TestOAuthRegistration_PayloadBoundsEnforced(t *testing.T) {
	// REG-07
	baseURL := testserver.New(t)

	// An oversized client_name pushes the whole JSON body itself over the
	// server's byte cap (independent of the client_name-length check
	// below, which catches a body that's individually small enough but
	// whose client_name field alone is unreasonable).
	status, out := registerClient(t, baseURL, map[string]any{
		"redirect_uris": []string{"http://localhost:9999/cb"},
		"client_name":   strings.Repeat("a", 32*1024),
	})
	if status != http.StatusBadRequest {
		t.Fatalf("oversized body: want 400, got %d: %v", status, out)
	}

	// A moderately long client_name, individually under the byte cap but
	// over the field's own length limit.
	status, out = registerClient(t, baseURL, map[string]any{
		"redirect_uris": []string{"http://localhost:9999/cb"},
		"client_name":   strings.Repeat("a", 500),
	})
	if status != http.StatusBadRequest {
		t.Fatalf("overlong client_name: want 400, got %d: %v", status, out)
	}
	if out["error"] != "invalid_client_metadata" {
		t.Fatalf("overlong client_name: want error=invalid_client_metadata, got %v", out["error"])
	}

	// Too many redirect_uris.
	many := make([]string, 25)
	for i := range many {
		many[i] = "http://localhost:9999/cb"
	}
	status, out = registerClient(t, baseURL, map[string]any{"redirect_uris": many})
	if status != http.StatusBadRequest {
		t.Fatalf("too many redirect_uris: want 400, got %d: %v", status, out)
	}
	if out["error"] != "invalid_redirect_uri" {
		t.Fatalf("too many redirect_uris: want error=invalid_redirect_uri, got %v", out["error"])
	}

	// A single overlong redirect_uri.
	status, out = registerClient(t, baseURL, map[string]any{
		"redirect_uris": []string{"http://localhost:9999/" + strings.Repeat("a", 3000)},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("overlong redirect_uri: want 400, got %d: %v", status, out)
	}
	if out["error"] != "invalid_redirect_uri" {
		t.Fatalf("overlong redirect_uri: want error=invalid_redirect_uri, got %v", out["error"])
	}
}

func TestOAuthRegistration_RejectsMalformedRedirectURI(t *testing.T) {
	// REG-08
	baseURL := testserver.New(t)

	for _, bad := range []string{
		"not a url at all",
		"/just/a/relative/path",
		"http://localhost:9999/cb#fragment-not-allowed",
	} {
		status, out := registerClient(t, baseURL, map[string]any{"redirect_uris": []string{bad}})
		if status != http.StatusBadRequest {
			t.Fatalf("redirect_uri %q: want 400, got %d: %v", bad, status, out)
		}
		if out["error"] != "invalid_redirect_uri" {
			t.Fatalf("redirect_uri %q: want error=invalid_redirect_uri, got %v", bad, out["error"])
		}
	}
}

func TestOAuthRegistration_IsRateLimited(t *testing.T) {
	// REG-07
	baseURL := testserver.NewCustom(t, testserver.Options{LoginRateLimit: 2, LoginRateWindow: 60 * time.Second})

	body := map[string]any{"redirect_uris": []string{"http://localhost:9999/cb"}}
	for i := 0; i < 2; i++ {
		status, out := registerClient(t, baseURL, body)
		if status != http.StatusCreated {
			t.Fatalf("attempt %d: want 201, got %d: %v", i+1, status, out)
		}
	}
	status, out := registerClient(t, baseURL, body)
	if status != http.StatusTooManyRequests {
		t.Fatalf("attempt over the limit: want 429, got %d: %v", status, out)
	}
}
