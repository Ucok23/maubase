package e2e_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"maubase/internal/testserver"
)

// Scenarios: spec/access-rules.md (ACCESS-01..08)

// policyRow builds an INSERT for the _policies table, the way a
// deployment's own migrations would declare an access-rule override —
// see spec/access-rules.md's "The model" section.
func policyRow(collection, operation, rule string) string {
	return fmt.Sprintf(`INSERT INTO _policies (collection, operation, rule) VALUES ('%s', '%s', '%s')`, collection, operation, rule)
}

func TestAccessRules_NoPolicyPreservesDefaults(t *testing.T) {
	// ACCESS-01
	baseURL := testserver.NewWithSchema(t, notesSchema, tagsSchema)
	tokenA := restToken(t, baseURL, "ar-default-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "ar-default-b@example.com", []string{"records:read", "records:write"})

	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenA, map[string]any{"title": "mine", "body": "x"})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d", createResp.StatusCode)
	}
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)

	// owner-scoped table: still invisible to another user by default.
	getResp := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, tokenB, nil)
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("other user get: want 404, got %d", getResp.StatusCode)
	}

	// shared table: still visible to everyone by default.
	tagResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/tags", tokenA, map[string]any{"name": "x"})
	if tagResp.StatusCode != http.StatusCreated {
		t.Fatalf("create tag: want 201, got %d", tagResp.StatusCode)
	}
	tag := decodeJSONMap(t, tagResp)
	tagGet := doAuthed(t, http.MethodGet, baseURL+"/api/data/tags/"+tag["id"].(string), tokenB, nil)
	if tagGet.StatusCode != http.StatusOK {
		t.Fatalf("other user get shared tag: want 200, got %d", tagGet.StatusCode)
	}
}

func TestAccessRules_PublicReadOwnerWrite(t *testing.T) {
	// ACCESS-02, ACCESS-03
	baseURL := testserver.NewWithSchema(t, notesSchema, policyRow("notes", "read", "shared"))
	tokenA := restToken(t, baseURL, "ar-pubread-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "ar-pubread-b@example.com", []string{"records:read", "records:write"})

	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenA, map[string]any{"title": "a's note", "body": "x"})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d", createResp.StatusCode)
	}
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)

	// read:shared -> B can list and get A's note.
	listResp := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes", tokenB, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("B list notes: want 200, got %d", listResp.StatusCode)
	}
	listBody := decodeJSONMap(t, listResp)
	records, _ := listBody["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("want A's note visible to B, got %v", listBody)
	}

	getResp := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, tokenB, nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("B get A's note: want 200, got %d", getResp.StatusCode)
	}

	// write ops stay at their owner default, unaffected by the read
	// override (ACCESS-02's "operations are independent").
	patchResp := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+id, tokenB, map[string]any{"title": "hijacked"})
	if patchResp.StatusCode != http.StatusNotFound {
		t.Fatalf("B patch A's note: want 404, got %d", patchResp.StatusCode)
	}
	delResp := doAuthed(t, http.MethodDelete, baseURL+"/api/data/notes/"+id, tokenB, nil)
	if delResp.StatusCode != http.StatusNotFound {
		t.Fatalf("B delete A's note: want 404, got %d", delResp.StatusCode)
	}
}

func TestAccessRules_SharedCreateStillStampsOwner(t *testing.T) {
	// ACCESS-04
	baseURL := testserver.NewWithSchema(t, notesSchema, policyRow("notes", "create", "shared"))
	tokenA := restToken(t, baseURL, "ar-sharedcreate-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "ar-sharedcreate-b@example.com", []string{"records:read", "records:write"})

	resp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenA, map[string]any{
		"title": "x", "body": "x", "owner_id": "someone-else",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	created := decodeJSONMap(t, resp)
	if created["owner_id"] == "someone-else" {
		t.Fatalf("want owner_id ignored (set to A's own subject), got %v", created["owner_id"])
	}
	id, _ := created["id"].(string)

	// create:shared didn't relax read (still owner-default): only A can
	// see the row it just made, confirming it's really owned by A.
	aGet := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, tokenA, nil)
	if aGet.StatusCode != http.StatusOK {
		t.Fatalf("A get own note: want 200, got %d", aGet.StatusCode)
	}
	bGet := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, tokenB, nil)
	if bGet.StatusCode != http.StatusNotFound {
		t.Fatalf("B get A's note: want 404, got %d", bGet.StatusCode)
	}
}

func TestAccessRules_DeniedDeleteRejectsEveryCaller(t *testing.T) {
	// ACCESS-05
	baseURL := testserver.NewWithSchema(t, notesSchema, policyRow("notes", "delete", "denied"))
	token := restToken(t, baseURL, "ar-deniedelete@example.com", []string{"records:read", "records:write"})

	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "x", "body": "x"})
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)

	delResp := doAuthed(t, http.MethodDelete, baseURL+"/api/data/notes/"+id, token, nil)
	if delResp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete own note under delete:denied: want 403, got %d", delResp.StatusCode)
	}
}

func TestAccessRules_DeniedReadHidesTheCollection(t *testing.T) {
	// ACCESS-06
	baseURL := testserver.NewWithSchema(t, notesSchema, policyRow("notes", "read", "denied"))
	token := restToken(t, baseURL, "ar-deniedread@example.com", []string{"records:read", "records:write"})

	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "x", "body": "x"})
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)

	listResp := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes", token, nil)
	if listResp.StatusCode != http.StatusForbidden {
		t.Fatalf("list under read:denied: want 403, got %d", listResp.StatusCode)
	}
	getResp := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, token, nil)
	if getResp.StatusCode != http.StatusForbidden {
		t.Fatalf("get own note under read:denied: want 403, got %d", getResp.StatusCode)
	}
}

func TestAccessRules_SharedTableCanStillDenyAnOperation(t *testing.T) {
	// ACCESS-07
	baseURL := testserver.NewWithSchema(t, tagsSchema, policyRow("tags", "create", "denied"))
	token := restToken(t, baseURL, "ar-shareddenied@example.com", []string{"records:read", "records:write"})

	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/tags", token, map[string]any{"name": "x"})
	if createResp.StatusCode != http.StatusForbidden {
		t.Fatalf("create under create:denied: want 403, got %d", createResp.StatusCode)
	}

	// read is untouched: still shared, still 200.
	listResp := doAuthed(t, http.MethodGet, baseURL+"/api/data/tags", token, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list tags (read unaffected): want 200, got %d", listResp.StatusCode)
	}
}

func TestAccessRules_SharedWriteRealtimeEventsGatedByRowOwnerNotWriter(t *testing.T) {
	// ACCESS-09: update/delete: shared with read left at its owner
	// default — the realtime event for a write must reach the row's
	// actual owner, not the (different) caller who made the write.
	baseURL := testserver.NewWithSchema(t, notesSchema,
		policyRow("notes", "update", "shared"),
		policyRow("notes", "delete", "shared"))
	tokenA := restToken(t, baseURL, "ar-sharedwrite-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "ar-sharedwrite-b@example.com", []string{"records:read", "records:write"})

	// B owns the row.
	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenB, map[string]any{"title": "b's note", "body": "x"})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("B create note: want 201, got %d: %s", createResp.StatusCode, bodyString(t, createResp))
	}
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)

	rcA := connectRealtime(t, baseURL, tokenA)
	rcB := connectRealtime(t, baseURL, tokenB)
	subscribe(t, rcA, "notes")
	subscribe(t, rcB, "notes")

	// A (not the owner) can write it, since update:shared widens who may.
	updateResp := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+id, tokenA, map[string]any{"title": "hijacked"})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("A update B's note under update:shared: want 200, got %d: %s", updateResp.StatusCode, bodyString(t, updateResp))
	}
	ev := readEvent(t, rcB)
	if ev.Type != "updated" || ev.Record["id"] != id {
		t.Fatalf("want B (the row's owner) to receive the updated event, got %+v", ev)
	}
	expectNoEvent(t, rcA)

	// Same for delete: shared.
	delResp := doAuthed(t, http.MethodDelete, baseURL+"/api/data/notes/"+id, tokenA, nil)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("A delete B's note under delete:shared: want 204, got %d", delResp.StatusCode)
	}
	ev = readEvent(t, rcB)
	if ev.Type != "deleted" || ev.ID != id {
		t.Fatalf("want B (the row's owner) to receive the deleted event, got %+v", ev)
	}
	expectNoEvent(t, rcA)
}

func TestAccessRules_OwnerRuleWithoutOwnerColumnFailsAtStartup(t *testing.T) {
	// ACCESS-08
	err := testserver.NewCustomExpectingDiscoverError(t, testserver.Options{
		Schema: []string{tagsSchema, policyRow("tags", "read", "owner")},
	})
	if err == nil {
		t.Fatalf("want an error, got none")
	}
	msg := err.Error()
	if !strings.Contains(msg, "tags") || !strings.Contains(msg, "owner_id") {
		t.Fatalf("want the error to name the table and the missing column, got: %v", err)
	}
}
