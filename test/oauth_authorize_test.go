package e2e_test

import (
	"net/http"
	"strings"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/oauth-authorize-and-consent.md

const testRedirectURI = "http://localhost:9999/cb"

func TestOAuthAuthorize_AnonymousSeesLoginForm(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")
	_, challenge := pkcePair(t)

	// AUTHZ-01
	client := newClient(t) // never signed in
	q := authorizeParams(clientID, testRedirectURI, "profile", "state12345678", challenge)
	resp, err := client.Get(baseURL + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatalf("GET /oauth/authorize: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 (login form), got %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "<form") || !strings.Contains(body, "password") {
		t.Fatalf("want a login form in the body, got: %s", body)
	}
}

func TestOAuthAuthorize_FirstTimeAsksConsentThenGrantsCode(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile records:read")
	verifier, challenge := pkcePair(t)

	client := newClient(t)
	signUp(t, client, baseURL, "consent1@example.com", "correcthorse")

	q := authorizeParams(clientID, testRedirectURI, "profile records:read", "state12345678", challenge)

	// AUTHZ-02: signed in, never consented before -> consent screen, no code.
	getResp, err := client.Get(baseURL + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatalf("GET /oauth/authorize: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 (consent screen), got %d", getResp.StatusCode)
	}
	consentBody := bodyString(t, getResp)
	if !strings.Contains(consentBody, "profile") || !strings.Contains(consentBody, "records:read") {
		t.Fatalf("want requested scopes listed on consent screen, got: %s", consentBody)
	}

	// AUTHZ-03: approving redirects back with a code and the original state.
	resp := approveConsent(t, client, baseURL, q, []string{"profile", "records:read"}, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	loc := locationQuery(t, resp)
	if loc.Get("code") == "" {
		t.Fatalf("want a code in the redirect, got %v", loc)
	}
	if loc.Get("state") != "state12345678" {
		t.Fatalf("want original state echoed back, got %q", loc.Get("state"))
	}

	// The code should actually redeem for a token (end-to-end sanity,
	// detailed behavior covered in oauth_token_test.go).
	status, tok := exchangeCode(t, baseURL, loc.Get("code"), testRedirectURI, clientID, verifier)
	if status != http.StatusOK {
		t.Fatalf("want token exchange to succeed, got %d: %v", status, tok)
	}
	if tok["access_token"] == nil {
		t.Fatalf("want an access_token, got %v", tok)
	}
}

func TestOAuthAuthorize_DenyingConsentReturnsErrorNoCode(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")
	_, challenge := pkcePair(t)

	client := newClient(t)
	signUp(t, client, baseURL, "denyuser@example.com", "correcthorse")

	q := authorizeParams(clientID, testRedirectURI, "profile", "state12345678", challenge)

	// AUTHZ-04
	resp := approveConsent(t, client, baseURL, q, []string{"profile"}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303, got %d", resp.StatusCode)
	}
	loc := locationQuery(t, resp)
	if loc.Get("code") != "" {
		t.Fatalf("want no code on denial, got %q", loc.Get("code"))
	}
	if loc.Get("error") == "" {
		t.Fatalf("want an error parameter on denial, got %v", loc)
	}
}

func TestOAuthAuthorize_ReturningUserSkipsConsent(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")

	client := newClient(t)
	signUp(t, client, baseURL, "returning@example.com", "correcthorse")

	_, challenge1 := pkcePair(t)
	q1 := authorizeParams(clientID, testRedirectURI, "profile", "firststate12345", challenge1)
	first := approveConsent(t, client, baseURL, q1, []string{"profile"}, true)
	if first.StatusCode != http.StatusSeeOther || locationQuery(t, first).Get("code") == "" {
		t.Fatalf("setup: first authorization should succeed")
	}

	// AUTHZ-05: same client, same scope, second time around -> straight
	// to a redirect with a fresh code, no consent screen.
	_, challenge2 := pkcePair(t)
	q2 := authorizeParams(clientID, testRedirectURI, "profile", "secondstate12345", challenge2)
	resp, err := client.Get(baseURL + "/oauth/authorize?" + q2.Encode())
	if err != nil {
		t.Fatalf("GET /oauth/authorize: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 (straight through, no consent screen), got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	loc := locationQuery(t, resp)
	if loc.Get("code") == "" {
		t.Fatalf("want a fresh code, got %v", loc)
	}
}

func TestOAuthAuthorize_RequiresPKCE(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")

	client := newClient(t)
	signUp(t, client, baseURL, "pkce-required@example.com", "correcthorse")

	// AUTHZ-06: no code_challenge at all. Fosite validates PKCE presence
	// only when a code is about to be issued (i.e. after consent is
	// approved), not at the initial GET — so the consent screen is
	// expected to render here; what must never happen is a code coming
	// out the other end.
	q := authorizeParams(clientID, testRedirectURI, "profile", "state12345678", "")
	q.Del("code_challenge")
	q.Del("code_challenge_method")

	resp := approveConsent(t, client, baseURL, q, []string{"profile"}, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want a redirect (possibly carrying an error), got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	loc := locationQuery(t, resp)
	if loc.Get("code") != "" {
		t.Fatalf("want no code issued without PKCE, got %v", loc)
	}
	if loc.Get("error") == "" {
		t.Fatalf("want an error explaining why, got %v", loc)
	}
}

func TestOAuthAuthorize_RejectsUnregisteredRedirectURI(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")
	_, challenge := pkcePair(t)

	client := newClient(t)
	signUp(t, client, baseURL, "redirectmismatch@example.com", "correcthorse")

	// AUTHZ-08: redirect_uri doesn't match what the client registered.
	// This must be rejected without ever redirecting anywhere — redirecting
	// to an attacker-supplied, unregistered URI is exactly the
	// open-redirect / code-theft vector this check defends against.
	q := authorizeParams(clientID, "https://evil.example/cb", "profile", "state12345678", challenge)
	resp, err := client.Get(baseURL + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatalf("GET /oauth/authorize: %v", err)
	}
	if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusFound {
		t.Fatalf("must never redirect for an unregistered redirect_uri, got %d to %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp.StatusCode < 400 {
		t.Fatalf("want an error response, got %d", resp.StatusCode)
	}
}

func TestOAuthAuthorize_RejectsWeakState(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")
	_, challenge := pkcePair(t)

	client := newClient(t)
	signUp(t, client, baseURL, "weakstate@example.com", "correcthorse")

	// AUTHZ-07: state under 8 characters.
	q := authorizeParams(clientID, testRedirectURI, "profile", "short", challenge)
	resp, err := client.Get(baseURL + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatalf("GET /oauth/authorize: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want a redirect carrying the error, got %d", resp.StatusCode)
	}
	loc := locationQuery(t, resp)
	if loc.Get("error") == "" {
		t.Fatalf("want an error for a too-short state, got %v", loc)
	}
}
