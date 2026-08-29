package e2e_test

import (
	"net/http"
	"testing"

	"maubase/internal/email"
	"maubase/internal/social"
	"maubase/internal/testserver"
)

// Scenarios: spec/cross-cutting.md AUDIT-CUST-01

func TestCustomerAudit_SignupRecorded(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	signUp(t, newClient(t), baseURL, "audit-signup@example.com", "correcthorse")

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	entries := auditLog(t, owner, baseURL)
	e := findAuditEntry(entries, "customer_signup", "audit-signup@example.com")
	if e == nil {
		t.Fatalf("want a customer_signup entry, got %v", entries)
	}
	if e["actor_email"] != "audit-signup@example.com" || e["target_email"] != "audit-signup@example.com" {
		t.Fatalf("want the new account as both actor and target, got %v", e)
	}
}

func TestCustomerAudit_LoginRecordedForSuccessAndFailure(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	signUp(t, newClient(t), baseURL, "audit-login@example.com", "correcthorse")

	// A fresh login, separate from the signup's own auto-login.
	postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "audit-login@example.com", "password": "correcthorse",
	})
	// Wrong password.
	postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "audit-login@example.com", "password": "wrongpassword",
	})
	// An email that doesn't correspond to any account at all.
	postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "no-such-audit-account@example.com", "password": "whatever1",
	})

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	entries := auditLog(t, owner, baseURL)

	loginEntry := findAuditEntry(entries, "customer_login", "")
	found := false
	for _, e := range entries {
		if e["event"] == "customer_login" && e["actor_email"] == "audit-login@example.com" {
			found = true
			break
		}
	}
	if loginEntry == nil || !found {
		t.Fatalf("want a customer_login entry for the successful login, got %v", entries)
	}

	wrongPwEntry := findAuditEntry(entries, "customer_login_failed", "audit-login@example.com")
	if wrongPwEntry == nil {
		t.Fatalf("want a customer_login_failed entry for the wrong-password attempt, got %v", entries)
	}
	unknownEntry := findAuditEntry(entries, "customer_login_failed", "no-such-audit-account@example.com")
	if unknownEntry == nil {
		t.Fatalf("want a customer_login_failed entry recorded even for an unknown email, got %v", entries)
	}
}

func TestCustomerAudit_LogoutRecorded(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	client := newClient(t)
	signUp(t, client, baseURL, "audit-logout@example.com", "correcthorse")

	resp := postJSON(t, client, baseURL+"/api/auth/logout", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: want 204, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	entries := auditLog(t, owner, baseURL)
	e := findAuditEntry(entries, "customer_logout", "")
	if e == nil || e["actor_email"] != "audit-logout@example.com" {
		t.Fatalf("want a customer_logout entry attributing the signed-in account, got %v", entries)
	}
}

func TestCustomerAudit_ForgotPasswordRecordedOnlyForRealAccount(t *testing.T) {
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		EmailSender: sender,
	})
	signUp(t, newClient(t), baseURL, "audit-forgot@example.com", "correcthorse")

	realResp := forgotPassword(t, newClient(t), baseURL, "audit-forgot@example.com")
	if realResp.StatusCode != http.StatusNoContent {
		t.Fatalf("forgot-password (real account): want 204, got %d", realResp.StatusCode)
	}
	fakeResp := forgotPassword(t, newClient(t), baseURL, "no-such-audit-forgot@example.com")
	if fakeResp.StatusCode != http.StatusNoContent {
		t.Fatalf("forgot-password (unknown account): want 204, got %d", fakeResp.StatusCode)
	}

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	entries := auditLog(t, owner, baseURL)

	real := findAuditEntry(entries, "customer_password_reset_requested", "audit-forgot@example.com")
	if real == nil {
		t.Fatalf("want a customer_password_reset_requested entry for the real account, got %v", entries)
	}
	fake := findAuditEntry(entries, "customer_password_reset_requested", "no-such-audit-forgot@example.com")
	if fake != nil {
		t.Fatalf("want no entry for an email with no account (PWRESET-02's anti-enumeration posture), got %v", fake)
	}
}

func TestCustomerAudit_ResetPasswordCompletedRecorded(t *testing.T) {
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		EmailSender: sender,
	})
	signUp(t, newClient(t), baseURL, "audit-reset@example.com", "correcthorse")
	forgotPassword(t, newClient(t), baseURL, "audit-reset@example.com")
	token := tokenFromResetLink(t, sender.Sent())

	resetResp := resetPassword(t, baseURL, token, "brandnewpassword1")
	if resetResp.StatusCode != http.StatusNoContent {
		t.Fatalf("reset-password: want 204, got %d: %s", resetResp.StatusCode, bodyString(t, resetResp))
	}

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	entries := auditLog(t, owner, baseURL)
	e := findAuditEntry(entries, "customer_password_reset_completed", "")
	if e == nil {
		t.Fatalf("want a customer_password_reset_completed entry, got %v", entries)
	}
	if e["actor_id"] == "" || e["actor_id"] != e["target_id"] {
		t.Fatalf("want actor and target to be the same account, got %v", e)
	}
}

func TestCustomerAudit_AccountDeletedRecorded(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	client := newClient(t)
	signUp(t, client, baseURL, "audit-delete@example.com", "correcthorse")

	del, err := client.Do(mustRequest(t, http.MethodDelete, baseURL+"/api/auth/me"))
	if err != nil {
		t.Fatalf("DELETE /api/auth/me: %v", err)
	}
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete account: want 204, got %d: %s", del.StatusCode, bodyString(t, del))
	}

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	entries := auditLog(t, owner, baseURL)
	e := findAuditEntry(entries, "customer_account_deleted", "audit-delete@example.com")
	if e == nil {
		t.Fatalf("want a customer_account_deleted entry naming the now-deleted account, got %v", entries)
	}
}

func TestCustomerAudit_SocialSignInRecordsNewAccountAndLinking(t *testing.T) {
	provider := fakeGoogleProvider(t, "google-uid-audit-1", "audit-social-new@example.com")
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		SocialProviders: map[string]social.Provider{"google": provider},
	})

	// A brand-new account, created anonymously via the social flow.
	anon := newClient(t)
	state := startSocialLogin(t, anon, baseURL, "google")
	socialCallback(t, anon, baseURL, "google", "fake-code", state)

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	entries := auditLog(t, owner, baseURL)
	newAcctEntry := findAuditEntry(entries, "customer_social_sign_in", "")
	if newAcctEntry == nil {
		t.Fatalf("want a customer_social_sign_in entry, got %v", entries)
	}
	meta, _ := newAcctEntry["metadata"].(map[string]any)
	if meta["provider"] != "google" || meta["new_account"] != true || meta["already_signed_in"] != false {
		t.Fatalf("want metadata to reflect a new, anonymous social signup, got %v", meta)
	}

	// A second provider identity, linked to an already-signed-in account
	// (SOCIAL-09's flow) — not a new account, and already signed in.
	linkProvider := fakeGoogleProvider(t, "google-uid-audit-2", "wont-be-used-for-matching@example.com")
	baseURL2 := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		SocialProviders: map[string]social.Provider{"google": linkProvider},
	})
	customer := newClient(t)
	signUp(t, customer, baseURL2, "audit-social-link@example.com", "correcthorse")
	state2 := startSocialLogin(t, customer, baseURL2, "google")
	socialCallback(t, customer, baseURL2, "google", "fake-code", state2)

	owner2 := newClient(t)
	ownerLogin(t, owner2, baseURL2, bootstrapEmail, bootstrapPassword)
	entries2 := auditLog(t, owner2, baseURL2)
	linkEntry := findAuditEntry(entries2, "customer_social_sign_in", "")
	if linkEntry == nil {
		t.Fatalf("want a customer_social_sign_in entry for the linking flow, got %v", entries2)
	}
	linkMeta, _ := linkEntry["metadata"].(map[string]any)
	if linkMeta["new_account"] != false || linkMeta["already_signed_in"] != true {
		t.Fatalf("want metadata to reflect a link-to-current-session, not a new account, got %v", linkMeta)
	}
}
