package e2e_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"maubase/internal/testserver"
)

// Scenarios: spec/maintenance.md (MAINT-01..04)

func purgeSessions(t *testing.T, client *http.Client, baseURL string) *http.Response {
	t.Helper()
	resp, err := client.Post(baseURL+"/admin/maintenance/purge-sessions", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /admin/maintenance/purge-sessions: %v", err)
	}
	return resp
}

func TestMaintenance_PurgeSessionsRequiresAdminRole(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	admin := newClient(t)
	ownerLogin(t, admin, baseURL, bootstrapEmail, bootstrapPassword)

	// A viewer-role owner is below admin.
	viewerEmail, viewerPassword := "viewer@example.com", "viewerpassword1"
	resp := createOwner(t, admin, baseURL, viewerEmail, viewerPassword, "viewer")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create viewer owner: want 201, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	resp.Body.Close()

	viewer := newClient(t)
	ownerLogin(t, viewer, baseURL, viewerEmail, viewerPassword)

	// MAINT-01: below-admin role is rejected.
	viewerResp := purgeSessions(t, viewer, baseURL)
	viewerResp.Body.Close()
	if viewerResp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer purge: want 403, got %d", viewerResp.StatusCode)
	}

	// MAINT-01: no session at all is rejected.
	anonResp := purgeSessions(t, newClient(t), baseURL)
	anonResp.Body.Close()
	if anonResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous purge: want 401, got %d", anonResp.StatusCode)
	}

	// MAINT-01: bootstrapped account is role "owner", which outranks admin.
	adminResp := purgeSessions(t, admin, baseURL)
	if adminResp.StatusCode != http.StatusOK {
		t.Fatalf("owner purge: want 200, got %d: %s", adminResp.StatusCode, bodyString(t, adminResp))
	}
	adminResp.Body.Close()
}

func TestMaintenance_PurgeSessionsDoesNotTouchValidSessions(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	customer := newClient(t)
	signUp(t, customer, baseURL, "user@example.com", "correcthorse")

	admin := newClient(t)
	ownerLogin(t, admin, baseURL, bootstrapEmail, bootstrapPassword)

	// MAINT-02
	resp := purgeSessions(t, admin, baseURL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge: want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	body := decodeJSONMap(t, resp)
	if _, ok := body["sessions_purged"]; !ok {
		t.Fatalf("purge response missing sessions_purged: %v", body)
	}
	if _, ok := body["owner_sessions_purged"]; !ok {
		t.Fatalf("purge response missing owner_sessions_purged: %v", body)
	}

	me, err := customer.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	defer me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("customer session after purge: want 200, got %d", me.StatusCode)
	}
}

func TestMaintenance_PurgeSessionsIsAuditLogged(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	admin := newClient(t)
	ownerLogin(t, admin, baseURL, bootstrapEmail, bootstrapPassword)

	// MAINT-03
	resp := purgeSessions(t, admin, baseURL)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge: want 200, got %d", resp.StatusCode)
	}

	entries := auditLog(t, admin, baseURL)
	e := findAuditEntry(entries, "sessions_purged", "")
	if e == nil {
		t.Fatalf("want a sessions_purged entry in the audit log, got %v", entries)
	}
	if e["actor_email"] != bootstrapEmail {
		t.Fatalf("want the calling admin as actor, got %v", e)
	}
}

func TestMaintenance_LoginRateLimitRejectsExcessAttempts(t *testing.T) {
	// MAINT-04
	baseURL := testserver.NewWithLoginRateLimit(t, 3, time.Minute)

	client := newClient(t)
	signUp(t, client, baseURL, "user@example.com", "correcthorse")

	tryLogin := func(password string) *http.Response {
		return postJSON(t, client, baseURL+"/api/auth/login", map[string]string{
			"email": "user@example.com", "password": password,
		})
	}

	// The signup itself doesn't count against the login limiter (it's a
	// different endpoint), so all 3 allowed attempts are available here.
	for i := range 3 {
		resp := tryLogin("wrong-password")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401 (within limit), got %d", i+1, resp.StatusCode)
		}
	}

	resp := tryLogin("wrong-password")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt beyond limit: want 429, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatalf("want a Retry-After header on 429, got none")
	}

	// Even correct credentials are rejected once the limit is exhausted —
	// the throttle counts every attempt, not just failures.
	blockedCorrect := tryLogin("correcthorse")
	blockedCorrect.Body.Close()
	if blockedCorrect.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("correct-password attempt beyond limit: want 429, got %d", blockedCorrect.StatusCode)
	}
}

func TestMaintenance_OwnerLoginRateLimitRejectsExcessAttempts(t *testing.T) {
	// MAINT-04, owner plane
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail:    bootstrapEmail,
		BootstrapOwnerPassword: bootstrapPassword,
		LoginRateLimit:         3,
		LoginRateWindow:        time.Minute,
	})

	client := newClient(t)
	for i := range 3 {
		resp := ownerLogin(t, client, baseURL, bootstrapEmail, "wrong-password")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401 (within limit), got %d", i+1, resp.StatusCode)
		}
	}

	resp := ownerLogin(t, client, baseURL, bootstrapEmail, "wrong-password")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt beyond limit: want 429, got %d", resp.StatusCode)
	}
}

// TestMaintenance_AdminUILoginRateLimitRejectsExcessAttempts: the human-
// facing admin login page (POST /admin/ui/login) used to be a complete
// bypass of MAUBASE_LOGIN_RATE_LIMIT — only its JSON twin,
// POST /admin/auth/login (tested above), was ever throttled.
func TestMaintenance_AdminUILoginRateLimitRejectsExcessAttempts(t *testing.T) {
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail:    bootstrapEmail,
		BootstrapOwnerPassword: bootstrapPassword,
		LoginRateLimit:         3,
		LoginRateWindow:        time.Minute,
	})

	client := newClient(t)
	for i := range 3 {
		resp := adminUILogin(t, client, baseURL, bootstrapEmail, "wrong-password")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d: want 200 (re-rendered login form, within limit), got %d", i+1, resp.StatusCode)
		}
	}

	resp := adminUILogin(t, client, baseURL, bootstrapEmail, "wrong-password")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt beyond limit: want 429, got %d", resp.StatusCode)
	}
}

// TestMaintenance_OAuthAuthorizeLoginRateLimitRejectsExcessAttempts: the
// login form embedded in GET/POST /oauth/authorize calls the exact same
// auth.Service.Login as POST /api/auth/login, but used to have none of
// that endpoint's throttling — a complete bypass via registering a
// throwaway (unauthenticated, trivial) OAuth client and guessing
// passwords through the consent-flow login form instead.
func TestMaintenance_OAuthAuthorizeLoginRateLimitRejectsExcessAttempts(t *testing.T) {
	baseURL := testserver.NewCustom(t, testserver.Options{LoginRateLimit: 3, LoginRateWindow: time.Minute})
	signUp(t, newClient(t), baseURL, "authorize-ratelimit@example.com", "correcthorse")

	clientID := registerPublicClient(t, baseURL, testRedirectURI, "records:read")
	_, challenge := pkcePair(t)
	authorizeQuery := authorizeParams(clientID, testRedirectURI, "records:read", "somestate12345678", challenge)

	client := newClient(t)
	tryLogin := func() *http.Response {
		form := url.Values{}
		form.Set("step", "login")
		form.Set("oauth_request", authorizeQuery.Encode())
		form.Set("email", "authorize-ratelimit@example.com")
		form.Set("password", "wrong-password")
		resp, err := client.PostForm(baseURL+"/oauth/authorize", form)
		if err != nil {
			t.Fatalf("POST /oauth/authorize (login step): %v", err)
		}
		return resp
	}

	for i := range 3 {
		resp := tryLogin()
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d: want 200 (re-rendered login form, within limit), got %d", i+1, resp.StatusCode)
		}
	}

	resp := tryLogin()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt beyond limit: want 429, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
}
