package e2e_test

import (
	"net/http"
	"testing"

	"github.com/Ucok23/maubase/internal/testserver"
)

// Scenarios: spec/oauth-resource-access.md
//
// /api/oauth/whoami (see internal/oauth.HandleWhoAmI) is the demo resource
// these exercise: it requires the "profile" scope.

func TestOAuthResource_ValidTokenWithScopeAccepted(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")

	client := newClient(t)
	signUp(t, client, baseURL, "res01@example.com", "correcthorse")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"profile"})
	accessToken, _ := tok["access_token"].(string)

	// RES-01
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/oauth/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/oauth/whoami: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	body := decodeJSONMap(t, resp)
	if body["subject"] == nil || body["subject"] == "" {
		t.Fatalf("want the resource to identify the subject, got %v", body)
	}
}

func TestOAuthResource_MissingTokenRejected(t *testing.T) {
	baseURL := testserver.New(t)

	// RES-02
	resp, err := http.Get(baseURL + "/api/oauth/whoami")
	if err != nil {
		t.Fatalf("GET /api/oauth/whoami: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestOAuthResource_InsufficientScopeRejected(t *testing.T) {
	baseURL := testserver.New(t)
	// Client only ever requests records:read, never profile.
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "records:read")

	client := newClient(t)
	signUp(t, client, baseURL, "res03@example.com", "correcthorse")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"records:read"})
	accessToken, _ := tok["access_token"].(string)

	// RES-03: whoami requires "profile", this token only has "records:read".
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/oauth/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/oauth/whoami: %v", err)
	}
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("want the request rejected for insufficient scope, got 200: %s", bodyString(t, resp))
	}
}

func TestOAuthResource_RevokedTokenRejected(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")

	client := newClient(t)
	signUp(t, client, baseURL, "res04@example.com", "correcthorse")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"profile"})
	accessToken, _ := tok["access_token"].(string)

	// Confirm it works before revocation, so the test actually proves
	// revocation is what changed the outcome.
	before := mustAuthedGet(t, baseURL+"/api/oauth/whoami", accessToken)
	if before.StatusCode != http.StatusOK {
		t.Fatalf("setup: token should work before revocation, got %d", before.StatusCode)
	}

	// RES-04
	revokeResp, err := http.PostForm(baseURL+"/oauth/revoke", map[string][]string{
		"token":     {accessToken},
		"client_id": {clientID},
	})
	if err != nil {
		t.Fatalf("POST /oauth/revoke: %v", err)
	}
	if revokeResp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from revoke, got %d: %s", revokeResp.StatusCode, bodyString(t, revokeResp))
	}

	after := mustAuthedGet(t, baseURL+"/api/oauth/whoami", accessToken)
	if after.StatusCode == http.StatusOK {
		t.Fatalf("want the revoked token rejected, got 200: %s", bodyString(t, after))
	}
}

func mustAuthedGet(t *testing.T, url, bearer string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}
