package e2e_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/Ucok23/maubase/internal/testserver"
)

// Scenarios: spec/schema-introspection.md (SCHEMA-01..04)

func TestSchemaIntrospection_RequiresRecordsReadInDevMode(t *testing.T) {
	// SCHEMA-01
	baseURL := testserver.NewCustom(t, testserver.Options{
		DevMode: true,
		Schema:  []string{notesSchema, tagsSchema},
	})
	token := restToken(t, baseURL, "schema-01@example.com", []string{"records:read"})

	resp := doAuthed(t, http.MethodGet, baseURL+"/api/schema", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/schema with records:read: want 200, got %d", resp.StatusCode)
	}
	body := decodeJSONMap(t, resp)
	collections, _ := body["collections"].([]any)
	byName := map[string]map[string]any{}
	for _, c := range collections {
		m, _ := c.(map[string]any)
		byName[m["name"].(string)] = m
	}
	notes, ok := byName["notes"]
	if !ok {
		t.Fatalf("want a \"notes\" collection, got: %v", body)
	}
	if notes["pk_column"] != "id" {
		t.Fatalf("want notes.pk_column == \"id\", got: %v", notes)
	}
	if notes["owner_column"] != "owner_id" {
		t.Fatalf("want notes.owner_column == \"owner_id\" (auto-detected), got: %v", notes)
	}
	if notes["read_rule"] != "owner" || notes["create_rule"] != "owner" {
		t.Fatalf("want notes' default rules to be owner-scoped (owner_id present, no _policies override), got: %v", notes)
	}
	cols, _ := notes["columns"].([]any)
	var sawTitle bool
	for _, c := range cols {
		col, _ := c.(map[string]any)
		if col["name"] == "title" {
			sawTitle = true
			if col["type"] != "TEXT" {
				t.Fatalf("want notes.title's declared type reported as TEXT, got: %v", col)
			}
		}
	}
	if !sawTitle {
		t.Fatalf("want notes' columns to include \"title\", got: %v", cols)
	}

	tags, ok := byName["tags"]
	if !ok {
		t.Fatalf("want a \"tags\" collection too, got: %v", body)
	}
	if tags["owner_column"] != "" || tags["read_rule"] != "shared" {
		t.Fatalf("want tags (no owner_id column) reported shared, got: %v", tags)
	}

	// Same rejection as any /api/data/* GET without the scope.
	noScopeToken := restToken(t, baseURL, "schema-01-write-only@example.com", []string{"records:write"})
	forbidden := doAuthed(t, http.MethodGet, baseURL+"/api/schema", noScopeToken, nil)
	if forbidden.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/schema with only records:write: want 401, got %d", forbidden.StatusCode)
	}
	anon, err := http.Get(baseURL + "/api/schema")
	if err != nil {
		t.Fatalf("anonymous GET /api/schema: %v", err)
	}
	if anon.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/schema (dev mode on): want 401, got %d", anon.StatusCode)
	}
}

func TestSchemaIntrospection_404sOutsideDevelopment(t *testing.T) {
	// SCHEMA-02
	baseURL := testserver.NewWithSchema(t, notesSchema) // DevMode defaults false

	anon, err := http.Get(baseURL + "/api/schema")
	if err != nil {
		t.Fatalf("anonymous GET /api/schema: %v", err)
	}
	if anon.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/schema outside dev mode, no token: want 404, got %d", anon.StatusCode)
	}

	// Even a fully valid records:read token doesn't get further than 404
	// — the route isn't just access-denied, it's not registered at all.
	token := restToken(t, baseURL, "schema-02@example.com", []string{"records:read"})
	authed := doAuthed(t, http.MethodGet, baseURL+"/api/schema", token, nil)
	if authed.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/schema outside dev mode, valid token: want 404, got %d", authed.StatusCode)
	}
}

func TestSchemaIntrospection_NeverExposesReservedTables(t *testing.T) {
	// SCHEMA-03
	baseURL := testserver.NewCustom(t, testserver.Options{
		DevMode: true,
		Schema:  []string{notesSchema},
	})
	token := restToken(t, baseURL, "schema-03@example.com", []string{"records:read"})

	resp := doAuthed(t, http.MethodGet, baseURL+"/api/schema", token, nil)
	body := decodeJSONMap(t, resp)
	collections, _ := body["collections"].([]any)
	for _, c := range collections {
		m, _ := c.(map[string]any)
		name, _ := m["name"].(string)
		for _, reserved := range []string{"users", "sessions", "oauth_clients", "oauth_access_tokens", "_policies", "owner_users", "schema_migrations", "files"} {
			if name == reserved {
				t.Fatalf("want reserved table %q never exposed via /api/schema, got: %v", reserved, collections)
			}
		}
	}
}

func TestSchemaIntrospection_ReflectsATableAddedAtRuntimeWithNoRestart(t *testing.T) {
	// SCHEMA-04
	baseURL := testserver.NewCustom(t, testserver.Options{
		DevMode:                true,
		BootstrapOwnerEmail:    bootstrapEmail,
		BootstrapOwnerPassword: bootstrapPassword,
	})
	token := restToken(t, baseURL, "schema-04@example.com", []string{"records:read"})

	before := doAuthed(t, http.MethodGet, baseURL+"/api/schema", token, nil)
	beforeBody := decodeJSONMap(t, before)
	if collections, _ := beforeBody["collections"].([]any); len(collections) != 0 {
		t.Fatalf("want no collections before any table exists, got: %v", collections)
	}

	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	ddlResp, err := owner.PostForm(baseURL+"/admin/ui/sql", url.Values{
		"query": {`CREATE TABLE gadgets (id TEXT PRIMARY KEY, name TEXT NOT NULL)`},
	})
	if err != nil {
		t.Fatalf("run DDL via SQL Studio: %v", err)
	}
	if ddlResp.StatusCode != http.StatusOK {
		t.Fatalf("run DDL via SQL Studio: want 200, got %d", ddlResp.StatusCode)
	}

	after := doAuthed(t, http.MethodGet, baseURL+"/api/schema", token, nil)
	afterBody := decodeJSONMap(t, after)
	collections, _ := afterBody["collections"].([]any)
	var sawGadgets bool
	for _, c := range collections {
		m, _ := c.(map[string]any)
		if m["name"] == "gadgets" {
			sawGadgets = true
		}
	}
	if !sawGadgets {
		t.Fatalf("want \"gadgets\" reflected in /api/schema right after SQL Studio created it, no restart, got: %v", collections)
	}
}
