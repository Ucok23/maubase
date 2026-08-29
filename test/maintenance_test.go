package e2e_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"maubase/internal/email"
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

func TestMaintenance_PurgeSessionsAlsoPurgesExpiredOrUsedResetTokens(t *testing.T) {
	// MAINT-07
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		EmailSender: sender,
	})
	admin := newClient(t)
	ownerLogin(t, admin, baseURL, bootstrapEmail, bootstrapPassword)

	// Still outstanding, unredeemed — must survive the purge.
	signUp(t, newClient(t), baseURL, "maint07-outstanding@example.com", "correcthorse")
	forgotPassword(t, newClient(t), baseURL, "maint07-outstanding@example.com")
	outstandingToken := tokenFromResetLink(t, sender.Sent())

	// Already redeemed — must be purged.
	signUp(t, newClient(t), baseURL, "maint07-used@example.com", "correcthorse")
	forgotPassword(t, newClient(t), baseURL, "maint07-used@example.com")
	usedToken := tokenFromResetLink(t, sender.Sent())
	if resp := resetPassword(t, baseURL, usedToken, "brandnewpassword1"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("setup: redeem token: want 204, got %d", resp.StatusCode)
	}

	// Already expired — must be purged.
	signUp(t, newClient(t), baseURL, "maint07-expired@example.com", "correcthorse")
	forgotPassword(t, newClient(t), baseURL, "maint07-expired@example.com")
	sqlResp, err := admin.PostForm(baseURL+"/admin/ui/sql", url.Values{
		"query": {"UPDATE password_reset_tokens SET expires_at = '2000-01-01 00:00:00' WHERE user_id = (SELECT id FROM users WHERE email = 'maint07-expired@example.com')"},
	})
	if err != nil {
		t.Fatalf("backdate token via SQL Studio: %v", err)
	}
	if sqlResp.StatusCode != http.StatusOK {
		t.Fatalf("SQL Studio update: want 200, got %d: %s", sqlResp.StatusCode, bodyString(t, sqlResp))
	}

	countBefore := sqlStudioCount(t, admin, baseURL, "SELECT COUNT(*) FROM password_reset_tokens")
	if countBefore != 3 {
		t.Fatalf("setup: want 3 reset token rows before purging, got %d", countBefore)
	}

	resp := purgeSessions(t, admin, baseURL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge: want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	body := decodeJSONMap(t, resp)
	purged, _ := body["reset_tokens_purged"].(float64)
	if purged != 2 {
		t.Fatalf("want reset_tokens_purged=2 (the used and expired ones), got %v: %v", body["reset_tokens_purged"], body)
	}

	countAfter := sqlStudioCount(t, admin, baseURL, "SELECT COUNT(*) FROM password_reset_tokens")
	if countAfter != 1 {
		t.Fatalf("want exactly 1 reset token row left after purging, got %d", countAfter)
	}

	entries := auditLog(t, admin, baseURL)
	e := findAuditEntry(entries, "sessions_purged", "")
	if e == nil {
		t.Fatalf("want a sessions_purged entry in the audit log, got %v", entries)
	}
	meta, _ := e["metadata"].(map[string]any)
	if resetTokens, _ := meta["reset_tokens"].(float64); resetTokens != 2 {
		t.Fatalf("want reset_tokens: 2 in the audit entry's metadata, got %v", meta)
	}

	// The still-outstanding token survived and still redeems normally.
	final := resetPassword(t, baseURL, outstandingToken, "stilloutstanding1")
	if final.StatusCode != http.StatusNoContent {
		t.Fatalf("outstanding token after purge: want 204, got %d: %s", final.StatusCode, bodyString(t, final))
	}
}

// sqlStudioCount runs a SELECT COUNT(*) ... query via SQL Studio and
// parses the single integer result out of the rendered results table.
func sqlStudioCount(t *testing.T, client *http.Client, baseURL, query string) int {
	t.Helper()
	resp, err := client.PostForm(baseURL+"/admin/ui/sql", url.Values{"query": {query}})
	if err != nil {
		t.Fatalf("SQL Studio query: %v", err)
	}
	body := bodyString(t, resp)
	// The results table renders one <td>...</td> holding the count.
	idx := strings.Index(body, "<td>")
	if idx < 0 {
		t.Fatalf("want a <td> result cell in SQL Studio output, got: %s", body)
	}
	rest := body[idx+len("<td>"):]
	end := strings.Index(rest, "</td>")
	if end < 0 {
		t.Fatalf("malformed result cell in SQL Studio output: %s", body)
	}
	var n int
	if _, err := fmt.Sscan(rest[:end], &n); err != nil {
		t.Fatalf("parse count %q: %v", rest[:end], err)
	}
	return n
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

func TestMaintenance_CustomerAndOwnerLoginRateLimitsAreIndependent(t *testing.T) {
	// MAINT-06
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail:    bootstrapEmail,
		BootstrapOwnerPassword: bootstrapPassword,
		LoginRateLimit:         2,
		LoginRateWindow:        time.Minute,
	})
	signUp(t, newClient(t), baseURL, "maint06@example.com", "correcthorse")

	customerLogin := func() *http.Response {
		return postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
			"email": "maint06@example.com", "password": "wrong-password",
		})
	}

	// Exhaust the customer-plane budget.
	for i := range 2 {
		resp := customerLogin()
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("customer attempt %d: want 401 (within limit), got %d", i+1, resp.StatusCode)
		}
	}
	exhausted := customerLogin()
	exhausted.Body.Close()
	if exhausted.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("customer attempt beyond limit: want 429, got %d", exhausted.StatusCode)
	}

	// The owner plane's own budget is still fresh — unaffected by the
	// customer-plane exhaustion above, even though every request here
	// comes from the same test process (same client IP).
	for i := range 2 {
		resp := ownerLogin(t, newClient(t), baseURL, bootstrapEmail, "wrong-password")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("owner attempt %d: want 401 (owner budget untouched by customer exhaustion), got %d", i+1, resp.StatusCode)
		}
	}

	// Exhausting the owner-plane budget in turn doesn't touch the
	// customer plane's (already-exhausted, but this proves independence
	// in both directions, not just one).
	ownerLogin(t, newClient(t), baseURL, bootstrapEmail, "wrong-password").Body.Close()
	stillExhausted := customerLogin()
	stillExhausted.Body.Close()
	if stillExhausted.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("customer plane: want still 429 (unaffected either way), got %d", stillExhausted.StatusCode)
	}
}

func TestMaintenance_RateLimitedOwnerLoginIsAuditLogged(t *testing.T) {
	// MAINT-05, JSON /admin/auth/login
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		LoginRateLimit: 2, LoginRateWindow: time.Minute,
	})
	admin := newClient(t)
	if resp := ownerLogin(t, admin, baseURL, bootstrapEmail, bootstrapPassword); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: owner login failed: %d", resp.StatusCode)
	}

	// That successful login already used 1 of the 2 allowed attempts; one
	// more exhausts it, and the next after that is rate-limited.
	ownerLogin(t, newClient(t), baseURL, bootstrapEmail, "wrong-password").Body.Close()
	limited := ownerLogin(t, newClient(t), baseURL, bootstrapEmail, "wrong-password")
	limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", limited.StatusCode)
	}

	// admin's own session (from the setup login, unaffected by the
	// exhaustion of an unrelated attempt count) can still read the log.
	entries := auditLog(t, admin, baseURL)
	if e := findAuditEntry(entries, "login_rate_limited", ""); e == nil {
		t.Fatalf("want a login_rate_limited entry in the audit log, got %v", entries)
	}
}

func TestMaintenance_RateLimitedAdminUILoginIsAuditLogged(t *testing.T) {
	// MAINT-05, HTML /admin/ui/login
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		LoginRateLimit: 2, LoginRateWindow: time.Minute,
	})
	// Via the JSON endpoint, an entirely separate limiter instance from
	// the HTML page's own — doesn't spend any of the budget under test.
	admin := newClient(t)
	if resp := ownerLogin(t, admin, baseURL, bootstrapEmail, bootstrapPassword); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: owner login failed: %d", resp.StatusCode)
	}

	for i := range 2 {
		resp := adminUILogin(t, newClient(t), baseURL, bootstrapEmail, "wrong-password")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d: want 200 (re-rendered form, within limit), got %d", i+1, resp.StatusCode)
		}
	}
	limited := adminUILogin(t, newClient(t), baseURL, bootstrapEmail, "wrong-password")
	limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", limited.StatusCode)
	}

	entries := auditLog(t, admin, baseURL)
	if e := findAuditEntry(entries, "login_rate_limited", ""); e == nil {
		t.Fatalf("want a login_rate_limited entry in the audit log, got %v", entries)
	}
}
