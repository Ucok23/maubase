package e2e_test

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ucok23/maubase/internal/email"
	"github.com/Ucok23/maubase/internal/testserver"
)

// Scenarios: spec/password-reset.md

func forgotPassword(t *testing.T, client *http.Client, baseURL, email string) *http.Response {
	t.Helper()
	resp := postJSON(t, client, baseURL+"/api/auth/forgot-password", map[string]string{"email": email})
	return resp
}

func resetPassword(t *testing.T, baseURL, token, password string) *http.Response {
	t.Helper()
	resp := postJSON(t, newClient(t), baseURL+"/api/auth/reset-password", map[string]string{
		"token": token, "password": password,
	})
	return resp
}

// tokenFromResetLink pulls the ?token= value out of the reset link the
// fake sender captured, failing the test if it can't find one.
func tokenFromResetLink(t *testing.T, sent []email.SentEmail) string {
	t.Helper()
	if len(sent) == 0 {
		t.Fatalf("want at least one email sent, got none")
	}
	html := sent[len(sent)-1].HTML
	idx := strings.Index(html, "token=")
	if idx < 0 {
		t.Fatalf("want a token= query param in the emailed link, got: %s", html)
	}
	rest := html[idx+len("token="):]
	end := strings.IndexAny(rest, `"'&`)
	if end < 0 {
		t.Fatalf("malformed token in emailed link: %s", html)
	}
	token, err := url.QueryUnescape(rest[:end])
	if err != nil {
		t.Fatalf("unescape token: %v", err)
	}
	return token
}

func TestPasswordReset_RealAccountGetsAWorkingLink(t *testing.T) {
	// PWRESET-01
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{EmailSender: sender, PasswordResetURL: "https://app.example.com/reset"})
	client := newClient(t)
	signUp(t, client, baseURL, "pwreset01@example.com", "originalpassword1")

	resp := forgotPassword(t, newClient(t), baseURL, "pwreset01@example.com")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forgot-password: want 204, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	sent := sender.Sent()
	if len(sent) != 1 {
		t.Fatalf("want exactly 1 email sent, got %d", len(sent))
	}
	if sent[0].To != "pwreset01@example.com" {
		t.Fatalf("want the email sent to pwreset01@example.com, got %q", sent[0].To)
	}
	if !strings.Contains(sent[0].HTML, "https://app.example.com/reset?token=") {
		t.Fatalf("want the link built from PasswordResetURL, got: %s", sent[0].HTML)
	}

	token := tokenFromResetLink(t, sent)
	reset := resetPassword(t, baseURL, token, "brandnewpassword1")
	if reset.StatusCode != http.StatusNoContent {
		t.Fatalf("reset-password with the emailed token: want 204, got %d: %s", reset.StatusCode, bodyString(t, reset))
	}
}

func TestPasswordReset_UnknownEmailLooksIdentical(t *testing.T) {
	// PWRESET-02
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{EmailSender: sender})

	resp := forgotPassword(t, newClient(t), baseURL, "no-such-account@example.com")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forgot-password for an unknown email: want 204 (same as a real one), got %d", resp.StatusCode)
	}
	if len(sender.Sent()) != 0 {
		t.Fatalf("want no email sent for an unregistered address, got %d", len(sender.Sent()))
	}
}

// TestPasswordReset_UnconfiguredEmailSenderStillLooksIdentical is
// PWRESET-02 under the actual shipped default: no MAUBASE_RESEND_API_KEY
// configured means email.NoopSender, whose Send always errors by design.
// Before this test's fix, a real account then got 500 (Send's error was
// surfaced) while an unregistered one still got 204 (Send was never
// called) — a fresh, not-yet-configured deployment leaked its user list
// through response codes alone, the exact enumeration PWRESET-02 exists
// to prevent.
func TestPasswordReset_UnconfiguredEmailSenderStillLooksIdentical(t *testing.T) {
	baseURL := testserver.NewCustom(t, testserver.Options{EmailSender: email.NoopSender{}})
	signUp(t, newClient(t), baseURL, "pwreset-noop@example.com", "originalpassword1")

	real := forgotPassword(t, newClient(t), baseURL, "pwreset-noop@example.com")
	if real.StatusCode != http.StatusNoContent {
		t.Fatalf("forgot-password for a real account with no email sender configured: want 204, got %d: %s", real.StatusCode, bodyString(t, real))
	}

	fake := forgotPassword(t, newClient(t), baseURL, "no-such-account@example.com")
	if fake.StatusCode != http.StatusNoContent {
		t.Fatalf("forgot-password for an unknown email: want 204, got %d", fake.StatusCode)
	}
}

func TestPasswordReset_ValidTokenChangesPasswordAndRevokesAllSessions(t *testing.T) {
	// PWRESET-03
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{EmailSender: sender})

	sessionA := newClient(t)
	signUp(t, sessionA, baseURL, "pwreset03@example.com", "originalpassword1")
	sessionB := newClient(t)
	if resp := postJSON(t, sessionB, baseURL+"/api/auth/login", map[string]string{
		"email": "pwreset03@example.com", "password": "originalpassword1",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("second login: want 200, got %d", resp.StatusCode)
	}

	forgotPassword(t, newClient(t), baseURL, "pwreset03@example.com")
	token := tokenFromResetLink(t, sender.Sent())

	if resp := resetPassword(t, baseURL, token, "brandnewpassword1"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reset-password: want 204, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	// Old password no longer works; new one does.
	oldLogin := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "pwreset03@example.com", "password": "originalpassword1",
	})
	if oldLogin.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login with old password: want 401, got %d", oldLogin.StatusCode)
	}
	newLogin := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "pwreset03@example.com", "password": "brandnewpassword1",
	})
	if newLogin.StatusCode != http.StatusOK {
		t.Fatalf("login with new password: want 200, got %d", newLogin.StatusCode)
	}

	// Both pre-existing sessions are dead, including the one that
	// requested the reset.
	for name, c := range map[string]*http.Client{"sessionA (requested the reset)": sessionA, "sessionB": sessionB} {
		me, err := c.Get(baseURL + "/api/auth/me")
		if err != nil {
			t.Fatalf("%s GET /api/auth/me: %v", name, err)
		}
		if me.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s after reset: want 401, got %d", name, me.StatusCode)
		}
	}
}

func TestPasswordReset_WeakPasswordRejected(t *testing.T) {
	// PWRESET-04
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{EmailSender: sender})
	signUp(t, newClient(t), baseURL, "pwreset04@example.com", "originalpassword1")

	forgotPassword(t, newClient(t), baseURL, "pwreset04@example.com")
	token := tokenFromResetLink(t, sender.Sent())

	resp := resetPassword(t, baseURL, token, "short")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reset with a weak password: want 400, got %d", resp.StatusCode)
	}

	// The old password still works — nothing changed.
	login := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "pwreset04@example.com", "password": "originalpassword1",
	})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login with the original password after a rejected reset: want 200, got %d", login.StatusCode)
	}
}

func TestPasswordReset_TokenRedeemableOnlyOnce(t *testing.T) {
	// PWRESET-05
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{EmailSender: sender})
	signUp(t, newClient(t), baseURL, "pwreset05@example.com", "originalpassword1")

	forgotPassword(t, newClient(t), baseURL, "pwreset05@example.com")
	token := tokenFromResetLink(t, sender.Sent())

	first := resetPassword(t, baseURL, token, "brandnewpassword1")
	if first.StatusCode != http.StatusNoContent {
		t.Fatalf("first redemption: want 204, got %d", first.StatusCode)
	}
	replay := resetPassword(t, baseURL, token, "yetanotherpassword1")
	if replay.StatusCode != http.StatusBadRequest {
		t.Fatalf("replaying the same token: want 400, got %d", replay.StatusCode)
	}

	// Still the first reset's password, not the replay's.
	login := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "pwreset05@example.com", "password": "brandnewpassword1",
	})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login with the first reset's password: want 200, got %d", login.StatusCode)
	}
}

// TestPasswordReset_ExpiredTokenRejected exercises PWRESET-06, which had
// no test at all: there's no way to just wait out the real one-hour TTL
// in a test, so this backdates the token's own expires_at via SQL Studio
// (owner-plane, direct DB access) instead.
func TestPasswordReset_ExpiredTokenRejected(t *testing.T) {
	// PWRESET-06
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		EmailSender: sender,
	})
	signUp(t, newClient(t), baseURL, "pwreset06@example.com", "originalpassword1")

	forgotPassword(t, newClient(t), baseURL, "pwreset06@example.com")
	token := tokenFromResetLink(t, sender.Sent())

	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	sqlResp, err := owner.PostForm(baseURL+"/admin/ui/sql", url.Values{
		"query": {"UPDATE password_reset_tokens SET expires_at = '2000-01-01 00:00:00'"},
	})
	if err != nil {
		t.Fatalf("backdate token via SQL Studio: %v", err)
	}
	if sqlResp.StatusCode != http.StatusOK {
		t.Fatalf("SQL Studio update: want 200, got %d: %s", sqlResp.StatusCode, bodyString(t, sqlResp))
	}

	reset := resetPassword(t, baseURL, token, "wontworkanyway1")
	if reset.StatusCode != http.StatusBadRequest {
		t.Fatalf("expired token: want 400, got %d: %s", reset.StatusCode, bodyString(t, reset))
	}

	// The password is genuinely unchanged.
	login := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "pwreset06@example.com", "password": "originalpassword1",
	})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login with the original password: want 200, got %d", login.StatusCode)
	}
}

// TestPasswordReset_ConcurrentRedemptionOnlyOneWins is PWRESET-10: two
// concurrent redemptions of the same still-valid token must not both
// succeed. Before the fix, ResetPassword's initial SELECT ran outside
// its transaction, so both concurrent calls could read used_at IS NULL
// before either committed — this fires the two requests in parallel
// (not sequentially, unlike TestPasswordReset_TokenRedeemableOnlyOnce
// above) to actually exercise that race, many times, to make a
// still-possible-but-rare race very unlikely to hide a regression.
func TestPasswordReset_ConcurrentRedemptionOnlyOneWins(t *testing.T) {
	// PWRESET-10
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{EmailSender: sender})
	signUp(t, newClient(t), baseURL, "pwreset10@example.com", "originalpassword1")

	forgotPassword(t, newClient(t), baseURL, "pwreset10@example.com")
	token := tokenFromResetLink(t, sender.Sent())

	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, pw := range []string{"concurrentpassword1", "concurrentpassword2"} {
		wg.Add(1)
		go func(password string) {
			defer wg.Done()
			resp := resetPassword(t, baseURL, token, password)
			results <- resp.StatusCode
		}(pw)
	}
	wg.Wait()
	close(results)

	var successes, failures int
	for status := range results {
		switch status {
		case http.StatusNoContent:
			successes++
		case http.StatusBadRequest:
			failures++
		default:
			t.Fatalf("concurrent redemption: want 204 or 400, got %d", status)
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("want exactly one redemption to succeed and one to fail, got %d successes and %d failures", successes, failures)
	}

	// Exactly one of the two passwords works — never both, never neither.
	workingCount := 0
	for _, pw := range []string{"concurrentpassword1", "concurrentpassword2"} {
		login := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
			"email": "pwreset10@example.com", "password": pw,
		})
		if login.StatusCode == http.StatusOK {
			workingCount++
		}
	}
	if workingCount != 1 {
		t.Fatalf("want exactly one of the two concurrently-submitted passwords to work, got %d", workingCount)
	}
}

// TestPasswordReset_RevokesOAuthGrantsToo is PWRESET-09: resetting a
// password used to revoke only identity-layer sessions, leaving any
// OAuth access token issued to a third-party client while one of those
// sessions was active fully live and usable — a real gap in the "signed
// out everywhere" guarantee PWRESET-03 already makes for sessions,
// mirroring the same RevokedTokenRejected pattern
// TestOAuthResource_RevokedTokenRejected uses for explicit /oauth/revoke.
func TestPasswordReset_RevokesOAuthGrantsToo(t *testing.T) {
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{EmailSender: sender})
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile")

	client := newClient(t)
	signUp(t, client, baseURL, "pwreset09@example.com", "originalpassword1")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"profile"})
	accessToken, _ := tok["access_token"].(string)

	before := mustAuthedGet(t, baseURL+"/api/oauth/whoami", accessToken)
	if before.StatusCode != http.StatusOK {
		t.Fatalf("setup: token should work before the reset, got %d", before.StatusCode)
	}

	forgotPassword(t, newClient(t), baseURL, "pwreset09@example.com")
	token := tokenFromResetLink(t, sender.Sent())
	reset := resetPassword(t, baseURL, token, "brandnewpassword1")
	if reset.StatusCode != http.StatusNoContent {
		t.Fatalf("reset-password: want 204, got %d: %s", reset.StatusCode, bodyString(t, reset))
	}

	after := mustAuthedGet(t, baseURL+"/api/oauth/whoami", accessToken)
	if after.StatusCode == http.StatusOK {
		t.Fatalf("want the OAuth access token rejected after a password reset, got 200: %s", bodyString(t, after))
	}
}

func TestPasswordReset_GarbageTokenRejected(t *testing.T) {
	// PWRESET-07
	baseURL := testserver.New(t)
	resp := resetPassword(t, baseURL, "this-token-was-never-issued", "brandnewpassword1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage token: want 400, got %d", resp.StatusCode)
	}
}

func TestPasswordReset_ForgotPasswordIsRateLimited(t *testing.T) {
	// PWRESET-08
	baseURL := testserver.NewCustom(t, testserver.Options{
		EmailSender: email.NewFakeSender(), LoginRateLimit: 2, LoginRateWindow: 60 * time.Second,
	})
	client := newClient(t)
	for i := 0; i < 2; i++ {
		resp := forgotPassword(t, client, baseURL, "whoever@example.com")
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("attempt %d: want 204, got %d", i+1, resp.StatusCode)
		}
	}
	resp := forgotPassword(t, client, baseURL, "whoever@example.com")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt over the limit: want 429, got %d", resp.StatusCode)
	}
}

func TestPasswordReset_ResetPasswordIsRateLimited(t *testing.T) {
	// PWRESET-11
	baseURL := testserver.NewCustom(t, testserver.Options{
		LoginRateLimit: 2, LoginRateWindow: 60 * time.Second,
	})
	for i := 0; i < 2; i++ {
		resp := resetPassword(t, baseURL, "garbage-token", "brandnewpassword1")
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("attempt %d: want 400 (garbage token, but under the limit), got %d", i+1, resp.StatusCode)
		}
	}
	resp := resetPassword(t, baseURL, "garbage-token", "brandnewpassword1")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt over the limit: want 429, got %d", resp.StatusCode)
	}
}

func TestPasswordReset_RedeemingOneTokenInvalidatesSiblingTokens(t *testing.T) {
	// PWRESET-12
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{EmailSender: sender})
	signUp(t, newClient(t), baseURL, "pwreset12@example.com", "originalpassword1")

	// Two separate forgot-password requests, two separate outstanding
	// tokens.
	forgotPassword(t, newClient(t), baseURL, "pwreset12@example.com")
	forgotPassword(t, newClient(t), baseURL, "pwreset12@example.com")
	sent := sender.Sent()
	if len(sent) != 2 {
		t.Fatalf("setup: want 2 emails sent, got %d", len(sent))
	}
	firstToken := tokenFromResetLink(t, sent[:1])
	secondToken := tokenFromResetLink(t, sent[1:2])
	if firstToken == secondToken {
		t.Fatalf("setup: want two distinct tokens, got the same one twice")
	}

	// Redeem the first.
	redeemed := resetPassword(t, baseURL, firstToken, "brandnewpassword1")
	if redeemed.StatusCode != http.StatusNoContent {
		t.Fatalf("redeem first token: want 204, got %d: %s", redeemed.StatusCode, bodyString(t, redeemed))
	}

	// The second, never-redeemed, still-unexpired token must no longer
	// work — not just the one that was actually used.
	sibling := resetPassword(t, baseURL, secondToken, "anotherpassword1")
	if sibling.StatusCode != http.StatusBadRequest {
		t.Fatalf("redeem sibling token after the other was used: want 400, got %d: %s", sibling.StatusCode, bodyString(t, sibling))
	}

	// The password from the first (legitimate) redemption is the one
	// that's actually in effect.
	login := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "pwreset12@example.com", "password": "brandnewpassword1",
	})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login with the first token's new password: want 200, got %d", login.StatusCode)
	}
}
