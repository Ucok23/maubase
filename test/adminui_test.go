package e2e_test

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/admin-ui.md (ADMINUI-01..30)

func adminUILogin(t *testing.T, client *http.Client, baseURL, email, password string) *http.Response {
	t.Helper()
	resp, err := client.PostForm(baseURL+"/admin/ui/login", url.Values{"email": {email}, "password": {password}})
	if err != nil {
		t.Fatalf("POST /admin/ui/login: %v", err)
	}
	return resp
}

func TestAdminUI_AnonymousRedirectsToLogin(t *testing.T) {
	// ADMINUI-01
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	resp := doGetNoRedirect(t, newClient(t), baseURL+"/admin/ui")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous dashboard: want 303, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/ui/login" {
		t.Fatalf("want redirect to /admin/ui/login, got %q", loc)
	}
}

func TestAdminUI_LoginRedirectsToDashboardAndSetsCookie(t *testing.T) {
	// ADMINUI-02
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	client := newClient(t)
	resp := adminUILogin(t, client, baseURL, bootstrapEmail, bootstrapPassword)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/ui" {
		t.Fatalf("want redirect to /admin/ui, got %q", loc)
	}
	var sawCookie bool
	for _, c := range resp.Cookies() {
		if c.Name == "maubase_owner_session" {
			sawCookie = true
		}
	}
	if !sawCookie {
		t.Fatalf("want the owner-plane session cookie set, got %v", resp.Cookies())
	}

	dash := doGetNoRedirect(t, client, baseURL+"/admin/ui")
	if dash.StatusCode != http.StatusOK {
		t.Fatalf("dashboard after login: want 200, got %d", dash.StatusCode)
	}
}

func TestAdminUI_WrongCredentialsReshowFormWithError(t *testing.T) {
	// ADMINUI-03
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	client := newClient(t)
	resp := adminUILogin(t, client, baseURL, bootstrapEmail, "wrong-password")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wrong password: want 200 (form re-shown), got %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "invalid email or password") {
		t.Fatalf("want an error message in the re-shown form, got: %s", body)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "maubase_owner_session" {
			t.Fatalf("want no session cookie set on failed login, got one")
		}
	}
}

func TestAdminUI_LogoutClearsSessionAndRedirects(t *testing.T) {
	// ADMINUI-04
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	client := newClient(t)
	adminUILogin(t, client, baseURL, bootstrapEmail, bootstrapPassword)

	resp, err := client.Post(baseURL+"/admin/ui/logout", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /admin/ui/logout: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/ui/login" {
		t.Fatalf("logout: want 303 to /admin/ui/login, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	after := doGetNoRedirect(t, client, baseURL+"/admin/ui")
	if after.StatusCode != http.StatusSeeOther {
		t.Fatalf("dashboard after logout: want 303 (session gone), got %d", after.StatusCode)
	}
}

func TestAdminUI_RoleBelowPageMinimumGets403(t *testing.T) {
	// ADMINUI-05
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword) // JSON login, for setup only

	viewerEmail, viewerPassword := "adminui-viewer@example.com", "viewerpassword1"
	if resp := createOwner(t, owner, baseURL, viewerEmail, viewerPassword, "viewer"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create viewer: want 201, got %d", resp.StatusCode)
	}

	viewer := newClient(t)
	adminUILogin(t, viewer, baseURL, viewerEmail, viewerPassword)
	resp := doGetNoRedirect(t, viewer, baseURL+"/admin/ui/owners")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer visiting /admin/ui/owners (needs admin+): want 403, got %d", resp.StatusCode)
	}
}

func TestAdminUI_OwnersPageListsEveryAccount(t *testing.T) {
	// ADMINUI-06
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-second@example.com", "secondpassword1", "admin")

	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	resp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/owners")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owners page: want 200, got %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, bootstrapEmail) || !strings.Contains(body, "adminui-second@example.com") {
		t.Fatalf("want both owners listed, got: %s", body)
	}
}

func TestAdminUI_OnlyOwnerRoleCanCreateAnOwner(t *testing.T) {
	// ADMINUI-07
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-admin@example.com", "adminpassword1", "admin")

	admin := newClient(t)
	adminUILogin(t, admin, baseURL, "adminui-admin@example.com", "adminpassword1")
	resp, err := admin.PostForm(baseURL+"/admin/ui/owners", url.Values{
		"email": {"adminui-blocked@example.com"}, "password": {"blockedpassword1"}, "role": {"viewer"},
	})
	if err != nil {
		t.Fatalf("POST /admin/ui/owners: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin-role create-owner: want 403, got %d", resp.StatusCode)
	}
}

func TestAdminUI_OwnerCanDeleteButNotTheLastOne(t *testing.T) {
	// ADMINUI-08
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createResp := createOwner(t, owner, baseURL, "adminui-todelete@example.com", "todeletepassword1", "viewer")
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)

	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	delResp, err := owner.PostForm(baseURL+"/admin/ui/owners/"+id+"/delete", nil)
	if err != nil {
		t.Fatalf("delete owner: %v", err)
	}
	if delResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete second owner: want 303, got %d: %s", delResp.StatusCode, bodyString(t, delResp))
	}
	listResp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/owners")
	if strings.Contains(bodyString(t, listResp), "adminui-todelete@example.com") {
		t.Fatalf("want deleted owner gone from the list")
	}

	// Now only the bootstrap owner remains; deleting it must be refused.
	meResp, err := owner.Get(baseURL + "/admin/auth/me")
	if err != nil {
		t.Fatalf("GET /admin/auth/me: %v", err)
	}
	me := decodeJSONMap(t, meResp)
	selfID, _ := me["id"].(string)

	lastResp, err := owner.PostForm(baseURL+"/admin/ui/owners/"+selfID+"/delete", nil)
	if err != nil {
		t.Fatalf("delete last owner: %v", err)
	}
	if lastResp.StatusCode != http.StatusOK {
		t.Fatalf("delete last owner: want 200 (form re-shown with error), got %d", lastResp.StatusCode)
	}
	if !strings.Contains(bodyString(t, lastResp), "last") {
		t.Fatalf("want an error mentioning the last-owner refusal, got: %s", bodyString(t, lastResp))
	}
}

func TestAdminUI_AuditLogPageListsEntries(t *testing.T) {
	// ADMINUI-09
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	resp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/audit-log")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit log page: want 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(bodyString(t, resp), "login") {
		t.Fatalf("want the login event to appear in the audit log page")
	}
}

func TestAdminUI_MaintenancePurgeShowsCounts(t *testing.T) {
	// ADMINUI-10
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	resp, err := owner.PostForm(baseURL+"/admin/ui/maintenance/purge-sessions", nil)
	if err != nil {
		t.Fatalf("purge sessions: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge sessions: want 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(bodyString(t, resp), "Purged") {
		t.Fatalf("want the purge result shown on the page")
	}
}

func TestAdminUI_CollectionListExcludesInternalTables(t *testing.T) {
	// ADMINUI-11
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	resp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/data")
	body := bodyString(t, resp)
	if !strings.Contains(body, `href="/admin/ui/data/notes"`) {
		t.Fatalf("want notes listed as a collection, got: %s", body)
	}
	for _, internal := range []string{"sessions", "_policies", "files", "users", "oauth_clients"} {
		if strings.Contains(body, `href="/admin/ui/data/`+internal+`"`) {
			t.Fatalf("want internal table %q never listed, got: %s", internal, body)
		}
	}
}

func TestAdminUI_RowListingShowsEveryRowRegardlessOfOwner(t *testing.T) {
	// ADMINUI-12, ADMINUI-13
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	create := func(title, ownerID string) {
		resp, err := owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{
			"title": {title}, "body": {"x"}, "owner_id": {ownerID},
		})
		if err != nil {
			t.Fatalf("create row: %v", err)
		}
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("create row: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
		}
	}
	create("from user A", "user-a")
	create("from user B", "user-b")

	resp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes")
	body := bodyString(t, resp)
	if !strings.Contains(body, "from user A") || !strings.Contains(body, "from user B") {
		t.Fatalf("want both users' rows visible regardless of owner_id, got: %s", body)
	}
	// ADMINUI-13: owner_id was set explicitly, not overridden to the
	// admin's own subject (unlike POST /api/data/{table}).
	if !strings.Contains(body, "user-a") || !strings.Contains(body, "user-b") {
		t.Fatalf("want the explicit owner_id values shown, got: %s", body)
	}
}

func TestAdminUI_ViewerCanReadButNotWrite(t *testing.T) {
	// ADMINUI-14
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-viewer2@example.com", "viewerpassword2", "viewer")

	viewer := newClient(t)
	adminUILogin(t, viewer, baseURL, "adminui-viewer2@example.com", "viewerpassword2")

	resp := doGetNoRedirect(t, viewer, baseURL+"/admin/ui/data/notes")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer reading data browser: want 200, got %d", resp.StatusCode)
	}
	if strings.Contains(bodyString(t, resp), "Create row") {
		t.Fatalf("want no create-row form shown to a viewer")
	}

	createResp, err := viewer.PostForm(baseURL+"/admin/ui/data/notes", url.Values{"title": {"x"}, "owner_id": {"x"}})
	if err != nil {
		t.Fatalf("viewer create attempt: %v", err)
	}
	if createResp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create attempt: want 403, got %d", createResp.StatusCode)
	}
}

func TestAdminUI_IgnoresAccessPolicies(t *testing.T) {
	// ADMINUI-15
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema, policyRow("notes", "delete", "denied")},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	createResp, err := owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{
		"title": {"to be deleted"}, "owner_id": {"someone"},
	})
	if err != nil || createResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create row: %v (status %d)", err, createResp.StatusCode)
	}

	rowsResp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes")
	body := bodyString(t, rowsResp)
	const marker = `id="row-`
	start := strings.Index(body, marker)
	if start == -1 {
		t.Fatalf("want a row in the listing, got: %s", body)
	}
	rest := body[start+len(marker):]
	id := rest[:strings.Index(rest, `"`)]

	delResp, err := owner.PostForm(baseURL+"/admin/ui/data/notes/"+id+"/delete", nil)
	if err != nil {
		t.Fatalf("delete row: %v", err)
	}
	if delResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete under delete:denied via admin UI: want 303 (denied policy ignored), got %d: %s", delResp.StatusCode, bodyString(t, delResp))
	}
	after := doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes")
	if strings.Contains(bodyString(t, after), "to be deleted") {
		t.Fatalf("want the row actually gone after admin-UI delete")
	}
}

// doGetNoRedirect performs a GET without following redirects, so the
// caller can assert on a 303's Location header directly.
func doGetNoRedirect(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func TestAdminUI_CreateTableAppearsLiveEverywhere(t *testing.T) {
	// ADMINUI-21
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	resp, err := owner.PostForm(baseURL+"/admin/ui/tables", url.Values{
		"name":           {"widgets"},
		"col_name_0":     {"label"},
		"col_type_0":     {"TEXT"},
		"col_required_0": {"1"},
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create table: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	collections := doGetNoRedirect(t, owner, baseURL+"/admin/ui/data")
	if !strings.Contains(bodyString(t, collections), `href="/admin/ui/data/widgets"`) {
		t.Fatalf("want widgets in the collection list right after creation, no restart")
	}

	rows := doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/widgets")
	if rows.StatusCode != http.StatusOK {
		t.Fatalf("browse the new table: want 200, got %d", rows.StatusCode)
	}
	if !strings.Contains(bodyString(t, rows), "label") {
		t.Fatalf("want the label column in the new table's browser")
	}

	// Also live at /api/data/widgets, same as any other collection.
	token := restToken(t, baseURL, "adminui-widgets@example.com", []string{"records:read"})
	apiResp := doAuthed(t, http.MethodGet, baseURL+"/api/data/widgets", token, nil)
	if apiResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/data/widgets: want 200, got %d", apiResp.StatusCode)
	}
}

func TestAdminUI_CreateTableOwnerScopedAddsOwnerColumn(t *testing.T) {
	// ADMINUI-22
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	resp, err := owner.PostForm(baseURL+"/admin/ui/tables", url.Values{
		"name": {"scoped_things"}, "owner_scoped": {"1"},
	})
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create table: %v (status %d)", err, resp.StatusCode)
	}

	tokenA := restToken(t, baseURL, "adminui-scoped-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "adminui-scoped-b@example.com", []string{"records:read"})

	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/scoped_things", tokenA, map[string]any{})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create via customer API: want 201, got %d: %s", createResp.StatusCode, bodyString(t, createResp))
	}
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)

	// Behaves like any other owner-scoped table: invisible to a
	// different customer token.
	bGet := doAuthed(t, http.MethodGet, baseURL+"/api/data/scoped_things/"+id, tokenB, nil)
	if bGet.StatusCode != http.StatusNotFound {
		t.Fatalf("other user get: want 404 (real owner_id scoping), got %d", bGet.StatusCode)
	}
}

func TestAdminUI_CreateTableRejectsInvalidOrReservedNames(t *testing.T) {
	// ADMINUI-23
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	for _, name := range []string{"Bad-Name", "1starts_with_digit", "sessions", "_policies"} {
		resp, err := owner.PostForm(baseURL+"/admin/ui/tables", url.Values{"name": {name}})
		if err != nil {
			t.Fatalf("create table %q: %v", name, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create table %q: want 200 (form re-shown with error), got %d", name, resp.StatusCode)
		}
		if !strings.Contains(bodyString(t, resp), "alert") {
			t.Fatalf("create table %q: want an error shown, got no alert", name)
		}
	}
}

func TestAdminUI_ViewerCannotCreateTable(t *testing.T) {
	// ADMINUI-24
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-viewer3@example.com", "viewerpassword3", "viewer")

	viewer := newClient(t)
	adminUILogin(t, viewer, baseURL, "adminui-viewer3@example.com", "viewerpassword3")

	getResp := doGetNoRedirect(t, viewer, baseURL+"/admin/ui/tables/new")
	if getResp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer GET /admin/ui/tables/new: want 403, got %d", getResp.StatusCode)
	}
	postResp, err := viewer.PostForm(baseURL+"/admin/ui/tables", url.Values{"name": {"nope"}})
	if err != nil {
		t.Fatalf("viewer create table: %v", err)
	}
	if postResp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer POST /admin/ui/tables: want 403, got %d", postResp.StatusCode)
	}
}

func TestAdminUI_SQLStudioRequiresOwnerRole(t *testing.T) {
	// ADMINUI-16
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-admin2@example.com", "adminpassword2", "admin")

	admin := newClient(t)
	adminUILogin(t, admin, baseURL, "adminui-admin2@example.com", "adminpassword2")

	getResp := doGetNoRedirect(t, admin, baseURL+"/admin/ui/sql")
	if getResp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin-role GET /admin/ui/sql: want 403, got %d", getResp.StatusCode)
	}
	postResp, err := admin.PostForm(baseURL+"/admin/ui/sql", url.Values{"query": {"select 1"}})
	if err != nil {
		t.Fatalf("admin-role run query: %v", err)
	}
	if postResp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin-role POST /admin/ui/sql: want 403, got %d", postResp.StatusCode)
	}
}

func TestAdminUI_SQLStudioSelectShowsRows(t *testing.T) {
	// ADMINUI-17
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{"title": {"sql-visible"}, "body": {"x"}, "owner_id": {"x"}})

	resp, err := owner.PostForm(baseURL+"/admin/ui/sql", url.Values{"query": {"select title from notes"}})
	if err != nil {
		t.Fatalf("run select: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run select: want 200, got %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "sql-visible") || !strings.Contains(body, "<th>title</th>") {
		t.Fatalf("want the query's rows and column header shown, got: %s", body)
	}
}

func TestAdminUI_SQLStudioDDLTakesEffectImmediately(t *testing.T) {
	// ADMINUI-18
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	resp, err := owner.PostForm(baseURL+"/admin/ui/sql", url.Values{
		"query": {`CREATE TABLE gadgets (id TEXT PRIMARY KEY, name TEXT NOT NULL)`},
	})
	if err != nil {
		t.Fatalf("run DDL: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run DDL: want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	collections := doGetNoRedirect(t, owner, baseURL+"/admin/ui/data")
	if !strings.Contains(bodyString(t, collections), `href="/admin/ui/data/gadgets"`) {
		t.Fatalf("want gadgets in the collection list right after a DDL run through SQL Studio")
	}
}

func TestAdminUI_SQLStudioErrorShownInline(t *testing.T) {
	// ADMINUI-19
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	resp, err := owner.PostForm(baseURL+"/admin/ui/sql", url.Values{"query": {"select * from no_such_table"}})
	if err != nil {
		t.Fatalf("run bad query: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run bad query: want 200 (error shown inline, not a 500), got %d", resp.StatusCode)
	}
	if !strings.Contains(bodyString(t, resp), "no such table") {
		t.Fatalf("want the database error message shown, got: %s", bodyString(t, resp))
	}
}

func TestAdminUI_SQLStudioAuditsEveryRun(t *testing.T) {
	// ADMINUI-20
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	// One successful run, one failing run — both should be logged.
	owner.PostForm(baseURL+"/admin/ui/sql", url.Values{"query": {"select 1"}})
	owner.PostForm(baseURL+"/admin/ui/sql", url.Values{"query": {"select * from no_such_table"}})

	resp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/audit-log")
	body := bodyString(t, resp)
	if strings.Count(body, "sql_executed") < 2 {
		t.Fatalf("want at least 2 sql_executed audit entries, got: %s", body)
	}
}

func TestAdminUI_AuditLogPageRendersMetadata(t *testing.T) {
	// ADMINUI-37
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	// sql_executed: the query text is the metadata.
	const distinctiveQuery = "select 1 as adminui37marker"
	owner.PostForm(baseURL+"/admin/ui/sql", url.Values{"query": {distinctiveQuery}})

	// owner_create: the role is the metadata.
	createOwner(t, owner, baseURL, "adminui37-viewer@example.com", "viewerpassword1", "viewer")

	body := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/audit-log"))
	if !strings.Contains(body, distinctiveQuery) {
		t.Fatalf("want the executed SQL text visible on the audit log page, got: %s", body)
	}
	if !strings.Contains(body, "role: viewer") {
		t.Fatalf("want the created account's role visible on the audit log page, got: %s", body)
	}
}

// TestAdminUI_AuditWriteFailureIsLoggedNotSilentlyDropped: audit.Log.
// RecordLogged (what every owner-plane handler now calls, instead of the
// old `_ = s.audit.Record(...)` that silently discarded a write failure —
// contradicting Record's own documented "never silently drops an entry"
// contract) must (a) still let the request that triggered it succeed, and
// (b) surface the failure somewhere an operator can see it, rather than
// losing it entirely. Dropping the audit table itself via SQL Studio is
// the most direct way to make every subsequent audit write fail.
func TestAdminUI_AuditWriteFailureIsLoggedNotSilentlyDropped(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	dropResp, err := owner.PostForm(baseURL+"/admin/ui/sql", url.Values{"query": {"DROP TABLE owner_audit_log"}})
	if err != nil {
		t.Fatalf("drop audit table: %v", err)
	}
	// The DROP itself is the very last statement that could still be
	// audited (it ran before the table it's about to remove is gone) —
	// not asserted either way here, just draining the response.
	dropResp.Body.Close()

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// Logout calls audit.RecordLogged(EventLogout, ...) — with the table
	// gone, that write now fails.
	logoutResp, err := owner.Post(baseURL+"/admin/ui/logout", "", nil)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if logoutResp.StatusCode != http.StatusSeeOther && logoutResp.StatusCode != http.StatusOK {
		t.Fatalf("logout after audit table was dropped: want success despite the audit write failing, got %d: %s", logoutResp.StatusCode, bodyString(t, logoutResp))
	}
	if !strings.Contains(logBuf.String(), "audit: record") {
		t.Fatalf("want the failed audit write logged (not silently dropped), got log output: %q", logBuf.String())
	}
}

// --- users (customer-plane accounts) --------------------------------------

func TestAdminUI_UsersPageListsEveryAccount(t *testing.T) {
	// ADMINUI-25
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	signUp(t, newClient(t), baseURL, "adminui-user1@example.com", "userpassword1")
	signUp(t, newClient(t), baseURL, "adminui-user2@example.com", "userpassword1")

	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	resp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/users")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("users page: want 200, got %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "adminui-user1@example.com") || !strings.Contains(body, "adminui-user2@example.com") {
		t.Fatalf("want both customer accounts listed, got: %s", body)
	}
}

func TestAdminUI_UserDetailShowsProfileAndSessionCount(t *testing.T) {
	// ADMINUI-26
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	customer := newClient(t)
	signUp(t, customer, baseURL, "adminui-detail@example.com", "userpassword1")
	meResp, err := customer.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	me := decodeJSONMap(t, meResp)
	id, _ := me["id"].(string)

	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	resp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/users/"+id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("user detail: want 200, got %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "adminui-detail@example.com") {
		t.Fatalf("want the user's email shown, got: %s", body)
	}
	if !strings.Contains(body, "Active sessions") {
		t.Fatalf("want an active-session count shown, got: %s", body)
	}
}

func TestAdminUI_DeveloperCanCreateUserViewerCannot(t *testing.T) {
	// ADMINUI-27
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-dev@example.com", "devpassword1", "developer")
	createOwner(t, owner, baseURL, "adminui-viewer2@example.com", "viewerpassword1", "viewer")

	dev := newClient(t)
	adminUILogin(t, dev, baseURL, "adminui-dev@example.com", "devpassword1")
	resp, err := dev.PostForm(baseURL+"/admin/ui/users", url.Values{
		"email": {"adminui-created@example.com"}, "password": {"createdpassword1"},
	})
	if err != nil {
		t.Fatalf("POST /admin/ui/users: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("developer create user: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	// The admin doing the creating must not be signed in as the new user.
	for _, c := range resp.Cookies() {
		if c.Name == "maubase_session" {
			t.Fatalf("want no customer-plane session cookie set for the admin, got one")
		}
	}
	list := doGetNoRedirect(t, dev, baseURL+"/admin/ui/users")
	if !strings.Contains(bodyString(t, list), "adminui-created@example.com") {
		t.Fatalf("want the created user listed afterward")
	}
	// The created account can sign in normally.
	login := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "adminui-created@example.com", "password": "createdpassword1",
	})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("sign in as admin-created user: want 200, got %d", login.StatusCode)
	}

	viewer := newClient(t)
	adminUILogin(t, viewer, baseURL, "adminui-viewer2@example.com", "viewerpassword1")
	blocked, err := viewer.PostForm(baseURL+"/admin/ui/users", url.Values{
		"email": {"adminui-blocked-user@example.com"}, "password": {"blockedpassword1"},
	})
	if err != nil {
		t.Fatalf("POST /admin/ui/users (viewer): %v", err)
	}
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create user: want 403, got %d", blocked.StatusCode)
	}
}

func TestAdminUI_DeveloperCanForceDeleteUserViewerCannot(t *testing.T) {
	// ADMINUI-28
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-dev2@example.com", "devpassword1", "developer")
	createOwner(t, owner, baseURL, "adminui-viewer3@example.com", "viewerpassword1", "viewer")

	customer := newClient(t)
	signUp(t, customer, baseURL, "adminui-todelete@example.com", "userpassword1")
	meResp, err := customer.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	me := decodeJSONMap(t, meResp)
	id, _ := me["id"].(string)

	viewer := newClient(t)
	adminUILogin(t, viewer, baseURL, "adminui-viewer3@example.com", "viewerpassword1")
	blocked, err := viewer.PostForm(baseURL+"/admin/ui/users/"+id+"/delete", nil)
	if err != nil {
		t.Fatalf("delete user (viewer): %v", err)
	}
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer force-delete: want 403, got %d", blocked.StatusCode)
	}

	dev := newClient(t)
	adminUILogin(t, dev, baseURL, "adminui-dev2@example.com", "devpassword1")
	resp, err := dev.PostForm(baseURL+"/admin/ui/users/"+id+"/delete", nil)
	if err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("developer force-delete: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	list := doGetNoRedirect(t, dev, baseURL+"/admin/ui/users")
	if strings.Contains(bodyString(t, list), "adminui-todelete@example.com") {
		t.Fatalf("want deleted user gone from the list")
	}
	// The deleted account's old credentials no longer work, and its
	// session is gone (IDNT-10, reproduced by the admin-initiated path).
	login := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "adminui-todelete@example.com", "password": "userpassword1",
	})
	if login.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sign in as deleted user: want 401, got %d", login.StatusCode)
	}
	me2, err := customer.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if me2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deleted user's old session: want 401, got %d", me2.StatusCode)
	}
}

func TestAdminUI_DeveloperCanRevokeUserSessionsWithoutDeleting(t *testing.T) {
	// ADMINUI-29
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-dev3@example.com", "devpassword1", "developer")

	customer := newClient(t)
	signUp(t, customer, baseURL, "adminui-revoke@example.com", "userpassword1")
	meResp, err := customer.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	me := decodeJSONMap(t, meResp)
	id, _ := me["id"].(string)

	dev := newClient(t)
	adminUILogin(t, dev, baseURL, "adminui-dev3@example.com", "devpassword1")
	resp, err := dev.PostForm(baseURL+"/admin/ui/users/"+id+"/revoke-sessions", nil)
	if err != nil {
		t.Fatalf("revoke sessions: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke sessions: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	// The old session is dead...
	me2, err := customer.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	if me2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session: want 401, got %d", me2.StatusCode)
	}
	// ...but the account itself still exists and can sign in again.
	login := postJSON(t, newClient(t), baseURL+"/api/auth/login", map[string]string{
		"email": "adminui-revoke@example.com", "password": "userpassword1",
	})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("sign in after revoke: want 200, got %d", login.StatusCode)
	}
}

func TestAdminUI_UserActionsAreAudited(t *testing.T) {
	// ADMINUI-30
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	owner.PostForm(baseURL+"/admin/ui/users", url.Values{
		"email": {"adminui-audited@example.com"}, "password": {"auditedpassword1"},
	})
	customer := newClient(t)
	loginResp := postJSON(t, customer, baseURL+"/api/auth/login", map[string]string{
		"email": "adminui-audited@example.com", "password": "auditedpassword1",
	})
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("sign in as created user: want 200, got %d", loginResp.StatusCode)
	}
	meResp, err := customer.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	me := decodeJSONMap(t, meResp)
	id, _ := me["id"].(string)

	owner.PostForm(baseURL+"/admin/ui/users/"+id+"/revoke-sessions", nil)
	owner.PostForm(baseURL+"/admin/ui/users/"+id+"/delete", nil)

	resp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/audit-log")
	body := bodyString(t, resp)
	for _, event := range []string{"user_create", "user_sessions_revoked", "user_delete"} {
		if !strings.Contains(body, event) {
			t.Fatalf("want %q in audit log, got: %s", event, body)
		}
	}
}

// --- sidebar link visibility ------------------------------------------------

func TestAdminUI_SidebarHidesLinksAboveRole(t *testing.T) {
	// ADMINUI-31
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-nav-viewer@example.com", "viewerpassword1", "viewer")
	createOwner(t, owner, baseURL, "adminui-nav-dev@example.com", "devpassword1", "developer")
	createOwner(t, owner, baseURL, "adminui-nav-admin@example.com", "adminpassword1", "admin")

	adminPlaneLinks := []string{`href="/admin/ui/owners"`, `href="/admin/ui/audit-log"`, `href="/admin/ui/maintenance"`}
	sqlLink := `href="/admin/ui/sql"`

	cases := []struct {
		role, email, password  string
		wantLinks, wantNoLinks []string
	}{
		{"viewer", "adminui-nav-viewer@example.com", "viewerpassword1",
			[]string{`href="/admin/ui/data"`, `href="/admin/ui/users"`},
			append(append([]string{}, adminPlaneLinks...), sqlLink)},
		{"developer", "adminui-nav-dev@example.com", "devpassword1",
			[]string{`href="/admin/ui/data"`, `href="/admin/ui/users"`},
			append(append([]string{}, adminPlaneLinks...), sqlLink)},
		{"admin", "adminui-nav-admin@example.com", "adminpassword1",
			adminPlaneLinks,
			[]string{sqlLink}},
		{"owner", bootstrapEmail, bootstrapPassword,
			append(append([]string{}, adminPlaneLinks...), sqlLink),
			nil},
	}

	for _, c := range cases {
		client := newClient(t)
		adminUILogin(t, client, baseURL, c.email, c.password)
		resp := doGetNoRedirect(t, client, baseURL+"/admin/ui")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s dashboard: want 200, got %d", c.role, resp.StatusCode)
		}
		body := bodyString(t, resp)
		for _, want := range c.wantLinks {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: want sidebar to link %s, got: %s", c.role, want, body)
			}
		}
		for _, notWant := range c.wantNoLinks {
			if strings.Contains(body, notWant) {
				t.Fatalf("%s: want sidebar to NOT link %s, got: %s", c.role, notWant, body)
			}
		}
	}

	// Hiding the link is a navigation nicety, not a looser authorization
	// boundary: GETting the route directly must still 403 (ADMINUI-05).
	viewer := newClient(t)
	adminUILogin(t, viewer, baseURL, "adminui-nav-viewer@example.com", "viewerpassword1")
	direct := doGetNoRedirect(t, viewer, baseURL+"/admin/ui/owners")
	if direct.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer GET /admin/ui/owners directly: want 403, got %d", direct.StatusCode)
	}
}

func TestAdminUI_HiddenRoutesReject403ForEveryUnderTierRole(t *testing.T) {
	// ADMINUI-36
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "adminui-hidden-viewer@example.com", "viewerpassword1", "viewer")
	createOwner(t, owner, baseURL, "adminui-hidden-dev@example.com", "devpassword1", "developer")
	createOwner(t, owner, baseURL, "adminui-hidden-admin@example.com", "adminpassword1", "admin")
	// A throwaway target for the owner-delete action — this route's role
	// check runs before the handler ever looks up the id, so a
	// nonexistent one still proves the 403 boundary.
	bogusID := "00000000-0000-0000-0000-000000000000"

	type roleAccount struct{ role, email, password string }
	viewer := roleAccount{"viewer", "adminui-hidden-viewer@example.com", "viewerpassword1"}
	developer := roleAccount{"developer", "adminui-hidden-dev@example.com", "devpassword1"}
	admin := roleAccount{"admin", "adminui-hidden-admin@example.com", "adminpassword1"}

	type routeCase struct {
		name        string
		method      string
		path        string
		underTier   []roleAccount // every role below this route's minimum
		formForPost url.Values
	}
	cases := []routeCase{
		{"owners page", http.MethodGet, "/admin/ui/owners", []roleAccount{viewer, developer}, nil},
		{"audit log page", http.MethodGet, "/admin/ui/audit-log", []roleAccount{viewer, developer}, nil},
		{"maintenance page", http.MethodGet, "/admin/ui/maintenance", []roleAccount{viewer, developer}, nil},
		{"purge sessions", http.MethodPost, "/admin/ui/maintenance/purge-sessions", []roleAccount{viewer, developer}, url.Values{}},
		{"create owner", http.MethodPost, "/admin/ui/owners", []roleAccount{viewer, developer, admin},
			url.Values{"email": {"whoever@example.com"}, "password": {"correcthorse"}, "role": {"viewer"}}},
		{"delete owner", http.MethodPost, "/admin/ui/owners/" + bogusID + "/delete", []roleAccount{viewer, developer, admin}, url.Values{}},
		{"sql studio page", http.MethodGet, "/admin/ui/sql", []roleAccount{viewer, developer, admin}, nil},
		{"sql studio run", http.MethodPost, "/admin/ui/sql", []roleAccount{viewer, developer, admin}, url.Values{"query": {"select 1"}}},
	}

	for _, c := range cases {
		for _, acct := range c.underTier {
			client := newClient(t)
			adminUILogin(t, client, baseURL, acct.email, acct.password)

			var resp *http.Response
			var err error
			switch c.method {
			case http.MethodGet:
				resp = doGetNoRedirect(t, client, baseURL+c.path)
			case http.MethodPost:
				resp, err = client.PostForm(baseURL+c.path, c.formForPost)
				if err != nil {
					t.Fatalf("%s %s as %s: %v", c.method, c.path, acct.role, err)
				}
			default:
				t.Fatalf("unsupported method %s", c.method)
			}
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s (%s) as %s: want 403, got %d: %s", c.method, c.path, c.name, acct.role, resp.StatusCode, bodyString(t, resp))
			}
		}
	}
}

// --- data browser: inline editing, NULL display, sorting -------------------

func TestAdminUI_InlineEditReturnsRowFragmentNotRedirect(t *testing.T) {
	// ADMINUI-32
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	createResp, err := owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{
		"title": {"original"}, "body": {"x"}, "owner_id": {"user-a"},
	})
	if err != nil {
		t.Fatalf("create row: %v", err)
	}
	rowsBody := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes"))
	id := idFromRowHTML(t, rowsBody, "original")
	_ = createResp

	// The edit-row fragment (what htmx's "Edit" swaps in) is just the
	// <tr>, not a full page.
	editRowResp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes/"+id+"/edit-row")
	if editRowResp.StatusCode != http.StatusOK {
		t.Fatalf("GET edit-row: want 200, got %d", editRowResp.StatusCode)
	}
	editRowBody := bodyString(t, editRowResp)
	if strings.Contains(editRowBody, "<html") {
		t.Fatalf("want a bare <tr> fragment from edit-row, got a full page: %s", editRowBody)
	}
	if !strings.Contains(editRowBody, `name="title"`) {
		t.Fatalf("want an editable title input in the fragment, got: %s", editRowBody)
	}

	// An htmx POST (HX-Request header set) gets the updated row's <tr>
	// back, not a redirect.
	req, err := http.NewRequest(http.MethodPost, baseURL+"/admin/ui/data/notes/"+id,
		strings.NewReader(url.Values{"title": {"edited"}, "body": {"x"}, "owner_id": {"user-a"}}.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := owner.Do(req)
	if err != nil {
		t.Fatalf("htmx update: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("htmx update: want 200 (row fragment), got %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if strings.Contains(body, "<html") {
		t.Fatalf("want a bare <tr> fragment back, got a full page: %s", body)
	}
	if !strings.Contains(body, "edited") {
		t.Fatalf("want the updated value in the returned fragment, got: %s", body)
	}

	// A non-htmx POST to the same route (the full-page form) still
	// redirects, per ADMINUI-13's existing behavior.
	plain, err := owner.PostForm(baseURL+"/admin/ui/data/notes/"+id, url.Values{
		"title": {"edited again"}, "body": {"x"}, "owner_id": {"user-a"},
	})
	if err != nil {
		t.Fatalf("plain update: %v", err)
	}
	if plain.StatusCode != http.StatusSeeOther {
		t.Fatalf("plain update: want 303, got %d", plain.StatusCode)
	}
}

func TestAdminUI_NullColumnShownDistinctlyFromEmptyString(t *testing.T) {
	// ADMINUI-32
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	// body is nullable and left out of the form entirely, so it's
	// inserted as SQL NULL, not "".
	if _, err := owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{
		"title": {"no body"}, "owner_id": {"user-a"},
	}); err != nil {
		t.Fatalf("create row: %v", err)
	}

	body := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes"))
	if !strings.Contains(body, `class="null-cell"`) {
		t.Fatalf("want a NULL body column rendered with a distinct marker, got: %s", body)
	}
}

func TestAdminUI_EditFormCanSetAndShowsNULL(t *testing.T) {
	// ADMINUI-38
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	if _, err := owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{
		"title": {"has a body"}, "body": {"real value"}, "owner_id": {"user-a"},
	}); err != nil {
		t.Fatalf("create row: %v", err)
	}
	rowsAfterCreate := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes"))
	id := idFromRowHTML(t, rowsAfterCreate, "has a body")

	// The edit form shows the current value and an unchecked NULL box.
	editPage := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes/"+id+"/edit"))
	if !strings.Contains(editPage, `value="real value"`) {
		t.Fatalf("want the current value pre-filled, got: %s", editPage)
	}
	if !strings.Contains(editPage, `name="body__null" value="1">`) {
		t.Fatalf("want an (unchecked) NULL checkbox for the nullable body field, got: %s", editPage)
	}
	if strings.Contains(editPage, `name="body__null" value="1" checked`) {
		t.Fatalf("want the NULL checkbox unchecked for a non-NULL field, got: %s", editPage)
	}

	// Checking it (regardless of the text field) sets the column to NULL.
	updateResp, err := owner.PostForm(baseURL+"/admin/ui/data/notes/"+id, url.Values{
		"title": {"has a body"}, "body": {"ignored because __null is set"}, "body__null": {"1"},
	})
	if err != nil {
		t.Fatalf("update row: %v", err)
	}
	if updateResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("update: want 303, got %d: %s", updateResp.StatusCode, bodyString(t, updateResp))
	}

	rowsBody := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes"))
	if !strings.Contains(rowsBody, `class="null-cell"`) {
		t.Fatalf("want the field now shown as NULL in the data browser, got: %s", rowsBody)
	}

	// Re-opening the edit form now shows the checkbox pre-checked.
	editAgain := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes/"+id+"/edit"))
	if !strings.Contains(editAgain, `name="body__null" value="1" checked`) {
		t.Fatalf("want the NULL checkbox pre-checked for an already-NULL field, got: %s", editAgain)
	}
}

func TestAdminUI_SortsRowsByColumn(t *testing.T) {
	// ADMINUI-33
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	for _, title := range []string{"bravo", "alpha", "charlie"} {
		if _, err := owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{
			"title": {title}, "body": {"x"}, "owner_id": {"user-a"},
		}); err != nil {
			t.Fatalf("create row %q: %v", title, err)
		}
	}

	asc := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes?sort=title&dir=asc"))
	if i, j, k := strings.Index(asc, "alpha"), strings.Index(asc, "bravo"), strings.Index(asc, "charlie"); !(i < j && j < k) {
		t.Fatalf("want alpha < bravo < charlie in ascending order, got positions %d,%d,%d in: %s", i, j, k, asc)
	}

	desc := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes?sort=title&dir=desc"))
	if i, j, k := strings.Index(desc, "charlie"), strings.Index(desc, "bravo"), strings.Index(desc, "alpha"); !(i < j && j < k) {
		t.Fatalf("want charlie < bravo < alpha in descending order, got positions %d,%d,%d in: %s", i, j, k, desc)
	}

	// An unknown sort column is ignored, not an error.
	fallback := doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes?sort=not_a_real_column")
	if fallback.StatusCode != http.StatusOK {
		t.Fatalf("bogus sort column: want 200 (falls back to default order), got %d", fallback.StatusCode)
	}
}

func TestAdminUI_UsersPagePaginationBoundary(t *testing.T) {
	// ADMINUI-40
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	const total = 51 // defaultLimit (50) + 1, so exactly one row must land on page 2.
	emails := make([]string, total)
	for i := 0; i < total; i++ {
		emails[i] = fmt.Sprintf("boundary-user-%02d@example.com", i)
		signUp(t, newClient(t), baseURL, emails[i], "correcthorse")
	}

	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	page1 := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/users"))
	page2 := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/users?offset=50"))

	for _, email := range emails {
		onPage1, onPage2 := strings.Contains(page1, email), strings.Contains(page2, email)
		if onPage1 == onPage2 {
			t.Fatalf("want %q on exactly one page, got page1=%v page2=%v", email, onPage1, onPage2)
		}
	}
}

func TestAdminUI_DataBrowserPaginationBoundary(t *testing.T) {
	// ADMINUI-40
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	const total = 51 // defaultLimit (50) + 1
	titles := make([]string, total)
	for i := 0; i < total; i++ {
		titles[i] = fmt.Sprintf("boundary-row-%02d", i)
		if _, err := owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{
			"title": {titles[i]}, "body": {"x"}, "owner_id": {"user-a"},
		}); err != nil {
			t.Fatalf("create row %q: %v", titles[i], err)
		}
	}

	page1 := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes"))
	page2 := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes?offset=50"))

	for _, title := range titles {
		onPage1, onPage2 := strings.Contains(page1, title), strings.Contains(page2, title)
		if onPage1 == onPage2 {
			t.Fatalf("want %q on exactly one page, got page1=%v page2=%v", title, onPage1, onPage2)
		}
	}
}

func TestAdminUI_DataBrowserPaginationBoundaryWithTiedSortColumn(t *testing.T) {
	// ADMINUI-40
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	// Every row shares the same owner_id, so sorting by it (a real but
	// non-unique column) puts all 51 rows in one tied group — the case
	// AdminListRows's primary-key tie-break exists for.
	const total = 51
	titles := make([]string, total)
	for i := 0; i < total; i++ {
		titles[i] = fmt.Sprintf("tied-row-%02d", i)
		if _, err := owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{
			"title": {titles[i]}, "body": {"x"}, "owner_id": {"same-owner-for-all"},
		}); err != nil {
			t.Fatalf("create row %q: %v", titles[i], err)
		}
	}

	page1 := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes?sort=owner_id"))
	page2 := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes?sort=owner_id&offset=50"))

	for _, title := range titles {
		onPage1, onPage2 := strings.Contains(page1, title), strings.Contains(page2, title)
		if onPage1 == onPage2 {
			t.Fatalf("want %q on exactly one page when sorted by a tied column, got page1=%v page2=%v", title, onPage1, onPage2)
		}
	}
}

func TestAdminUI_OwnerSessionCookieHasDefensiveAttributes(t *testing.T) {
	// ADMINUI-34
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	resp := adminUILogin(t, newClient(t), baseURL, bootstrapEmail, bootstrapPassword)

	var found *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "maubase_owner_session" {
			found = c
		}
	}
	if found == nil {
		t.Fatalf("want the owner session cookie set, got %v", resp.Cookies())
	}
	if found.SameSite != http.SameSiteLaxMode {
		t.Fatalf("want SameSite=Lax, got %v", found.SameSite)
	}
	if !found.HttpOnly {
		t.Fatalf("want HttpOnly, got false")
	}
	if !found.Secure {
		t.Fatalf("want Secure, got false")
	}
}

func TestAdminUI_HostileMarkupIsEscapedNotExecuted(t *testing.T) {
	// ADMINUI-35
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	// A customer account whose email carries markup, shown on the users
	// list.
	const hostileEmail = `<script>window.__xss=1</script>@example.com`
	signUp(t, newClient(t), baseURL, hostileEmail, "correcthorse")

	usersBody := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/users"))
	if strings.Contains(usersBody, "<script>window.__xss") {
		t.Fatalf("want the hostile email HTML-escaped, got raw markup in users page: %s", usersBody)
	}
	if !strings.Contains(usersBody, "&lt;script&gt;") {
		t.Fatalf("want the escaped form of the hostile email present, got: %s", usersBody)
	}

	// A data-browser row whose value carries markup.
	hostileToken := restToken(t, baseURL, "hostile-row-owner@example.com", []string{"records:read", "records:write"})
	const hostileTitle = `<img src=x onerror="window.__xss=1">`
	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", hostileToken, map[string]any{"title": hostileTitle, "body": "x"})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create hostile note: want 201, got %d: %s", createResp.StatusCode, bodyString(t, createResp))
	}

	rowsBody := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes"))
	if strings.Contains(rowsBody, `<img src=x onerror=`) {
		t.Fatalf("want the hostile row value HTML-escaped, got raw markup in data browser: %s", rowsBody)
	}
	if !strings.Contains(rowsBody, "&lt;img") {
		t.Fatalf("want the escaped form of the hostile row value present, got: %s", rowsBody)
	}

	// A very long field renders without erroring or losing its action
	// buttons — same page, not a separate one, since it's the same
	// rendering path under a different kind of stress.
	longEmail := strings.Repeat("a", 240) + "@example.com"
	signUp(t, newClient(t), baseURL, longEmail, "correcthorse")
	longUsersResp := doGetNoRedirect(t, owner, baseURL+"/admin/ui/users")
	if longUsersResp.StatusCode != http.StatusOK {
		t.Fatalf("users page with a long email present: want 200, got %d", longUsersResp.StatusCode)
	}
	longUsersBody := bodyString(t, longUsersResp)
	if !strings.Contains(longUsersBody, longEmail) {
		t.Fatalf("want the long email rendered somewhere on the page")
	}
	if !strings.Contains(longUsersBody, "Create") && !strings.Contains(longUsersBody, "/admin/ui") {
		t.Fatalf("want the page's normal chrome (nav/actions) still present alongside a long value, got: %s", longUsersBody)
	}
}

// idFromRowHTML extracts the id chi assigned a newly created row from its
// id="row-{id}" attribute in a rendered data_rows page, by finding the
// <tr> whose text contains marker.
func idFromRowHTML(t *testing.T, html, marker string) string {
	t.Helper()
	idx := strings.Index(html, marker)
	if idx < 0 {
		t.Fatalf("marker %q not found in: %s", marker, html)
	}
	rowStart := strings.LastIndex(html[:idx], `id="row-`)
	if rowStart < 0 {
		t.Fatalf("no id=\"row-...\" before marker %q in: %s", marker, html)
	}
	rest := html[rowStart+len(`id="row-`):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		t.Fatalf("malformed row id near marker %q in: %s", marker, html)
	}
	return rest[:end]
}
