package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Ucok23/maubase/internal/testserver"
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

// TestRestAPI_CreateWithNullBodyDoesNotPanic: a literal JSON `null` body
// decodes successfully into a nil (not empty) map — encoding/json's
// documented behavior for a map destination — and every downstream write
// into that map (owner-stamping, PK generation) used to panic on it. A
// table with only nullable, defaulted, or PK columns is used deliberately
// so the create can succeed cleanly once the nil map is fixed, isolating
// this from the separate (tracked) issue of constraint violations
// surfacing as 500 instead of 400.
func TestRestAPI_CreateWithNullBodyDoesNotPanic(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, `CREATE TABLE nullable_notes (
		id       TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		title    TEXT
	)`)
	token := restToken(t, baseURL, "null-body@example.com", []string{"records:read", "records:write"})

	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/data/nullable_notes", bytes.NewReader([]byte("null")))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST with null body: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201 (an empty object, not a panic), got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	rec := decodeJSONMap(t, resp)
	if rec["id"] == nil || rec["id"] == "" {
		t.Fatalf("want a generated id even with a null body, got %v", rec)
	}
}

// TestRestAPI_OversizedBodyRejected (REST-VALIDATION-03) uses a small,
// explicit MaxRequestBodyBytes rather than the real ~1MB default, so the
// test doesn't need to actually push megabytes over the wire to exercise
// the limit.
func TestRestAPI_OversizedBodyRejected(t *testing.T) {
	baseURL := testserver.NewCustom(t, testserver.Options{
		Schema:              []string{notesSchema},
		MaxRequestBodyBytes: 200,
	})
	token := restToken(t, baseURL, "oversized-body@example.com", []string{"records:read", "records:write"})

	oversized := map[string]any{"title": "x", "body": strings.Repeat("a", 1000)}
	resp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, oversized)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413 for an oversized body, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	// A body under the limit still works, proving 413 was about size, not
	// some other misconfiguration.
	listResp := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes", token, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 listing (should be empty, no row created), got %d", listResp.StatusCode)
	}
	list := decodeJSONMap(t, listResp)
	if records, _ := list["records"].([]any); len(records) != 0 {
		t.Fatalf("want no row created by the rejected oversized request, got %v", records)
	}

	normal := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "small", "body": "x"})
	if normal.StatusCode != http.StatusCreated {
		t.Fatalf("want a normal-sized body to still succeed, got %d: %s", normal.StatusCode, bodyString(t, normal))
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

func TestRestAPI_ConcurrentPatchesToDisjointFieldsBothLand(t *testing.T) {
	// REST-CRUD-05
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "concurrent-patch@example.com", []string{"records:read", "records:write"})

	created := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{
		"title": "original title", "body": "original body",
	})
	rec := decodeJSONMap(t, created)
	id := rec["id"].(string)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		resp := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+id, token, map[string]any{"title": "new title"})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("patch title: want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
		}
	}()
	go func() {
		defer wg.Done()
		resp := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+id, token, map[string]any{"body": "new body"})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("patch body: want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
		}
	}()
	wg.Wait()

	final := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, token, nil)
	finalRec := decodeJSONMap(t, final)
	if finalRec["title"] != "new title" {
		t.Fatalf("want title updated regardless of interleaving, got %v", finalRec)
	}
	if finalRec["body"] != "new body" {
		t.Fatalf("want body updated regardless of interleaving, got %v", finalRec)
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

func TestRestAPI_OwnerIDNonTextAffinityFailsAtStartup(t *testing.T) {
	// REST-OWNERSHIP-04
	for _, tc := range []struct {
		name, declType string
	}{
		{"integer", "INTEGER"},
		{"numeric", "NUMERIC"},
		{"real", "REAL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := `CREATE TABLE bad_owner_notes (
				id       TEXT PRIMARY KEY,
				owner_id ` + tc.declType + ` NOT NULL,
				title    TEXT NOT NULL
			)`
			err := testserver.NewCustomExpectingDiscoverError(t, testserver.Options{
				Schema: []string{schema},
			})
			if err == nil {
				t.Fatalf("want an error, got none")
			}
			msg := err.Error()
			if !strings.Contains(msg, "bad_owner_notes") || !strings.Contains(msg, "owner_id") {
				t.Fatalf("want the error to name the table and owner_id, got: %v", err)
			}
		})
	}
}

func TestRestAPI_OwnerIDTextAffinityStillFiltersCorrectly(t *testing.T) {
	// REST-OWNERSHIP-04: the codebase's own convention (owner_id TEXT) is
	// unaffected by the startup check above, and continues to filter
	// distinct subjects apart correctly — this is the "no coercion"
	// counterpart to the numeric-affinity collision that check exists to
	// reject at startup instead.
	baseURL := testserver.NewWithSchema(t, notesSchema)
	tokenA := restToken(t, baseURL, "text-affinity-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "text-affinity-b@example.com", []string{"records:read", "records:write"})

	doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenA, map[string]any{"title": "a's note"})
	doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenB, map[string]any{"title": "b's note"})

	listA := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes", tokenA, nil)
	var bodyA struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.NewDecoder(listA.Body).Decode(&bodyA); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(bodyA.Records) != 1 || bodyA.Records[0]["title"] != "a's note" {
		t.Fatalf("want exactly A's own note, got %v", bodyA.Records)
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

func TestRestAPI_ConstraintViolationsReturn400Not500(t *testing.T) {
	// REST-VALIDATION-04
	baseURL := testserver.NewWithSchema(t, notesSchema, `CREATE TABLE parents (
		id       TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL
	)`, `CREATE TABLE constrained_items (
		id        TEXT PRIMARY KEY,
		owner_id  TEXT NOT NULL,
		quantity  INTEGER CHECK(quantity >= 0),
		parent_id TEXT REFERENCES parents(id)
	)`, `CREATE TABLE unique_items (
		id       TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		code     TEXT NOT NULL UNIQUE
	)`)
	token := restToken(t, baseURL, "constraint-violation@example.com", []string{"records:read", "records:write"})

	// NOT NULL, via omission, on create: notesSchema's title has no default.
	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"body": "no title"})
	if createResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with NOT NULL field omitted: want 400, got %d: %s", createResp.StatusCode, bodyString(t, createResp))
	}

	// NOT NULL, via explicit null, on update.
	okNote := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "fine"})
	noteID := decodeJSONMap(t, okNote)["id"].(string)
	updateResp := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+noteID, token, map[string]any{"title": nil})
	if updateResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("update with NOT NULL field set to null: want 400, got %d: %s", updateResp.StatusCode, bodyString(t, updateResp))
	}

	// CHECK, on create.
	checkResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/constrained_items", token, map[string]any{"quantity": -1})
	if checkResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create violating CHECK: want 400, got %d: %s", checkResp.StatusCode, bodyString(t, checkResp))
	}

	// FOREIGN KEY, on create.
	fkResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/constrained_items", token, map[string]any{
		"quantity": 1, "parent_id": "no-such-parent",
	})
	if fkResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create violating FOREIGN KEY: want 400, got %d: %s", fkResp.StatusCode, bodyString(t, fkResp))
	}

	// UNIQUE, on update — this path had no constraint handling at all
	// before and always 500'd, unlike create's already-existing 409.
	first := doAuthed(t, http.MethodPost, baseURL+"/api/data/unique_items", token, map[string]any{"code": "taken"})
	second := doAuthed(t, http.MethodPost, baseURL+"/api/data/unique_items", token, map[string]any{"code": "available"})
	firstCode, _ := decodeJSONMap(t, first)["code"].(string)
	secondID, _ := decodeJSONMap(t, second)["id"].(string)
	if firstCode != "taken" || secondID == "" {
		t.Fatalf("setup: want two unique_items rows created, got %v / %v", first, second)
	}
	uniqueResp := doAuthed(t, http.MethodPatch, baseURL+"/api/data/unique_items/"+secondID, token, map[string]any{"code": "taken"})
	if uniqueResp.StatusCode != http.StatusConflict {
		t.Fatalf("update violating UNIQUE: want 409, got %d: %s", uniqueResp.StatusCode, bodyString(t, uniqueResp))
	}

	// An ordinary update still succeeds cleanly, proving classification
	// didn't break the happy path.
	happyUpdate := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+noteID, token, map[string]any{"title": "renamed"})
	if happyUpdate.StatusCode != http.StatusOK {
		t.Fatalf("ordinary update: want 200, got %d: %s", happyUpdate.StatusCode, bodyString(t, happyUpdate))
	}
}

func TestRestAPI_NonScalarFieldValueRejected(t *testing.T) {
	// REST-VALIDATION-05
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "non-scalar-field@example.com", []string{"records:read", "records:write"})

	arrayResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{
		"title": "x", "body": []string{"a", "b"},
	})
	if arrayResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("array field value: want 400, got %d: %s", arrayResp.StatusCode, bodyString(t, arrayResp))
	}

	objectResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{
		"title": "x", "body": map[string]any{"nested": true},
	})
	if objectResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("object field value: want 400, got %d: %s", objectResp.StatusCode, bodyString(t, objectResp))
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

func TestRestAPI_OutOfRangePaginationIsRejected(t *testing.T) {
	// REST-PAGINATION-01
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "pagination-invalid@example.com", []string{"records:read", "records:write"})

	for _, tc := range []struct{ name, query string }{
		{"limit too high", "?limit=999999"},
		{"limit zero", "?limit=0"},
		{"limit negative", "?limit=-1"},
		{"limit non-numeric", "?limit=abc"},
		{"offset negative", "?offset=-1"},
		{"offset non-numeric", "?offset=abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes"+tc.query, token, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", resp.StatusCode, bodyString(t, resp))
			}
		})
	}

	// Omitted entirely still works, defaulting exactly as before —
	// rejection only applies to a param that's actually present and
	// out of range, not to "unset."
	ok := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes", token, nil)
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("want 200 with no pagination params, got %d", ok.StatusCode)
	}
}
