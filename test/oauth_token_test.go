package e2e_test

import (
	"net/http"
	"sync"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/oauth-token.md

func TestOAuthToken_ValidCodeAndPKCEYieldsToken(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")

	client := newClient(t)
	signUp(t, client, baseURL, "tok01@example.com", "correcthorse")

	// TOK-01
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"profile"})
	if tok["access_token"] == nil || tok["access_token"] == "" {
		t.Fatalf("want an access_token, got %v", tok)
	}
	if tok["expires_in"] == nil {
		t.Fatalf("want an expiry, got %v", tok)
	}
	if tok["scope"] != "profile" {
		t.Fatalf("want granted scope 'profile', got %v", tok["scope"])
	}
}

func TestOAuthToken_WrongVerifierRejected(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")

	client := newClient(t)
	signUp(t, client, baseURL, "tok02@example.com", "correcthorse")

	_, challenge := pkcePair(t)
	q := authorizeParams(clientID, testRedirectURI, "profile", "somestate12345678", challenge)
	resp := approveConsent(t, client, baseURL, q, []string{"profile"}, true)
	code := locationQuery(t, resp).Get("code")

	// TOK-02: a verifier that doesn't match the challenge used above.
	status, tok := exchangeCode(t, baseURL, code, testRedirectURI, clientID, "wrong-verifier-wrong-verifier-wrong")
	if status != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %v", status, tok)
	}
	if tok["error"] != "invalid_grant" {
		t.Fatalf("want error=invalid_grant, got %v", tok["error"])
	}
	if tok["access_token"] != nil {
		t.Fatalf("want no access_token issued, got %v", tok)
	}
}

func TestOAuthToken_CodeCannotBeReused(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")

	client := newClient(t)
	signUp(t, client, baseURL, "tok03@example.com", "correcthorse")

	verifier, challenge := pkcePair(t)
	q := authorizeParams(clientID, testRedirectURI, "profile", "somestate12345678", challenge)
	resp := approveConsent(t, client, baseURL, q, []string{"profile"}, true)
	code := locationQuery(t, resp).Get("code")

	firstStatus, first := exchangeCode(t, baseURL, code, testRedirectURI, clientID, verifier)
	if firstStatus != http.StatusOK {
		t.Fatalf("setup: first exchange should succeed, got %d: %v", firstStatus, first)
	}

	// TOK-03: replaying the same code a second time must not work.
	secondStatus, second := exchangeCode(t, baseURL, code, testRedirectURI, clientID, verifier)
	if secondStatus != http.StatusBadRequest {
		t.Fatalf("want 400 on code reuse, got %d: %v", secondStatus, second)
	}
	if second["access_token"] != nil {
		t.Fatalf("want no token from a replayed code, got %v", second)
	}
}

func TestOAuthToken_RefreshTokenOnlyWithOfflineAccess(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile offline_access")

	// TOK-04, part 1: offline_access granted -> refresh_token present.
	withOffline := newClient(t)
	signUp(t, withOffline, baseURL, "tok04a@example.com", "correcthorse")
	tokWith := authorizeAndGetToken(t, withOffline, baseURL, clientID, testRedirectURI, []string{"profile", "offline_access"})
	if tokWith["refresh_token"] == nil || tokWith["refresh_token"] == "" {
		t.Fatalf("want a refresh_token when offline_access was granted, got %v", tokWith)
	}

	// TOK-04, part 2: offline_access NOT granted -> no refresh_token.
	withoutOffline := newClient(t)
	signUp(t, withoutOffline, baseURL, "tok04b@example.com", "correcthorse")
	tokWithout := authorizeAndGetToken(t, withoutOffline, baseURL, clientID, testRedirectURI, []string{"profile"})
	if v, has := tokWithout["refresh_token"]; has && v != "" {
		t.Fatalf("want no refresh_token when offline_access was not granted, got %v", v)
	}
}

func TestOAuthToken_RefreshTokenRotates(t *testing.T) {
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile offline_access")

	client := newClient(t)
	signUp(t, client, baseURL, "tok05@example.com", "correcthorse")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"profile", "offline_access"})
	oldRefresh, _ := tok["refresh_token"].(string)
	if oldRefresh == "" {
		t.Fatalf("setup: want a refresh_token, got %v", tok)
	}

	// TOK-05: redeeming the refresh token yields new tokens...
	form := map[string][]string{
		"grant_type":    {"refresh_token"},
		"refresh_token": {oldRefresh},
		"client_id":     {clientID},
	}
	resp, err := http.PostForm(baseURL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("POST /oauth/token (refresh): %v", err)
	}
	refreshed := decodeJSONMap(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %v", resp.StatusCode, refreshed)
	}
	newRefresh, _ := refreshed["refresh_token"].(string)
	if newRefresh == "" || newRefresh == oldRefresh {
		t.Fatalf("want a new, different refresh_token, got %q (old was %q)", newRefresh, oldRefresh)
	}

	// ...and the old refresh token no longer works.
	replay, err := http.PostForm(baseURL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("POST /oauth/token (replay old refresh token): %v", err)
	}
	replayBody := decodeJSONMap(t, replay)
	if replay.StatusCode == http.StatusOK {
		t.Fatalf("want the rotated-out refresh token to be rejected, got 200: %v", replayBody)
	}
}

func TestOAuthToken_RefreshTokenReuseRevokesWholeChain(t *testing.T) {
	// TOK-07
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile offline_access")

	client := newClient(t)
	signUp(t, client, baseURL, "tok07@example.com", "correcthorse")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"profile", "offline_access"})
	oldRefresh, _ := tok["refresh_token"].(string)
	if oldRefresh == "" {
		t.Fatalf("setup: want a refresh_token, got %v", tok)
	}

	// Rotate once, legitimately, producing a fresh pair.
	form := func(refresh string) map[string][]string {
		return map[string][]string{
			"grant_type":    {"refresh_token"},
			"refresh_token": {refresh},
			"client_id":     {clientID},
		}
	}
	firstRotate, err := http.PostForm(baseURL+"/oauth/token", form(oldRefresh))
	if err != nil {
		t.Fatalf("POST /oauth/token (rotate): %v", err)
	}
	rotated := decodeJSONMap(t, firstRotate)
	if firstRotate.StatusCode != http.StatusOK {
		t.Fatalf("setup: rotation should succeed, got %d: %v", firstRotate.StatusCode, rotated)
	}
	freshAccess, _ := rotated["access_token"].(string)
	freshRefresh, _ := rotated["refresh_token"].(string)
	if freshAccess == "" || freshRefresh == "" {
		t.Fatalf("setup: want a fresh access+refresh token pair, got %v", rotated)
	}

	// Confirm the fresh access token actually works before the reuse
	// attack, so the test proves reuse detection is what breaks it.
	before := mustAuthedGet(t, baseURL+"/api/oauth/whoami", freshAccess)
	if before.StatusCode != http.StatusOK {
		t.Fatalf("setup: fresh access token should work before reuse is detected, got %d", before.StatusCode)
	}

	// Replay the OLD (already-rotated-away) refresh token — this is the
	// reuse signal: only an attacker holding a stale, stolen token (or a
	// client that lost track of rotation) would ever present it again.
	replay, err := http.PostForm(baseURL+"/oauth/token", form(oldRefresh))
	if err != nil {
		t.Fatalf("POST /oauth/token (replay old refresh token): %v", err)
	}
	if replay.StatusCode == http.StatusOK {
		t.Fatalf("want the reused refresh token rejected, got 200: %v", decodeJSONMap(t, replay))
	}

	// The whole chain downstream of the reused token must now be dead:
	// both the fresh access token and the fresh refresh token it minted.
	after := mustAuthedGet(t, baseURL+"/api/oauth/whoami", freshAccess)
	if after.StatusCode == http.StatusOK {
		t.Fatalf("want the fresh access token revoked by reuse detection, got 200: %s", bodyString(t, after))
	}
	secondRotate, err := http.PostForm(baseURL+"/oauth/token", form(freshRefresh))
	if err != nil {
		t.Fatalf("POST /oauth/token (attempt to use the fresh refresh token after reuse detection): %v", err)
	}
	if secondRotate.StatusCode == http.StatusOK {
		t.Fatalf("want the fresh refresh token revoked by reuse detection too, got 200: %v", decodeJSONMap(t, secondRotate))
	}
}

func TestOAuthToken_ConcurrentRefreshRedemptionOnlyOneWins(t *testing.T) {
	// TOK-08
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile offline_access")

	client := newClient(t)
	signUp(t, client, baseURL, "tok08@example.com", "correcthorse")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"profile", "offline_access"})
	refreshToken, _ := tok["refresh_token"].(string)
	if refreshToken == "" {
		t.Fatalf("setup: want a refresh_token, got %v", tok)
	}

	form := map[string][]string{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}

	var wg sync.WaitGroup
	statuses := make([]int, 2)
	bodies := make([]map[string]any, 2)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := http.PostForm(baseURL+"/oauth/token", form)
			if err != nil {
				t.Errorf("POST /oauth/token (concurrent refresh #%d): %v", i, err)
				return
			}
			statuses[i] = resp.StatusCode
			bodies[i] = decodeJSONMap(t, resp)
		}(i)
	}
	wg.Wait()

	successes, accessTokens := 0, map[string]bool{}
	for i, status := range statuses {
		if status == http.StatusOK {
			successes++
			at, _ := bodies[i]["access_token"].(string)
			accessTokens[at] = true
		}
	}
	if successes != 1 {
		t.Fatalf("want exactly 1 of 2 concurrent redemptions to succeed, got %d: statuses=%v bodies=%v", successes, statuses, bodies)
	}

	// The one access_token that was minted must actually work — proving
	// this isn't a case where both failed to register as a win.
	var liveToken string
	for at := range accessTokens {
		liveToken = at
	}
	check := mustAuthedGet(t, baseURL+"/api/oauth/whoami", liveToken)
	if check.StatusCode != http.StatusOK {
		t.Fatalf("want the single winning access_token to work, got %d", check.StatusCode)
	}
}
