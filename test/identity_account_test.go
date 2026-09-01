package e2e_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/Ucok23/maubase/internal/email"
	"github.com/Ucok23/maubase/internal/social"
	"github.com/Ucok23/maubase/internal/testserver"
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

func TestIdentity_DeleteAccountCascadesSocialIdentitiesAndResetTokens(t *testing.T) {
	// IDNT-15
	provider := fakeGoogleProvider(t, "idnt15-google-uid", "idnt15-provider-email@example.com")
	sender := email.NewFakeSender()
	baseURL := testserver.NewCustom(t, testserver.Options{
		SocialProviders: map[string]social.Provider{"google": provider},
		EmailSender:     sender,
	})

	client := newClient(t)
	signUp(t, client, baseURL, "idnt15-cascade@example.com", "correcthorse")

	// Link a social identity to this account — SOCIAL-09: a signed-in
	// session links the identity to the current account rather than
	// matching/creating by email.
	state := startSocialLogin(t, client, baseURL, "google")
	linkResp := socialCallback(t, client, baseURL, "google", "fake-code", state)
	if linkResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("link social identity: want 303, got %d: %s", linkResp.StatusCode, bodyString(t, linkResp))
	}

	// Leave an outstanding, unredeemed password-reset token.
	fpResp := forgotPassword(t, newClient(t), baseURL, "idnt15-cascade@example.com")
	if fpResp.StatusCode != http.StatusNoContent {
		t.Fatalf("forgot-password: want 204, got %d: %s", fpResp.StatusCode, bodyString(t, fpResp))
	}
	resetToken := tokenFromResetLink(t, sender.Sent())

	del, err := client.Do(mustRequest(t, http.MethodDelete, baseURL+"/api/auth/me"))
	if err != nil {
		t.Fatalf("DELETE /api/auth/me: %v", err)
	}
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", del.StatusCode, bodyString(t, del))
	}

	// The cascaded social_identities row is gone: the same provider
	// identity completing the flow again is "never seen before", not a
	// dangling link to a deleted account — it creates a brand-new
	// account rather than erroring or resurrecting the old one.
	returning := newClient(t)
	state2 := startSocialLogin(t, returning, baseURL, "google")
	cbResp := socialCallback(t, returning, baseURL, "google", "fake-code-2", state2)
	if cbResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("social login for the same identity after account erasure: want 303 (a fresh account), got %d: %s", cbResp.StatusCode, bodyString(t, cbResp))
	}
	newAccount := decodeJSONMap(t, mustGet(t, returning, baseURL+"/api/auth/me"))
	if newAccount["email"] != "idnt15-provider-email@example.com" {
		t.Fatalf("want a brand-new account for the same provider identity, got %v", newAccount)
	}

	// The cascaded password_reset_tokens row is gone: the old token no
	// longer redeems.
	reset := resetPassword(t, baseURL, resetToken, "wontworkanyway1")
	if reset.StatusCode != http.StatusBadRequest {
		t.Fatalf("want the old reset token rejected after account erasure, got %d: %s", reset.StatusCode, bodyString(t, reset))
	}
}

// TestIdentity_ConcurrentDuplicateSignupsOnlyOneSucceeds exercises
// IDNT-16: SignUp relies on the database's own UNIQUE constraint on
// email failing for the loser, not a check-then-insert (which a future
// "optimization" — check if the email exists first, then insert — could
// silently reintroduce a race for), so this was probably already
// race-safe, but nothing ever actually fired concurrent signups at the
// same email to prove it.
func TestIdentity_ConcurrentDuplicateSignupsOnlyOneSucceeds(t *testing.T) {
	// IDNT-16
	baseURL := testserver.New(t)
	const n = 10
	const email = "idnt16-concurrent@example.com"

	results := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := postJSON(t, newClient(t), baseURL+"/api/auth/signup", map[string]string{
				"email": email, "password": "correcthorse",
			})
			results <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(results)

	var successes, conflicts int
	for status := range results {
		switch status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("concurrent signup: want 201 or 409, got %d", status)
		}
	}
	if successes != 1 || conflicts != n-1 {
		t.Fatalf("want exactly 1 success and %d conflicts among %d concurrent signups for the same email, got %d successes and %d conflicts", n-1, n, successes, conflicts)
	}
}

// TestIdentity_ConcurrentExportDuringDeleteNeverCorruptsOrErrors
// exercises IDNT-17: handleDeleteAccount's multi-step erasure isn't
// wrapped in a transaction (application data, then storage, then OAuth
// grants, then the account itself, each its own separate statement/call
// — see its own doc comment), so a concurrent GET /api/auth/me/export
// can genuinely observe a mix of already-erased and not-yet-erased
// collections, with no signal in the response distinguishing that from
// a complete, uncontested export. That's accepted, not fixed here — a
// full fix means wrapping cross-store erasure in a transaction spanning
// auto-REST, storage, and OAuth grants, a bigger change than this
// scenario's severity warrants — but the actual safety properties this
// relies on (never a 500, never a torn/partial row within one
// collection, since each collection's read or delete is its own single
// atomic SQL statement) had never been exercised concurrently at all.
func TestIdentity_ConcurrentExportDuringDeleteNeverCorruptsOrErrors(t *testing.T) {
	// IDNT-17
	baseURL := testserver.NewWithSchema(t, notesSchema)
	client := newClient(t)
	signUp(t, client, baseURL, "idnt17-concurrent@example.com", "correcthorse")
	token := restTokenForClient(t, baseURL, client)

	for i := 0; i < 5; i++ {
		doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": fmt.Sprintf("note-%d", i)})
	}

	var exportResp *http.Response
	var exportErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		exportResp, exportErr = client.Get(baseURL + "/api/auth/me/export")
	}()

	delResp, delErr := client.Do(mustRequest(t, http.MethodDelete, baseURL+"/api/auth/me"))
	wg.Wait()

	if delErr != nil {
		t.Fatalf("DELETE /api/auth/me: %v", delErr)
	}
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d: %s", delResp.StatusCode, bodyString(t, delResp))
	}

	if exportErr != nil {
		t.Fatalf("concurrent GET /api/auth/me/export: %v", exportErr)
	}
	// The export's session lookup can land on either side of the delete
	// revoking it: 200 with a (possibly partial, per this test's own doc
	// comment) snapshot, or 401 once the session is already gone. Never
	// a 500 — that's the actual property under test.
	if exportResp.StatusCode != http.StatusOK && exportResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("concurrent export during delete: want 200 or 401, got %d: %s", exportResp.StatusCode, bodyString(t, exportResp))
	}
	if exportResp.StatusCode == http.StatusOK {
		body := decodeJSONMap(t, exportResp)
		records, _ := body["records"].(map[string]any)
		if notesVal, ok := records["notes"]; ok {
			list, _ := notesVal.([]any)
			// Whatever count of notes it saw (anywhere from 0 to all 5,
			// depending on how the race landed), every row present is a
			// complete, real record — never a torn/partial one.
			for _, r := range list {
				rec, _ := r.(map[string]any)
				if rec["id"] == nil || rec["title"] == nil {
					t.Fatalf("want every exported row complete, got %v", rec)
				}
			}
		}
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

func TestIdentity_BearerTokenWinsOverCookieWhenBothPresent(t *testing.T) {
	// IDNT-18
	baseURL := testserver.NewWithSchema(t, notesSchema)

	// A signs up (which auto-logs-in and sets a session cookie in A's own
	// jar — see handleSignUp).
	clientA := newClient(t)
	signUp(t, clientA, baseURL, "idnt18-a@example.com", "correcthorse")

	// B signs up too, then its raw session token is pulled straight out
	// of its own cookie jar, so it can be replayed as a Bearer header on
	// a request that also carries A's cookie.
	clientB := newClient(t)
	signUp(t, clientB, baseURL, "idnt18-b@example.com", "correcthorse")
	var bToken string
	for _, c := range clientB.Jar.Cookies(mustURL(t, baseURL)) {
		if c.Name == "maubase_session" {
			bToken = c.Value
		}
	}
	if bToken == "" {
		t.Fatalf("setup: couldn't find B's session cookie in its own jar")
	}

	req := mustRequest(t, http.MethodGet, baseURL+"/api/auth/me")
	req.Header.Set("Authorization", "Bearer "+bToken)
	for _, c := range clientA.Jar.Cookies(mustURL(t, baseURL)) {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	me := decodeJSONMap(t, resp)
	if me["email"] != "idnt18-b@example.com" {
		t.Fatalf("want the bearer token's account (B) to win over the cookie's (A), got %v", me)
	}
}
