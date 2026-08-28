package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
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

func TestOwner_EmailMatchingIsCaseInsensitive(t *testing.T) {
	// OWNR-20
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	first := createOwner(t, owner, baseURL, "Admin@Example.com", "correcthorse", "admin")
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("setup: want 201, got %d: %s", first.StatusCode, bodyString(t, first))
	}

	dupe := createOwner(t, owner, baseURL, "admin@example.com", "correcthorse", "admin")
	if dupe.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 for a differently-cased duplicate email, got %d: %s", dupe.StatusCode, bodyString(t, dupe))
	}

	login := ownerLogin(t, newClient(t), baseURL, "ADMIN@EXAMPLE.COM", "correcthorse")
	if login.StatusCode != http.StatusOK {
		t.Fatalf("want login with a different casing to succeed, got %d: %s", login.StatusCode, bodyString(t, login))
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

// TestOwner_AdminCannotCreateAccountsEither exercises OWNR-22:
// TestOwner_OnlyOwnerCanCreateAccounts (OWNR-06) only ever checked
// owner (allow) against developer (deny) — the widest gap in the role
// hierarchy, and the least likely place an off-by-one hides. admin is
// the rung immediately adjacent to owner, and the one most likely to
// have been accidentally let through by a `role.AtLeast(...)` typo.
func TestOwner_AdminCannotCreateAccountsEither(t *testing.T) {
	// OWNR-22
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "ownr22-admin@example.com", "correcthorse", "admin")

	admin := newClient(t)
	if resp := ownerLogin(t, admin, baseURL, "ownr22-admin@example.com", "correcthorse"); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: admin login failed: %d", resp.StatusCode)
	}

	denied := createOwner(t, admin, baseURL, "ownr22-blocked@example.com", "correcthorse", "viewer")
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for an admin-role account creating owners, got %d: %s", denied.StatusCode, bodyString(t, denied))
	}
}

// TestOwner_DeveloperCannotListAccountsEither exercises the "GET
// /admin/owners" half of OWNR-23: TestOwner_ListingRequiresAtLeastAdmin
// (OWNR-07) only ever checked viewer, the rung furthest from the
// admin+ boundary. developer sits directly adjacent to it — the
// scenario a boundary-off-by-one would actually let through.
func TestOwner_DeveloperCannotListAccountsEither(t *testing.T) {
	// OWNR-23
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "ownr23-dev@example.com", "correcthorse", "developer")

	dev := newClient(t)
	if resp := ownerLogin(t, dev, baseURL, "ownr23-dev@example.com", "correcthorse"); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: developer login failed: %d", resp.StatusCode)
	}

	denied, err := dev.Get(baseURL + "/admin/owners")
	if err != nil {
		t.Fatalf("GET /admin/owners: %v", err)
	}
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for a developer-role account listing owners, got %d: %s", denied.StatusCode, bodyString(t, denied))
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

// TestOwner_ConcurrentDeleteOfBothOwnersLeavesOneStanding is OWNR-18:
// given exactly two owner-role accounts, concurrent deletes of BOTH must
// not both succeed — a deployment must always retain at least one owner.
// Before the fix, DeleteOwner's owner-count check and its DELETE ran as
// two separate, unguarded queries, so two concurrent calls could each
// observe ownerCount == 2, both pass the "> 1" guard, and both proceed —
// zero owners, an unrecoverable deployment. Fires both deletes in
// parallel (not sequentially, unlike TestOwner_LastOwnerCannotBeDeleted
// above) to actually exercise that race.
func TestOwner_ConcurrentDeleteOfBothOwnersLeavesOneStanding(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	founder := newClient(t)
	ownerLogin(t, founder, baseURL, bootstrapEmail, bootstrapPassword)

	me := decodeJSONMap(t, mustGet(t, founder, baseURL+"/admin/auth/me"))
	firstID, _ := me["id"].(string)
	if firstID == "" {
		t.Fatalf("setup: couldn't get own id: %v", me)
	}

	const secondEmail, secondPassword = "coowner-race@example.com", "correcthorse"
	secondResp := createOwner(t, founder, baseURL, secondEmail, secondPassword, "owner")
	if secondResp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: creating a second owner failed: %d", secondResp.StatusCode)
	}
	secondID, _ := decodeJSONMap(t, secondResp)["id"].(string)
	if secondID == "" {
		t.Fatalf("setup: couldn't get second owner's id")
	}

	// Each owner deletes ITSELF using its OWN session — not each other,
	// and not both via one shared session. Cross-deleting (or reusing
	// one session for both calls) would confound the test: deleting an
	// owner cascades to that owner's own sessions, so whichever request
	// commits first would invalidate the session authenticating the
	// OTHER still-in-flight request, turning it into an unrelated 401
	// rather than the 409 this test actually wants to observe. A
	// self-delete's own authentication can never be invalidated by the
	// OTHER request, since the other request only ever touches the
	// other account.
	secondClient := newClient(t)
	if resp := ownerLogin(t, secondClient, baseURL, secondEmail, secondPassword); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: second owner login failed: %d", resp.StatusCode)
	}

	type result struct {
		ownerID string
		client  *http.Client
		status  int
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, pair := range []struct {
		id     string
		client *http.Client
	}{{firstID, founder}, {secondID, secondClient}} {
		wg.Add(1)
		go func(ownerID string, client *http.Client) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodDelete, baseURL+"/admin/owners/"+ownerID, nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("DELETE /admin/owners/%s: %v", ownerID, err)
				return
			}
			results <- result{ownerID: ownerID, client: client, status: resp.StatusCode}
		}(pair.id, pair.client)
	}
	wg.Wait()
	close(results)

	var successes, conflicts int
	var survivor *http.Client
	for r := range results {
		switch r.status {
		case http.StatusNoContent:
			successes++
		case http.StatusConflict:
			conflicts++
			survivor = r.client // rejected as "last owner" — this one is still here
		default:
			t.Fatalf("concurrent self-delete of %s: want 204 or 409, got %d", r.ownerID, r.status)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("want exactly one self-delete to succeed and one to be rejected as the last owner, got %d successes and %d conflicts", successes, conflicts)
	}

	// Verified via the survivor's own still-valid session — if the race
	// had actually gone the buggy way (both succeeding), there would be
	// no valid owner session left at all to check this with, which is
	// itself exactly the failure this test exists to catch.
	remaining := decodeJSONList(t, mustGet(t, survivor, baseURL+"/admin/owners"))
	ownerCount := 0
	for _, o := range remaining {
		if role, _ := o["role"].(string); role == "owner" {
			ownerCount++
		}
	}
	if ownerCount != 1 {
		t.Fatalf("want exactly one owner-role account left standing, got %d", ownerCount)
	}
}

func TestOwner_DeletedOwnerSessionStopsAuthenticatingImmediately(t *testing.T) {
	// OWNR-19
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	founder := newClient(t)
	ownerLogin(t, founder, baseURL, bootstrapEmail, bootstrapPassword)

	const targetEmail, targetPassword = "soon-deleted@example.com", "correcthorse"
	createResp := createOwner(t, founder, baseURL, targetEmail, targetPassword, "admin")
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: creating the target owner failed: %d", createResp.StatusCode)
	}
	targetID, _ := decodeJSONMap(t, createResp)["id"].(string)
	if targetID == "" {
		t.Fatalf("setup: couldn't get target owner's id")
	}

	target := newClient(t)
	if resp := ownerLogin(t, target, baseURL, targetEmail, targetPassword); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: target owner login failed: %d", resp.StatusCode)
	}

	// Confirm the session works before deletion, so the test proves
	// deletion is what killed it.
	before := mustGet(t, target, baseURL+"/admin/auth/me")
	if before.StatusCode != http.StatusOK {
		t.Fatalf("setup: target's session should work before deletion, got %d", before.StatusCode)
	}

	req, err := http.NewRequest(http.MethodDelete, baseURL+"/admin/owners/"+targetID, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	delResp, err := founder.Do(req)
	if err != nil {
		t.Fatalf("DELETE /admin/owners/%s: %v", targetID, err)
	}
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete target owner: want 204, got %d: %s", delResp.StatusCode, bodyString(t, delResp))
	}

	after := mustGet(t, target, baseURL+"/admin/auth/me")
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want the deleted owner's session rejected immediately, got %d", after.StatusCode)
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

// TestOwner_ExpiredSessionRejected exercises OWNR-24: maintenance.md's
// intro claims "Expired sessions already fail authentication on their
// own," but no OWNR-xx scenario ever actually stated or tested this for
// the owner plane specifically — the only expiry-adjacent test
// (TestMaintenance_PurgeSessionsDoesNotTouchValidSessions,
// spec/maintenance.md) checks a *customer* session stays valid post-purge,
// never the owner-plane *rejection* path for a session that's past
// expiry but not yet purged. With SQL Studio available, this is directly
// testable: backdate the session's own expires_at, then confirm it's
// rejected.
func TestOwner_ExpiredSessionRejected(t *testing.T) {
	// OWNR-24
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	sqlResp, err := owner.PostForm(baseURL+"/admin/ui/sql", url.Values{
		"query": {"UPDATE owner_sessions SET expires_at = '2000-01-01 00:00:00'"},
	})
	if err != nil {
		t.Fatalf("backdate session via SQL Studio: %v", err)
	}
	if sqlResp.StatusCode != http.StatusOK {
		t.Fatalf("SQL Studio update: want 200, got %d: %s", sqlResp.StatusCode, bodyString(t, sqlResp))
	}

	me, err := owner.Get(baseURL + "/admin/auth/me")
	if err != nil {
		t.Fatalf("GET /admin/auth/me: %v", err)
	}
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for an expired owner session, got %d: %s", me.StatusCode, bodyString(t, me))
	}
}

// TestOwner_CreatingWithInvalidRoleIsRejected exercises OWNR-26:
// Role.IsValid()/ErrInvalidRole exist specifically for this, and
// writeOwnerAuthError already maps ErrInvalidRole to 400, but nothing
// ever actually called CreateOwner with a bad role to prove it.
func TestOwner_CreatingWithInvalidRoleIsRejected(t *testing.T) {
	// OWNR-26
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	for _, role := range []string{"superadmin", ""} {
		resp := createOwner(t, owner, baseURL, "ownr26-"+role+"@example.com", "correcthorse", role)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("role %q: want 400, got %d: %s", role, resp.StatusCode, bodyString(t, resp))
		}
	}

	// Neither attempt created an account: the list still shows only the
	// bootstrapped owner.
	list, err := owner.Get(baseURL + "/admin/owners")
	if err != nil {
		t.Fatalf("GET /admin/owners: %v", err)
	}
	var out []map[string]any
	if err := json.NewDecoder(list.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	list.Body.Close()
	if len(out) != 1 {
		t.Fatalf("want no account created by either invalid-role attempt, got %d accounts: %v", len(out), out)
	}
}
