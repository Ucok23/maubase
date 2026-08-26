package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/auto-rest.md

const (
	notesSchema = `CREATE TABLE notes (
		id       TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		title    TEXT NOT NULL,
		body     TEXT
	)`
	tagsSchema = `CREATE TABLE tags (
		id   TEXT PRIMARY KEY,
		name TEXT NOT NULL
	)`
)

// restToken registers a public client, signs up a fresh user, and drives
// them through consent for the given scopes, returning a ready-to-use
// access token. Each call uses a distinct email so tests can get
// independent "users" cheaply.
func restToken(t *testing.T, baseURL, email string, scopes []string) string {
	t.Helper()
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "records:read records:write")
	client := newClient(t)
	signUp(t, client, baseURL, email, "correcthorse")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, scopes)
	at, _ := tok["access_token"].(string)
	if at == "" {
		t.Fatalf("setup: no access_token in %v", tok)
	}
	return at
}

func doAuthed(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func TestRestAPI_InternalTablesNeverExposed(t *testing.T) {
	baseURL := testserver.New(t)
	token := restToken(t, baseURL, "internal-tables@example.com", []string{"records:read", "records:write"})

	// REST-COL-01
	for _, table := range []string{
		"users", "oauth_clients", "owner_users", "sessions",
		// password_reset_tokens and social_identities shipped exposed
		// (see registry.go's reservedTables doc comment) — a real
		// account-takeover vulnerability. Named explicitly here, in
		// addition to TestRestAPI_NoAppSchemaExposesNothing below,
		// because that incident is exactly the kind of thing worth a
		// test a future reader can find by grepping the table name.
		"password_reset_tokens", "social_identities",
	} {
		resp := doAuthed(t, http.MethodGet, baseURL+"/api/data/"+table, token, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET /api/data/%s: want 404, got %d: %s", table, resp.StatusCode, bodyString(t, resp))
		}
	}
}

// TestRestAPI_NoAppSchemaExposesNothing is REST-COL-01's structural
// backstop: rather than checking a hand-maintained list of table names
// (which is exactly what let password_reset_tokens/social_identities
// ship exposed — see registry.go's reservedTables doc comment), this
// stands up a server with maubase's own migrations only — no deployment
// schema at all — and asserts the admin UI's data-browser landing page
// (GET /admin/ui/data, backed by restapi.Server.AdminCollections, the
// exact same registry auto-REST itself uses) shows its "no application
// tables yet" empty state. Every table maubase's own migrations create
// is necessarily one of its own internal tables — if Discover ever
// finds one that isn't in reservedTables, this fails loudly the moment
// a new migration ships (the empty state disappears, replaced by a
// card naming the leaked table), instead of silently exposing it until
// someone happens to write a by-name test for that specific table.
func TestRestAPI_NoAppSchemaExposesNothing(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, "structural-check@example.com", "correcthorse")
	client := newClient(t)
	loginResp := ownerLogin(t, client, baseURL, "structural-check@example.com", "correcthorse")
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("owner login: want 200, got %d: %s", loginResp.StatusCode, bodyString(t, loginResp))
	}

	resp, err := client.Get(baseURL + "/admin/ui/data")
	if err != nil {
		t.Fatalf("GET /admin/ui/data: %v", err)
	}
	body := bodyString(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/ui/data: want 200, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "No application tables yet") {
		t.Fatalf("want the empty state (no app schema means no exposed collections at all), got a non-empty collections list:\n%s", body)
	}
}

func TestRestAPI_CreateThenGet(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "create-get@example.com", []string{"records:read", "records:write"})

	// REST-CRUD-01
	created := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{
		"title": "hello", "body": "world", "owner_id": "someone-else", // owner_id must be ignored
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", created.StatusCode, bodyString(t, created))
	}
	rec := decodeJSONMap(t, created)
	if rec["id"] == nil || rec["id"] == "" {
		t.Fatalf("want a generated id, got %v", rec)
	}
	if rec["title"] != "hello" || rec["body"] != "world" {
		t.Fatalf("want the fields we sent, got %v", rec)
	}

	got := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+rec["id"].(string), token, nil)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", got.StatusCode, bodyString(t, got))
	}
	gotRec := decodeJSONMap(t, got)
	if gotRec["title"] != "hello" {
		t.Fatalf("want the same record back, got %v", gotRec)
	}
}

func TestRestAPI_ReadScopeCannotWrite(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "read-only@example.com", []string{"records:read"})

	// REST-CRUD-02
	resp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "x"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for write with read-only scope, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
}

func TestRestAPI_UpdateOnlyChangesSentFields(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "partial-update@example.com", []string{"records:read", "records:write"})

	created := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{
		"title": "original title", "body": "original body",
	})
	rec := decodeJSONMap(t, created)
	id := rec["id"].(string)

	// REST-CRUD-03
	updated := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+id, token, map[string]any{
		"title": "new title",
	})
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", updated.StatusCode, bodyString(t, updated))
	}
	updRec := decodeJSONMap(t, updated)
	if updRec["title"] != "new title" {
		t.Fatalf("want title updated, got %v", updRec)
	}
	if updRec["body"] != "original body" {
		t.Fatalf("want body untouched, got %v", updRec)
	}
}

func TestRestAPI_DeleteThen404(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "delete-me@example.com", []string{"records:read", "records:write"})

	created := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "temp"})
	rec := decodeJSONMap(t, created)
	id := rec["id"].(string)

	// REST-CRUD-04
	del := doAuthed(t, http.MethodDelete, baseURL+"/api/data/notes/"+id, token, nil)
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", del.StatusCode, bodyString(t, del))
	}
	after := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, token, nil)
	if after.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 after delete, got %d", after.StatusCode)
	}
}

func TestRestAPI_OwnershipListFiltering(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	tokenA := restToken(t, baseURL, "owner-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "owner-b@example.com", []string{"records:read", "records:write"})

	doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenA, map[string]any{"title": "A's note"})
	doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenB, map[string]any{"title": "B's note"})

	// REST-OWNERSHIP-01
	listA := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes", tokenA, nil)
	listBody := decodeJSONMap(t, listA)
	records, _ := listBody["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("want exactly A's own 1 record, got %d: %v", len(records), records)
	}
	first, _ := records[0].(map[string]any)
	if first["title"] != "A's note" {
		t.Fatalf("want A's own note, got %v", first)
	}
}

func TestRestAPI_OwnershipCannotAccessOthersRecord(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	tokenA := restToken(t, baseURL, "victim@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "attacker@example.com", []string{"records:read", "records:write"})

	created := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenA, map[string]any{"title": "private"})
	rec := decodeJSONMap(t, created)
	id := rec["id"].(string)

	// REST-OWNERSHIP-02
	get := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, tokenB, nil)
	if get.StatusCode != http.StatusNotFound {
		t.Errorf("GET another user's record: want 404, got %d", get.StatusCode)
	}
	patch := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+id, tokenB, map[string]any{"title": "hijacked"})
	if patch.StatusCode != http.StatusNotFound {
		t.Errorf("PATCH another user's record: want 404, got %d", patch.StatusCode)
	}
	del := doAuthed(t, http.MethodDelete, baseURL+"/api/data/notes/"+id, tokenB, nil)
	if del.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE another user's record: want 404, got %d", del.StatusCode)
	}

	// Confirm it's untouched by the owner's own view.
	stillThere := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, tokenA, nil)
	if stillThere.StatusCode != http.StatusOK {
		t.Fatalf("want the record still intact for its real owner, got %d", stillThere.StatusCode)
	}
	stillRec := decodeJSONMap(t, stillThere)
	if stillRec["title"] != "private" {
		t.Fatalf("want the record unmodified by the failed attacker PATCH, got %v", stillRec)
	}
}

func TestRestAPI_OwnerIDImmutable(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "immutable-owner@example.com", []string{"records:read", "records:write"})

	created := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "x"})
	rec := decodeJSONMap(t, created)
	originalOwner := rec["owner_id"]
	id := rec["id"].(string)

	// REST-OWNERSHIP-03
	updated := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+id, token, map[string]any{
		"owner_id": "someone-else-entirely",
	})
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("want 200 (owner_id silently ignored, not rejected), got %d: %s", updated.StatusCode, bodyString(t, updated))
	}
	updRec := decodeJSONMap(t, updated)
	if updRec["owner_id"] != originalOwner {
		t.Fatalf("want owner_id unchanged, got %v (was %v)", updRec["owner_id"], originalOwner)
	}
}

func TestRestAPI_SharedTableHasNoFiltering(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, tagsSchema)
	tokenA := restToken(t, baseURL, "shared-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "shared-b@example.com", []string{"records:read", "records:write"})

	created := doAuthed(t, http.MethodPost, baseURL+"/api/data/tags", tokenA, map[string]any{"name": "backend"})
	rec := decodeJSONMap(t, created)
	id := rec["id"].(string)

	// REST-SHARED-01: user B can read and modify user A's row on a
	// shared (no owner_id) table.
	got := doAuthed(t, http.MethodGet, baseURL+"/api/data/tags/"+id, tokenB, nil)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("want 200 (shared table, no ownership), got %d", got.StatusCode)
	}
	updated := doAuthed(t, http.MethodPatch, baseURL+"/api/data/tags/"+id, tokenB, map[string]any{"name": "backend-updated"})
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("want 200 for another user updating a shared row, got %d: %s", updated.StatusCode, bodyString(t, updated))
	}
}

func TestRestAPI_RejectsUnknownField(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "unknown-field@example.com", []string{"records:read", "records:write"})

	// REST-VALIDATION-01
	resp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{
		"title": "x", "not_a_real_column": "y",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for an unknown field, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
}

func TestRestAPI_PrimaryKeyImmutable(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "immutable-pk@example.com", []string{"records:read", "records:write"})

	created := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "x"})
	rec := decodeJSONMap(t, created)
	id := rec["id"].(string)

	// REST-VALIDATION-02
	updated := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+id, token, map[string]any{
		"id": "attempted-new-id", "title": "y",
	})
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("want 200 (id silently ignored, not rejected), got %d: %s", updated.StatusCode, bodyString(t, updated))
	}
	updRec := decodeJSONMap(t, updated)
	if updRec["id"] != id {
		t.Fatalf("want id unchanged, got %v (was %v)", updRec["id"], id)
	}

	// The old id must still resolve; a new one must not have been created.
	stillThere := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, token, nil)
	if stillThere.StatusCode != http.StatusOK {
		t.Fatalf("want the original id to still resolve, got %d", stillThere.StatusCode)
	}
}
