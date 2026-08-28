package e2e_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/owner-plane.md, "Audit log" section (OWNR-11..17)

func auditLog(t *testing.T, client *http.Client, baseURL string) []map[string]any {
	t.Helper()
	resp, err := client.Get(baseURL + "/admin/audit-log")
	if err != nil {
		t.Fatalf("GET /admin/audit-log: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/audit-log: want 200, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	defer resp.Body.Close()
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// findAuditEntry returns the newest entry matching event (and, if
// nonempty, targetEmail), since List is newest-first and a test may
// trigger more than one entry of the same kind.
func findAuditEntry(entries []map[string]any, event, targetEmail string) map[string]any {
	for _, e := range entries {
		if e["event"] != event {
			continue
		}
		if targetEmail != "" && e["target_email"] != targetEmail {
			continue
		}
		return e
	}
	return nil
}

func TestOwnerAudit_SuccessfulLoginRecorded(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	// OWNR-11
	entries := auditLog(t, owner, baseURL)
	e := findAuditEntry(entries, "login", "")
	if e == nil {
		t.Fatalf("want a login entry in the audit log, got %v", entries)
	}
	if e["actor_email"] != bootstrapEmail {
		t.Fatalf("want the logged-in account as actor, got %v", e)
	}
}

func TestOwnerAudit_FailedLoginRecorded(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)

	// Wrong password for a real account.
	anon := newClient(t)
	ownerLogin(t, anon, baseURL, bootstrapEmail, "not-the-password")
	// An email that was never registered at all.
	ownerLogin(t, anon, baseURL, "never-existed@example.com", "whatever12345")

	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	// OWNR-12
	entries := auditLog(t, owner, baseURL)
	wrongPassword := findAuditEntry(entries, "login_failed", bootstrapEmail)
	if wrongPassword == nil {
		t.Fatalf("want a login_failed entry for a wrong password, got %v", entries)
	}
	unknownEmail := findAuditEntry(entries, "login_failed", "never-existed@example.com")
	if unknownEmail == nil {
		t.Fatalf("want a login_failed entry even for an unknown email, got %v", entries)
	}
}

func TestOwnerAudit_LogoutRecorded(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	resp, err := owner.Post(baseURL+"/admin/auth/logout", "", nil)
	if err != nil {
		t.Fatalf("POST /admin/auth/logout: %v", err)
	}
	resp.Body.Close()

	// Log back in to read the audit log (the previous session was just revoked).
	owner2 := newClient(t)
	ownerLogin(t, owner2, baseURL, bootstrapEmail, bootstrapPassword)

	// OWNR-13
	entries := auditLog(t, owner2, baseURL)
	e := findAuditEntry(entries, "logout", "")
	if e == nil {
		t.Fatalf("want a logout entry in the audit log, got %v", entries)
	}
	if e["actor_email"] != bootstrapEmail {
		t.Fatalf("want the logged-out account as actor, got %v", e)
	}
}

func TestOwnerAudit_CreateAndDeleteRecorded(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	created := createOwner(t, owner, baseURL, "audited-dev@example.com", "correcthorse", "developer")
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("setup: create owner failed: %d", created.StatusCode)
	}
	createdBody := decodeJSONMap(t, created)
	createdID, _ := createdBody["id"].(string)

	// OWNR-14
	afterCreate := auditLog(t, owner, baseURL)
	createEntry := findAuditEntry(afterCreate, "owner_create", "audited-dev@example.com")
	if createEntry == nil {
		t.Fatalf("want an owner_create entry, got %v", afterCreate)
	}
	if createEntry["actor_email"] != bootstrapEmail {
		t.Fatalf("want the creator as actor, got %v", createEntry)
	}
	meta, _ := createEntry["metadata"].(map[string]any)
	if meta["role"] != "developer" {
		t.Fatalf("want the assigned role in metadata, got %v", createEntry)
	}

	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/admin/owners/"+createdID, nil)
	del, err := owner.Do(req)
	if err != nil {
		t.Fatalf("DELETE /admin/owners/%s: %v", createdID, err)
	}
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("setup: delete owner failed: %d", del.StatusCode)
	}

	// OWNR-15
	afterDelete := auditLog(t, owner, baseURL)
	deleteEntry := findAuditEntry(afterDelete, "owner_delete", "audited-dev@example.com")
	if deleteEntry == nil {
		t.Fatalf("want an owner_delete entry, got %v", afterDelete)
	}
	if deleteEntry["actor_email"] != bootstrapEmail {
		t.Fatalf("want the deleter as actor, got %v", deleteEntry)
	}
	delMeta, _ := deleteEntry["metadata"].(map[string]any)
	if delMeta["role"] != "developer" {
		t.Fatalf("want the removed account's role in metadata, got %v", deleteEntry)
	}

	// OWNR-17: the earlier owner_create entry must be untouched, still
	// naming the now-deleted account's email.
	stillThere := findAuditEntry(afterDelete, "owner_create", "audited-dev@example.com")
	if stillThere == nil {
		t.Fatalf("want the owner_create entry to survive the account's deletion, got %v", afterDelete)
	}
}

func TestOwnerAudit_ReadingRequiresAtLeastAdmin(t *testing.T) {
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	createOwner(t, owner, baseURL, "audit-viewer@example.com", "correcthorse", "viewer")

	// OWNR-16, part 1: owner (and by extension admin) can read.
	resp, err := owner.Get(baseURL + "/admin/audit-log")
	if err != nil {
		t.Fatalf("GET /admin/audit-log: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 for owner reading the audit log, got %d", resp.StatusCode)
	}

	// OWNR-16, part 2: viewer cannot.
	viewer := newClient(t)
	ownerLogin(t, viewer, baseURL, "audit-viewer@example.com", "correcthorse")
	denied, err := viewer.Get(baseURL + "/admin/audit-log")
	if err != nil {
		t.Fatalf("GET /admin/audit-log: %v", err)
	}
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for viewer reading the audit log, got %d: %s", denied.StatusCode, bodyString(t, denied))
	}
}

func TestOwnerAudit_OutOfRangePaginationIsRejected(t *testing.T) {
	// OWNR-21
	baseURL := testserver.NewWithOwner(t, bootstrapEmail, bootstrapPassword)
	owner := newClient(t)
	ownerLogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	for _, query := range []string{"?limit=999999", "?limit=0", "?limit=-1", "?limit=abc", "?offset=-1", "?offset=abc"} {
		resp, err := owner.Get(baseURL + "/admin/audit-log" + query)
		if err != nil {
			t.Fatalf("GET /admin/audit-log%s: %v", query, err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET /admin/audit-log%s: want 400, got %d: %s", query, resp.StatusCode, bodyString(t, resp))
		}
	}

	// Omitted entirely still works, defaulting exactly as before.
	ok, err := owner.Get(baseURL + "/admin/audit-log")
	if err != nil {
		t.Fatalf("GET /admin/audit-log: %v", err)
	}
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("want 200 with no pagination params, got %d", ok.StatusCode)
	}
}
