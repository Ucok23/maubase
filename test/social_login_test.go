package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"maubase/internal/social"
	"maubase/internal/testserver"
)

// Scenarios: spec/social-login.md

// fakeProviderServer stands in for Google/GitHub: /token always succeeds
// with a fake access token, and /userinfo (plus, for GitHub-shaped
// tests, /emails) return the given JSON verbatim. A real request never
// leaves the test process — social.NewGoogle/NewGitHub take every
// endpoint URL explicitly for exactly this.
func fakeProviderServer(t *testing.T, userInfoJSON, emailsJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access-token", "token_type": "Bearer", "expires_in": 3600,
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(userInfoJSON))
	})
	if emailsJSON != "" {
		mux.HandleFunc("/emails", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(emailsJSON))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// The RedirectURL given here is never actually validated by
// fakeProviderServer (unlike a real provider, which checks it against
// what was registered) — a placeholder is fine since maubase's actual
// callback route is fixed by chi's router regardless of what this
// string says.
func fakeGoogleProvider(t *testing.T, providerUserID, email string) social.Provider {
	t.Helper()
	srv := fakeProviderServer(t, fmt.Sprintf(`{"sub":%q,"email":%q}`, providerUserID, email), "")
	return social.NewGoogle("test-client-id", "test-client-secret", "http://placeholder.invalid/callback",
		srv.URL+"/authorize", srv.URL+"/token", srv.URL+"/userinfo")
}

func fakeGitHubProvider(t *testing.T, providerUserID int64, publicEmail, primaryVerifiedEmail string) social.Provider {
	t.Helper()
	userJSON := fmt.Sprintf(`{"id":%d,"email":%q}`, providerUserID, publicEmail)
	emailsJSON := fmt.Sprintf(`[{"email":%q,"primary":true,"verified":true}]`, primaryVerifiedEmail)
	srv := fakeProviderServer(t, userJSON, emailsJSON)
	return social.NewGitHub("test-client-id", "test-client-secret", "http://placeholder.invalid/callback",
		srv.URL+"/authorize", srv.URL+"/token", srv.URL+"/userinfo", srv.URL+"/emails")
}

// startSocialLogin drives GET /api/auth/social/{provider} and returns the
// state handleSocialStart put both in the redirect and in a cookie
// (already stored in client's jar by the time this returns).
func startSocialLogin(t *testing.T, client *http.Client, baseURL, provider string) string {
	t.Helper()
	resp := doGetNoRedirect(t, client, baseURL+"/api/auth/social/"+provider)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /api/auth/social/%s: want 303, got %d: %s", provider, resp.StatusCode, bodyString(t, resp))
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatalf("want a state param in the redirect, got: %s", loc)
	}
	return state
}

func socialCallback(t *testing.T, client *http.Client, baseURL, provider, code, state string) *http.Response {
	t.Helper()
	u := fmt.Sprintf("%s/api/auth/social/%s/callback?code=%s&state=%s", baseURL, provider, url.QueryEscape(code), url.QueryEscape(state))
	return doGetNoRedirect(t, client, u)
}

func meEmail(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()
	resp, err := client.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/auth/me: want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	me := decodeJSONMap(t, resp)
	email, _ := me["email"].(string)
	return email
}

func TestSocialLogin_NewIdentityCreatesAccountAndSignsIn(t *testing.T) {
	// SOCIAL-01
	provider := fakeGoogleProvider(t, "google-uid-1", "social01@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{
		SocialProviders:     map[string]social.Provider{"google": provider},
		SocialLoginRedirect: "https://app.example.com/welcome",
	})
	client := newClient(t)

	state := startSocialLogin(t, client, baseURL, "google")
	resp := socialCallback(t, client, baseURL, "google", "fake-code", state)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "https://app.example.com/welcome" {
		t.Fatalf("want redirect to SocialLoginRedirect, got %q", loc)
	}
	if email := meEmail(t, client, baseURL); email != "social01@example.com" {
		t.Fatalf("want the signed-in account's email to be social01@example.com, got %q", email)
	}
}

func TestSocialLogin_MatchingEmailLinksToExistingAccountInsteadOfDuplicating(t *testing.T) {
	// SOCIAL-02
	provider := fakeGoogleProvider(t, "google-uid-2", "social02@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{SocialProviders: map[string]social.Provider{"google": provider}})

	// An existing password account with the same email the provider will
	// report.
	passwordClient := newClient(t)
	signUp(t, passwordClient, baseURL, "social02@example.com", "originalpassword1")
	existingMe := decodeJSONMap(t, mustGet(t, passwordClient, baseURL+"/api/auth/me"))
	existingID, _ := existingMe["id"].(string)

	socialClient := newClient(t)
	state := startSocialLogin(t, socialClient, baseURL, "google")
	socialCallback(t, socialClient, baseURL, "google", "fake-code", state)

	socialMe := decodeJSONMap(t, mustGet(t, socialClient, baseURL+"/api/auth/me"))
	if socialMe["id"] != existingID {
		t.Fatalf("want the social sign-in to land on the existing account (id %v), got a different one: %v", existingID, socialMe)
	}
}

func TestSocialLogin_MatchingEmailLinksCaseInsensitively(t *testing.T) {
	// SOCIAL-08 / spec/identity.md IDNT-14
	provider := fakeGoogleProvider(t, "google-uid-caseinsensitive", "differently.cased@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{SocialProviders: map[string]social.Provider{"google": provider}})

	// The password account signed up with different casing than what the
	// provider will report.
	passwordClient := newClient(t)
	signUp(t, passwordClient, baseURL, "Differently.Cased@Example.com", "originalpassword1")
	existingMe := decodeJSONMap(t, mustGet(t, passwordClient, baseURL+"/api/auth/me"))
	existingID, _ := existingMe["id"].(string)

	socialClient := newClient(t)
	state := startSocialLogin(t, socialClient, baseURL, "google")
	socialCallback(t, socialClient, baseURL, "google", "fake-code", state)

	socialMe := decodeJSONMap(t, mustGet(t, socialClient, baseURL+"/api/auth/me"))
	if socialMe["id"] != existingID {
		t.Fatalf("want the social sign-in to link to the existing account (id %v) despite the casing difference, got a different one: %v", existingID, socialMe)
	}
}

func TestSocialLogin_ReturningIdentitySignsIntoTheSameAccountAgain(t *testing.T) {
	// SOCIAL-03
	provider := fakeGoogleProvider(t, "google-uid-3", "social03@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{SocialProviders: map[string]social.Provider{"google": provider}})

	first := newClient(t)
	state1 := startSocialLogin(t, first, baseURL, "google")
	socialCallback(t, first, baseURL, "google", "fake-code-1", state1)
	firstMe := decodeJSONMap(t, mustGet(t, first, baseURL+"/api/auth/me"))
	firstID, _ := firstMe["id"].(string)

	second := newClient(t)
	state2 := startSocialLogin(t, second, baseURL, "google")
	socialCallback(t, second, baseURL, "google", "fake-code-2", state2)
	secondMe := decodeJSONMap(t, mustGet(t, second, baseURL+"/api/auth/me"))

	if secondMe["id"] != firstID {
		t.Fatalf("want the same provider identity to sign into the same account both times, got %v then %v", firstID, secondMe["id"])
	}
}

func TestSocialLogin_StateMismatchRejected(t *testing.T) {
	// SOCIAL-04
	provider := fakeGoogleProvider(t, "google-uid-4", "social04@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{SocialProviders: map[string]social.Provider{"google": provider}})
	client := newClient(t)

	startSocialLogin(t, client, baseURL, "google") // sets the real state cookie, discarded here
	resp := socialCallback(t, client, baseURL, "google", "fake-code", "not-the-real-state")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback with a mismatched state: want 400, got %d", resp.StatusCode)
	}

	// No session was created.
	me, err := client.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/auth/me after a rejected callback: want 401 (no session), got %d", me.StatusCode)
	}
}

func TestSocialLogin_UnconfiguredOrUnknownProvider404s(t *testing.T) {
	// SOCIAL-05
	provider := fakeGoogleProvider(t, "google-uid-5", "social05@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{SocialProviders: map[string]social.Provider{"google": provider}})
	client := newClient(t)

	for _, name := range []string{"github", "not-a-real-provider"} {
		resp := doGetNoRedirect(t, client, baseURL+"/api/auth/social/"+name)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET /api/auth/social/%s: want 404, got %d", name, resp.StatusCode)
		}
	}
}

func TestSocialLogin_GitHubFallsBackToPrimaryVerifiedEmail(t *testing.T) {
	// SOCIAL-06
	provider := fakeGitHubProvider(t, 4242, "", "primary-verified@example.com") // "" — no public email on the main profile
	baseURL := testserver.NewCustom(t, testserver.Options{SocialProviders: map[string]social.Provider{"github": provider}})
	client := newClient(t)

	state := startSocialLogin(t, client, baseURL, "github")
	resp := socialCallback(t, client, baseURL, "github", "fake-code", state)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	if email := meEmail(t, client, baseURL); email != "primary-verified@example.com" {
		t.Fatalf("want the account created with GitHub's primary verified email, got %q", email)
	}
}

// TestSocialLogin_GitHubZeroVerifiedEmailsGetsSyntheticAddress exercises
// SOCIAL-12: no email on the main profile (SOCIAL-06's case) *and* no
// primary+verified entry in /user/emails either — the account still
// gets created, with createUserForSocialSignIn's synthetic
// "provider-id@users.noreply.provider.invalid" placeholder locked in
// exactly, since a future refactor changing this format without
// updating spec/social-login.md SOCIAL-12 would be exactly the kind of
// silent regression worth catching here.
func TestSocialLogin_GitHubZeroVerifiedEmailsGetsSyntheticAddress(t *testing.T) {
	// SOCIAL-12
	srv := fakeProviderServer(t, `{"id":777,"email":""}`, `[{"email":"unverified@example.com","primary":true,"verified":false}]`)
	provider := social.NewGitHub("test-client-id", "test-client-secret", "http://placeholder.invalid/callback",
		srv.URL+"/authorize", srv.URL+"/token", srv.URL+"/userinfo", srv.URL+"/emails")
	baseURL := testserver.NewCustom(t, testserver.Options{SocialProviders: map[string]social.Provider{"github": provider}})
	client := newClient(t)

	state := startSocialLogin(t, client, baseURL, "github")
	resp := socialCallback(t, client, baseURL, "github", "fake-code", state)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	if email := meEmail(t, client, baseURL); email != "github-777@users.noreply.github.invalid" {
		t.Fatalf("want the synthetic github-{id}@users.noreply.github.invalid address, got %q", email)
	}
}

func TestSocialLogin_StartIsRateLimited(t *testing.T) {
	// SOCIAL-07
	provider := fakeGoogleProvider(t, "google-uid-ratelimit", "social07@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{
		SocialProviders: map[string]social.Provider{"google": provider},
		LoginRateLimit:  2, LoginRateWindow: 60 * time.Second,
	})
	client := newClient(t)

	for i := 0; i < 2; i++ {
		resp := doGetNoRedirect(t, client, baseURL+"/api/auth/social/google")
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("attempt %d: want 303 (redirect to provider), got %d", i+1, resp.StatusCode)
		}
	}
	resp := doGetNoRedirect(t, client, baseURL+"/api/auth/social/google")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt over the limit: want 429, got %d", resp.StatusCode)
	}
}

func TestSocialLogin_LinksNewIdentityToCurrentSessionWhenSignedIn(t *testing.T) {
	// SOCIAL-09
	provider := fakeGoogleProvider(t, "google-uid-link-current", "wont-be-used-for-matching@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{SocialProviders: map[string]social.Provider{"google": provider}})

	client := newClient(t)
	signUp(t, client, baseURL, "account-a@example.com", "originalpassword1")
	meBefore := decodeJSONMap(t, mustGet(t, client, baseURL+"/api/auth/me"))
	accountAID, _ := meBefore["id"].(string)

	state := startSocialLogin(t, client, baseURL, "google")
	resp := socialCallback(t, client, baseURL, "google", "fake-code", state)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback while signed in: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	meAfter := decodeJSONMap(t, mustGet(t, client, baseURL+"/api/auth/me"))
	if meAfter["id"] != accountAID {
		t.Fatalf("want the same session/account after linking, got %v (was %v)", meAfter["id"], accountAID)
	}

	// Signed out, the same identity now signs back into account A
	// directly — the second sign-in method is real, not just accepted
	// and discarded.
	returning := newClient(t)
	state2 := startSocialLogin(t, returning, baseURL, "google")
	socialCallback(t, returning, baseURL, "google", "fake-code-2", state2)
	returningMe := decodeJSONMap(t, mustGet(t, returning, baseURL+"/api/auth/me"))
	if returningMe["id"] != accountAID {
		t.Fatalf("want the linked identity to sign back into account A, got %v", returningMe["id"])
	}
}

func TestSocialLogin_RejectsIdentityAlreadyLinkedElsewhere(t *testing.T) {
	// SOCIAL-10
	provider := fakeGoogleProvider(t, "google-uid-linked-elsewhere", "account-b-social@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{SocialProviders: map[string]social.Provider{"google": provider}})

	// Link the identity to account B first, via a normal anonymous flow.
	bClient := newClient(t)
	stateB := startSocialLogin(t, bClient, baseURL, "google")
	socialCallback(t, bClient, baseURL, "google", "fake-code-b", stateB)
	bMe := decodeJSONMap(t, mustGet(t, bClient, baseURL+"/api/auth/me"))
	accountBID, _ := bMe["id"].(string)

	// Sign in as a completely different account A, then complete the
	// SAME identity's flow.
	aClient := newClient(t)
	signUp(t, aClient, baseURL, "account-a-elsewhere@example.com", "originalpassword1")
	aMeBefore := decodeJSONMap(t, mustGet(t, aClient, baseURL+"/api/auth/me"))
	accountAID, _ := aMeBefore["id"].(string)
	if accountAID == accountBID {
		t.Fatalf("setup: want two distinct accounts")
	}

	stateA := startSocialLogin(t, aClient, baseURL, "google")
	resp := socialCallback(t, aClient, baseURL, "google", "fake-code-a", stateA)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 for an identity already linked to a different account, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	// A's session must be completely unaffected — never silently
	// switched to B.
	aMeAfter := decodeJSONMap(t, mustGet(t, aClient, baseURL+"/api/auth/me"))
	if aMeAfter["id"] != accountAID {
		t.Fatalf("want A's session unchanged after a rejected link attempt, got %v (was %v)", aMeAfter["id"], accountAID)
	}
}

// TestSocialLogin_SecondProviderDoesNotBreakFirstsInFlightCallback
// exercises SOCIAL-11: the state cookie is namespaced per provider, so
// starting a second provider's flow can't clobber a first, still-open
// one's cookie — a plausible two-tab or change-your-mind sequence on a
// login screen offering more than one "Continue with X" button.
func TestSocialLogin_SecondProviderDoesNotBreakFirstsInFlightCallback(t *testing.T) {
	// SOCIAL-11
	googleProvider := fakeGoogleProvider(t, "google-uid-multi", "multi-google@example.com")
	githubProvider := fakeGitHubProvider(t, 555, "multi-github@example.com", "multi-github@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{
		SocialProviders: map[string]social.Provider{"google": googleProvider, "github": githubProvider},
	})
	client := newClient(t)

	// Start google, then — before finishing it — also start github. Both
	// state cookies must coexist in the same client's jar.
	stateGoogle := startSocialLogin(t, client, baseURL, "google")
	stateGithub := startSocialLogin(t, client, baseURL, "github")
	if stateGoogle == stateGithub {
		t.Fatalf("setup: want two distinct state values, got the same for both")
	}

	// The original google callback, completing after github's flow
	// started, still succeeds — its cookie was never touched by
	// starting a different provider's flow.
	googleResp := socialCallback(t, client, baseURL, "google", "fake-code-google", stateGoogle)
	if googleResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("google callback after github started: want 303, got %d: %s", googleResp.StatusCode, bodyString(t, googleResp))
	}
	if email := meEmail(t, client, baseURL); email != "multi-google@example.com" {
		t.Fatalf("want the google account signed in, got email %q", email)
	}

	// The github flow, completed afterward on a fresh (signed-out)
	// client using its own state, also still works independently.
	returning := newClient(t)
	stateGithub2 := startSocialLogin(t, returning, baseURL, "github")
	githubResp := socialCallback(t, returning, baseURL, "github", "fake-code-github", stateGithub2)
	if githubResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("github callback: want 303, got %d: %s", githubResp.StatusCode, bodyString(t, githubResp))
	}
}

// mustGet is a small convenience so tests above stay one line at each
// call site — GET a URL with client and fail the test on any transport
// error, without asserting on status (callers that care check it
// themselves).
func mustGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}
