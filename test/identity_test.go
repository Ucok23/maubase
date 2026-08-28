package e2e_test

import (
	"net/http"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/identity.md

func TestIdentity_SignUp(t *testing.T) {
	baseURL := testserver.New(t)
	client := newClient(t)

	// IDNT-01: signing up creates the account and signs the user in.
	resp := postJSON(t, client, baseURL+"/api/auth/signup", map[string]string{
		"email": "new@example.com", "password": "correcthorse",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	body := decodeJSONMap(t, resp)
	user, _ := body["user"].(map[string]any)
	if user["email"] != "new@example.com" {
		t.Fatalf("want email in response, got %v", body)
	}
	if len(client.Jar.Cookies(mustURL(t, baseURL))) == 0 {
		t.Fatal("want a session cookie set after signup")
	}

	// IDNT-02: signing up again with the same email is rejected.
	dupe := postJSON(t, client, baseURL+"/api/auth/signup", map[string]string{
		"email": "new@example.com", "password": "anotherpassword",
	})
	if dupe.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 for duplicate email, got %d: %s", dupe.StatusCode, bodyString(t, dupe))
	}
}

func TestIdentity_EmailMatchingIsCaseInsensitive(t *testing.T) {
	// IDNT-14
	baseURL := testserver.New(t)
	client := newClient(t)

	resp := postJSON(t, client, baseURL+"/api/auth/signup", map[string]string{
		"email": "Jane.Doe@Example.com", "password": "correcthorse",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: want 201, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	// Signing up again with a different casing of the same address is a
	// duplicate, not a second account.
	dupe := postJSON(t, newClient(t), baseURL+"/api/auth/signup", map[string]string{
		"email": "jane.doe@example.com", "password": "anotherpassword",
	})
	if dupe.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 for a differently-cased duplicate email, got %d: %s", dupe.StatusCode, bodyString(t, dupe))
	}

	// Logging in with a different casing than what was typed at signup
	// still works.
	login := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "JANE.DOE@EXAMPLE.COM", "password": "correcthorse",
	})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("want login with a different casing to succeed, got %d: %s", login.StatusCode, bodyString(t, login))
	}
}

func TestIdentity_SignUpWeakPassword(t *testing.T) {
	baseURL := testserver.New(t)
	client := newClient(t)

	// IDNT-03
	resp := postJSON(t, client, baseURL+"/api/auth/signup", map[string]string{
		"email": "weak@example.com", "password": "short",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for weak password, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	// The rejected signup must not have created an account: a later
	// signup with the same email should succeed, not 409.
	retry := postJSON(t, client, baseURL+"/api/auth/signup", map[string]string{
		"email": "weak@example.com", "password": "longenoughpassword",
	})
	if retry.StatusCode != http.StatusCreated {
		t.Fatalf("want signup to succeed after a rejected weak-password attempt, got %d", retry.StatusCode)
	}
}

func TestIdentity_LoginAndMe(t *testing.T) {
	baseURL := testserver.New(t)
	setup := newClient(t)
	signUp(t, setup, baseURL, "loginuser@example.com", "correcthorse")

	// IDNT-04: correct credentials sign in on a fresh client.
	client := newClient(t)
	resp := postJSON(t, client, baseURL+"/api/auth/login", map[string]string{
		"email": "loginuser@example.com", "password": "correcthorse",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	body := decodeJSONMap(t, resp)
	if body["expires_at"] == nil {
		t.Fatalf("want expires_at in login response, got %v", body)
	}

	// IDNT-06: the signed-in session can fetch its own identity.
	me, err := client.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if me.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", me.StatusCode)
	}
	meBody := decodeJSONMap(t, me)
	if meBody["email"] != "loginuser@example.com" {
		t.Fatalf("want own email back, got %v", meBody)
	}
}

func TestIdentity_LoginWrongPassword(t *testing.T) {
	baseURL := testserver.New(t)
	setup := newClient(t)
	signUp(t, setup, baseURL, "wrongpass@example.com", "correcthorse")

	// IDNT-05
	client := newClient(t)
	resp := postJSON(t, client, baseURL+"/api/auth/login", map[string]string{
		"email": "wrongpass@example.com", "password": "not-the-password",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	if len(client.Jar.Cookies(mustURL(t, baseURL))) != 0 {
		t.Fatal("want no session cookie set after a failed login")
	}
}

func TestIdentity_MeRequiresAuth(t *testing.T) {
	baseURL := testserver.New(t)
	client := newClient(t) // never signed in

	// IDNT-07
	resp, err := client.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for anonymous request, got %d", resp.StatusCode)
	}
}

func TestIdentity_Logout(t *testing.T) {
	baseURL := testserver.New(t)
	client := newClient(t)
	signUp(t, client, baseURL, "logout@example.com", "correcthorse")

	// IDNT-08
	resp, err := client.Post(baseURL+"/api/auth/logout", "", nil)
	if err != nil {
		t.Fatalf("POST /api/auth/logout: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}

	me, err := client.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 after logout, got %d", me.StatusCode)
	}
}
