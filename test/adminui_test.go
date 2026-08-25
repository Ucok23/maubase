package e2e_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/admin-ui.md (ADMINUI-01..15)

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
