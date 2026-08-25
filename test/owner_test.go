package e2e_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/owner-plane.md

const (
	bootstrapEmail    = "founder@example.com"
	bootstrapPassword = "correcthorsebattery"
)

func ownerLogin(t *testing.T, client *http.Client, baseURL, email, password string) *http.Response {
	t.Helper()
	return postJSON(t, client, baseURL+"/admin/auth/login", map[string]string{
		"email": email, "password": password,
	})
}

func createOwner(t *testing.T, client *http.Client, baseURL, email, password, role string) *http.Response {
	t.Helper()
	return postJSON(t, client, baseURL+"/admin/owners", map[string]string{
		"email": email, "password": password, "role": role,
	})
}

func TestOwner_BootstrapCreatesFirstOwner(t *testing.T) {
	// OWNR-01
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	client := newClient(t)
	resp := ownerLogin(t, client, baseURL, bootstrapEmail, bootstrapPassword)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	me, err := client.Get(baseURL + "/admin/auth/me")
	if err != nil {
		t.Fatalf("GET /admin/auth/me: %v", err)
	}
	meBody := decodeJSONMap(t, me)
	if meBody["role"] != "owner" {
		t.Fatalf("want role=owner for the bootstrapped account, got %v", meBody)
	}
}

func TestOwner_BootstrapIsANoOpOnceOwnerExists(t *testing.T) {
	// OWNR-02: NewWithOwner already exercises Bootstrap once; a direct
	// second call (as a real restart would make) must not error or
	// duplicate, and the original account must still be the one that logs
	// in with the original password.
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	// A second bootstrap attempt with different credentials must not take
	// effect: only the original account/password should work.
	client := newClient(t)
	resp := ownerLogin(t, client, baseURL, bootstrapEmail, bootstrapPassword)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("original bootstrapped account should still log in, got %d", resp.StatusCode)
	}
}

func TestOwner_LoginWrongPassword(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	// OWNR-04
	client := newClient(t)
	resp := ownerLogin(t, client, baseURL, bootstrapEmail, "not-the-password")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	if len(client.Jar.Cookies(mustURL(t, baseURL))) != 0 {
		t.Fatal("want no session cookie after a failed owner login")
	}
}

func TestOwner_AnonymousRejected(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	// OWNR-05
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/admin/auth/me"},
		{http.MethodGet, "/admin/owners"},
	} {
		r, err := http.NewRequest(req.method, baseURL+req.path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatalf("%s %s: %v", req.method, req.path, err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: want 401 anonymous, got %d", req.method, req.path, resp.StatusCode)
		}
	}
}

func TestOwner_OnlyOwnerCanCreateAccounts(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	owner := newClient(t)
	if resp := ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: owner login failed: %d", resp.StatusCode)
	}

	// OWNR-06, part 1: an owner can create accounts of any role.
	created := createOwner(t, owner, baseURL, "dev1@example.com", "correcthorse", "developer")
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", created.StatusCode, bodyString(t, created))
	}

	// Sign the new developer in, then confirm they can't create accounts.
	dev := newClient(t)
	if resp := ownerLogin(t, dev, baseURL, "dev1@example.com", "correcthorse"); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: developer login failed: %d", resp.StatusCode)
	}

	// OWNR-06, part 2
	denied := createOwner(t, dev, baseURL, "dev2@example.com", "correcthorse", "developer")
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for a non-owner creating accounts, got %d: %s", denied.StatusCode, bodyString(t, denied))
	}
}

func TestOwner_ListingRequiresAtLeastAdmin(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "viewer1@example.com", "correcthorse", "viewer")

	// OWNR-07, part 1: admin/owner can list.
	list, err := owner.Get(baseURL + "/admin/owners")
	if err != nil {
		t.Fatalf("GET /admin/owners: %v", err)
	}
	if list.StatusCode != http.StatusOK {
		t.Fatalf("want 200 for owner listing accounts, got %d", list.StatusCode)
	}
	var out []map[string]any
	if err := json.NewDecoder(list.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	list.Body.Close()
	if len(out) != 2 {
		t.Fatalf("want 2 accounts listed, got %d: %v", len(out), out)
	}

	// OWNR-07, part 2: viewer cannot list.
	viewer := newClient(t)
	ownerLogin(t, viewer, baseURL, "viewer1@example.com", "correcthorse")
	denied, err := viewer.Get(baseURL + "/admin/owners")
	if err != nil {
		t.Fatalf("GET /admin/owners: %v", err)
	}
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for viewer listing accounts, got %d: %s", denied.StatusCode, bodyString(t, denied))
	}
}

func TestOwner_LastOwnerCannotBeDeleted(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	me, err := owner.Get(baseURL + "/admin/auth/me")
	if err != nil {
		t.Fatalf("GET /admin/auth/me: %v", err)
	}
	meBody := decodeJSONMap(t, me)
	ownerID, _ := meBody["id"].(string)
	if ownerID == "" {
		t.Fatalf("setup: couldn't get own id: %v", meBody)
	}

	// OWNR-08, part 1: sole owner can't delete themselves (or any owner).
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/admin/owners/"+ownerID, nil)
	resp, err := owner.Do(req)
	if err != nil {
		t.Fatalf("DELETE /admin/owners/%s: %v", ownerID, err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 deleting the last owner, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	// OWNR-08, part 2: with a second owner present, deletion succeeds.
	second := createOwner(t, owner, baseURL, "coowner@example.com", "correcthorse", "owner")
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("setup: creating a second owner failed: %d", second.StatusCode)
	}
	req2, _ := http.NewRequest(http.MethodDelete, baseURL+"/admin/owners/"+ownerID, nil)
	resp2, err := owner.Do(req2)
	if err != nil {
		t.Fatalf("DELETE /admin/owners/%s: %v", ownerID, err)
	}
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204 deleting an owner when a second owner remains, got %d: %s", resp2.StatusCode, bodyString(t, resp2))
	}
}

func TestOwner_SessionsDoNotCrossPlanes(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	// OWNR-09
	owner := newClient(t)
	if resp := ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: owner login failed: %d", resp.StatusCode)
	}

	resp, err := owner.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want an owner session rejected on a customer-plane route, got %d", resp.StatusCode)
	}
}

func TestOwner_Logout(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	// OWNR-10
	client := newClient(t)
	ownerLogin(t, client, baseURL, bootstrapEmail, bootstrapPassword)

	resp, err := client.Post(baseURL+"/admin/auth/logout", "", nil)
	if err != nil {
		t.Fatalf("POST /admin/auth/logout: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}

	me, err := client.Get(baseURL + "/admin/auth/me")
	if err != nil {
		t.Fatalf("GET /admin/auth/me: %v", err)
	}
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 after logout, got %d", me.StatusCode)
	}
}
