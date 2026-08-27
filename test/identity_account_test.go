package e2e_test

import (
	"net/http"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/identity.md IDNT-09..12

// restTokenForClient registers a public client and drives an already
// signed-in client (its cookie jar holds the session from an earlier
// signUp) through consent, returning a records:read+write access token
// for that same user. Unlike restToken (test/restapi_test.go), the caller
// controls the client and its signed-in identity, so the same user can be
// addressed both via session cookie (customer-plane /api/auth/*) and via
// OAuth bearer token (/api/data/*) in one test.
func restTokenForClient(t *testing.T, baseURL string, client *http.Client) string {
	t.Helper()
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "records:read records:write")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"records:read", "records:write"})
	at, _ := tok["access_token"].(string)
	if at == "" {
		t.Fatalf("setup: no access_token in %v", tok)
	}
	return at
}

func TestIdentity_ExportAccount(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	client := newClient(t)
	signUp(t, client, baseURL, "export-me@example.com", "correcthorse")
	token := restTokenForClient(t, baseURL, client)

	doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "mine"})

	// IDNT-09
	resp, err := client.Get(baseURL + "/api/auth/me/export")
	if err != nil {
		t.Fatalf("GET /api/auth/me/export: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	body := decodeJSONMap(t, resp)

	profile, _ := body["profile"].(map[string]any)
	if profile["email"] != "export-me@example.com" {
		t.Fatalf("want own email in exported profile, got %v", profile)
	}

	records, _ := body["records"].(map[string]any)
	notes, _ := records["notes"].([]any)
	if len(notes) != 1 {
		t.Fatalf("want exactly 1 owned note in export, got %d: %v", len(notes), records)
	}
	note, _ := notes[0].(map[string]any)
	if note["title"] != "mine" {
		t.Fatalf("want the note we created, got %v", note)
	}

	// tags is a shared (non-owner-scoped) table and must not appear in a
	// per-user export at all.
	if _, present := records["tags"]; present {
		t.Fatalf("want no shared-table key in the export, got %v", records)
	}
}

func TestIdentity_DeleteAccount(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	client := newClient(t)
	signUp(t, client, baseURL, "delete-me@example.com", "correcthorse")
	token := restTokenForClient(t, baseURL, client)

	created := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "gone soon"})
	rec := decodeJSONMap(t, created)
	noteID := rec["id"].(string)

	// IDNT-10
	del, err := client.Do(mustRequest(t, http.MethodDelete, baseURL+"/api/auth/me"))
	if err != nil {
		t.Fatalf("DELETE /api/auth/me: %v", err)
	}
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", del.StatusCode, bodyString(t, del))
	}
	if len(client.Jar.Cookies(mustURL(t, baseURL))) != 0 {
		t.Fatal("want the session cookie cleared after account deletion")
	}

	// The old credentials no longer work.
	loginResp := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "delete-me@example.com", "password": "correcthorse",
	})
	if loginResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want login with deleted account's old credentials to fail, got %d", loginResp.StatusCode)
	}

	// The now-revoked session no longer authenticates anything.
	me, err := client.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 after account deletion, got %d", me.StatusCode)
	}

	// Owned rows are gone too — confirmed via a second user's own token
	// (the deleted user's own token/session is no longer valid to check
	// with directly).
	otherToken := restToken(t, baseURL, "witness@example.com", []string{"records:read", "records:write"})
	stillThere := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+noteID, otherToken, nil)
	if stillThere.StatusCode != http.StatusNotFound {
		t.Fatalf("want the deleted user's note gone (404), got %d", stillThere.StatusCode)
	}
}

func TestIdentity_DeleteAccountRevokesOAuthGrants(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	client := newClient(t)
	signUp(t, client, baseURL, "revoke-me@example.com", "correcthorse")
	token := restTokenForClient(t, baseURL, client)

	// The token works before deletion.
	before := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes", token, nil)
	if before.StatusCode != http.StatusOK {
		t.Fatalf("want the token to work before deletion, got %d: %s", before.StatusCode, bodyString(t, before))
	}

	del, err := client.Do(mustRequest(t, http.MethodDelete, baseURL+"/api/auth/me"))
	if err != nil {
		t.Fatalf("DELETE /api/auth/me: %v", err)
	}
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", del.StatusCode, bodyString(t, del))
	}

	// IDNT-13: the same access token, still within its natural lifetime,
	// no longer works — it was revoked, not just left to expire.
	after := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes", token, nil)
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want the token revoked (401) after account deletion, got %d: %s", after.StatusCode, bodyString(t, after))
	}
}

func TestIdentity_DeleteAccountDoesNotAffectOtherUsers(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)

	clientA := newClient(t)
	signUp(t, clientA, baseURL, "victim-a@example.com", "correcthorse")
	tokenA := restTokenForClient(t, baseURL, clientA)
	createdA := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenA, map[string]any{"title": "A's note"})
	recA := decodeJSONMap(t, createdA)
	idA := recA["id"].(string)

	clientB := newClient(t)
	signUp(t, clientB, baseURL, "bystander-b@example.com", "correcthorse")
	tokenB := restTokenForClient(t, baseURL, clientB)
	doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenB, map[string]any{"title": "B's note"})

	// IDNT-11: deleting A's account must not touch B's account or data.
	del, err := clientA.Do(mustRequest(t, http.MethodDelete, baseURL+"/api/auth/me"))
	if err != nil {
		t.Fatalf("DELETE /api/auth/me: %v", err)
	}
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", del.StatusCode, bodyString(t, del))
	}

	bMe, err := clientB.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if bMe.StatusCode != http.StatusOK {
		t.Fatalf("want B's session still valid after A's deletion, got %d", bMe.StatusCode)
	}

	bList := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes", tokenB, nil)
	bBody := decodeJSONMap(t, bList)
	bRecords, _ := bBody["records"].([]any)
	if len(bRecords) != 1 {
		t.Fatalf("want B's own note untouched, got %d records: %v", len(bRecords), bRecords)
	}

	// A's note id must not have leaked into B's ownership either.
	crossGet := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+idA, tokenB, nil)
	if crossGet.StatusCode != http.StatusNotFound {
		t.Fatalf("want A's deleted note inaccessible to B, got %d", crossGet.StatusCode)
	}
}

func TestIdentity_ListAndRevokeConsent(t *testing.T) {
	// AUTHZ-11 (spec/oauth-authorize-and-consent.md)
	baseURL := testserver.New(t)
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "profile records:read")

	client := newClient(t)
	signUp(t, client, baseURL, "consent-mgmt@example.com", "correcthorse")
	accessToken := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"profile", "records:read"})["access_token"].(string)

	// The token works before revocation, so the test proves revocation
	// is what changed the outcome.
	before := mustAuthedGet(t, baseURL+"/api/oauth/whoami", accessToken)
	if before.StatusCode != http.StatusOK {
		t.Fatalf("setup: token should work before revocation, got %d", before.StatusCode)
	}

	listResp, err := client.Get(baseURL + "/api/auth/me/consents")
	if err != nil {
		t.Fatalf("GET /api/auth/me/consents: %v", err)
	}
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", listResp.StatusCode, bodyString(t, listResp))
	}
	listBody := decodeJSONMap(t, listResp)
	consents, _ := listBody["consents"].([]any)
	if len(consents) != 1 {
		t.Fatalf("want exactly 1 consent listed, got %d: %v", len(consents), consents)
	}
	entry, _ := consents[0].(map[string]any)
	if entry["client_id"] != clientID {
		t.Fatalf("want the registered client listed, got %v", entry)
	}

	delReq := mustRequest(t, http.MethodDelete, baseURL+"/api/auth/me/consents/"+clientID)
	delResp, err := client.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE /api/auth/me/consents/%s: %v", clientID, err)
	}
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", delResp.StatusCode, bodyString(t, delResp))
	}

	// The previously-issued access token no longer works.
	after := mustAuthedGet(t, baseURL+"/api/oauth/whoami", accessToken)
	if after.StatusCode == http.StatusOK {
		t.Fatalf("want the revoked client's token rejected, got 200: %s", bodyString(t, after))
	}

	// The consent list is now empty.
	afterList := decodeJSONMap(t, mustGet(t, client, baseURL+"/api/auth/me/consents"))
	afterConsents, _ := afterList["consents"].([]any)
	if len(afterConsents) != 0 {
		t.Fatalf("want no consents left after revocation, got %v", afterConsents)
	}

	// A later authorize request for this client shows the consent screen
	// again, from a clean slate.
	q := authorizeParams(clientID, testRedirectURI, "profile", "freshstate12345", mustChallenge(t))
	fresh, err := client.Get(baseURL + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatalf("GET /oauth/authorize: %v", err)
	}
	if fresh.StatusCode != http.StatusOK {
		t.Fatalf("want the consent screen shown again after revocation, got %d", fresh.StatusCode)
	}
}

func TestIdentity_ExportAndDeleteRequireAuth(t *testing.T) {
	baseURL := testserver.New(t)
	client := newClient(t) // never signed in

	// IDNT-12
	export, err := client.Get(baseURL + "/api/auth/me/export")
	if err != nil {
		t.Fatalf("GET /api/auth/me/export: %v", err)
	}
	if export.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for anonymous export, got %d", export.StatusCode)
	}

	del, err := client.Do(mustRequest(t, http.MethodDelete, baseURL+"/api/auth/me"))
	if err != nil {
		t.Fatalf("DELETE /api/auth/me: %v", err)
	}
	if del.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for anonymous delete, got %d", del.StatusCode)
	}
}

// mustRequest builds a body-less request — net/http.Client has no DELETE
// helper analogous to Get/Post, so this fills that gap for these tests.
func mustRequest(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	return req
}
