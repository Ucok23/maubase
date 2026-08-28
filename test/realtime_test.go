package e2e_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"maubase/internal/testserver"
)

// Scenarios: spec/realtime.md (RT-01..08).

func wsURL(baseURL string) string {
	return strings.Replace(baseURL, "http://", "ws://", 1) + "/api/realtime"
}

type realtimeEvent struct {
	Type       string         `json:"type"`
	Collection string         `json:"collection"`
	Record     map[string]any `json:"record"`
	ID         string         `json:"id"`
}

// realtimeClient wraps a WebSocket connection with a background goroutine
// that reads continuously and hands events off over a channel. This is
// deliberate: gorilla/websocket permanently marks a connection's read
// side as broken after *any* read error, including a SetReadDeadline
// timeout (see conn.go's readErr, set unconditionally in NextReader) — so
// a "read with a short deadline to check nothing arrived" pattern on the
// raw *websocket.Conn would poison it for every read after. Reading
// continuously in the background and timing out on the Go channel
// instead sidesteps that entirely: the underlying ReadJSON call never
// times out, it just blocks until the next real message.
type realtimeClient struct {
	conn   *websocket.Conn
	events chan realtimeEvent
}

// dialRealtime attempts the handshake and returns the connection (nil on
// failure) alongside the HTTP response status the server answered with —
// callers assert on whichever side of that they care about.
func dialRealtime(t *testing.T, baseURL, token string) (*websocket.Conn, int) {
	t.Helper()
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(baseURL), header)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	if err != nil {
		return nil, status
	}
	return conn, status
}

func connectRealtime(t *testing.T, baseURL, token string) *realtimeClient {
	t.Helper()
	conn, status := dialRealtime(t, baseURL, token)
	if conn == nil {
		t.Fatalf("connect /api/realtime: want a successful upgrade, got status %d", status)
	}
	t.Cleanup(func() { conn.Close() })

	rc := &realtimeClient{conn: conn, events: make(chan realtimeEvent, 16)}
	go func() {
		for {
			var ev realtimeEvent
			if err := conn.ReadJSON(&ev); err != nil {
				close(rc.events)
				return
			}
			rc.events <- ev
		}
	}()
	return rc
}

func subscribe(t *testing.T, rc *realtimeClient, collection string) {
	t.Helper()
	if err := rc.conn.WriteJSON(map[string]string{"type": "subscribe", "collection": collection}); err != nil {
		t.Fatalf("subscribe to %s: %v", collection, err)
	}
	// There's no subscribe-ack in v1 (spec/realtime.md), so give the
	// server a moment to process the message before the test triggers a
	// write it expects to be notified about.
	time.Sleep(100 * time.Millisecond)
}

func unsubscribe(t *testing.T, rc *realtimeClient, collection string) {
	t.Helper()
	if err := rc.conn.WriteJSON(map[string]string{"type": "unsubscribe", "collection": collection}); err != nil {
		t.Fatalf("unsubscribe from %s: %v", collection, err)
	}
	time.Sleep(100 * time.Millisecond)
}

// readEvent waits for the next event, failing the test if none arrives
// in time — asserts an event *is* delivered.
func readEvent(t *testing.T, rc *realtimeClient) realtimeEvent {
	t.Helper()
	select {
	case ev, ok := <-rc.events:
		if !ok {
			t.Fatalf("read event: connection closed before an event arrived")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatalf("read event: timed out waiting for an event")
		return realtimeEvent{}
	}
}

// expectNoEvent asserts nothing arrives within a short window — asserts
// an event is *not* delivered (e.g. to an unauthorized or unsubscribed
// connection). Safe to keep using rc afterward (see realtimeClient's doc).
func expectNoEvent(t *testing.T, rc *realtimeClient) {
	t.Helper()
	select {
	case ev, ok := <-rc.events:
		if ok {
			t.Fatalf("want no event, got %+v", ev)
		}
	case <-time.After(400 * time.Millisecond):
	}
}

func TestRealtime_ConnectingRequiresScope(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)

	// RT-01: no token at all.
	if conn, status := dialRealtime(t, baseURL, ""); status != http.StatusUnauthorized {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("no token: want 401, got %d", status)
	}

	// RT-01: a token without records:read.
	writeOnly := restToken(t, baseURL, "rt-writeonly@example.com", []string{"records:write"})
	if conn, status := dialRealtime(t, baseURL, writeOnly); status != http.StatusUnauthorized {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("records:write-only token: want 401, got %d", status)
	}
}

func TestRealtime_CreateEventDelivered(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "rt-create@example.com", []string{"records:read", "records:write"})

	rc := connectRealtime(t, baseURL, token)
	subscribe(t, rc, "notes")

	// RT-02
	resp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "hello", "body": "world"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	created := decodeJSONMap(t, resp)

	ev := readEvent(t, rc)
	if ev.Type != "created" || ev.Collection != "notes" {
		t.Fatalf("want a created/notes event, got %+v", ev)
	}
	if ev.Record["title"] != "hello" || ev.Record["id"] != created["id"] {
		t.Fatalf("want the created record in the event, got %+v (created was %v)", ev.Record, created)
	}
}

func TestRealtime_UpdateEventDelivered(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "rt-update@example.com", []string{"records:read", "records:write"})

	rc := connectRealtime(t, baseURL, token)
	subscribe(t, rc, "notes")

	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "before", "body": "x"})
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)
	readEvent(t, rc) // drain the "created" event from the setup above

	// RT-03
	updateResp := doAuthed(t, http.MethodPatch, baseURL+"/api/data/notes/"+id, token, map[string]any{"title": "after"})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("update note: want 200, got %d: %s", updateResp.StatusCode, bodyString(t, updateResp))
	}

	ev := readEvent(t, rc)
	if ev.Type != "updated" || ev.Collection != "notes" {
		t.Fatalf("want an updated/notes event, got %+v", ev)
	}
	if ev.Record["title"] != "after" || ev.Record["id"] != id {
		t.Fatalf("want the full post-update record, got %+v", ev.Record)
	}
}

func TestRealtime_DeleteEventDelivered(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	token := restToken(t, baseURL, "rt-delete@example.com", []string{"records:read", "records:write"})

	rc := connectRealtime(t, baseURL, token)
	subscribe(t, rc, "notes")

	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "bye", "body": "x"})
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)
	readEvent(t, rc) // drain "created"

	// RT-04
	delResp := doAuthed(t, http.MethodDelete, baseURL+"/api/data/notes/"+id, token, nil)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete note: want 204, got %d", delResp.StatusCode)
	}

	ev := readEvent(t, rc)
	if ev.Type != "deleted" || ev.Collection != "notes" || ev.ID != id {
		t.Fatalf("want a deleted/notes event carrying id %s, got %+v", id, ev)
	}
	if ev.Record != nil {
		t.Fatalf("want no record on a deleted event (stale by definition), got %+v", ev.Record)
	}
}

func TestRealtime_InvisibleAcrossUsers(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	tokenA := restToken(t, baseURL, "rt-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "rt-b@example.com", []string{"records:read", "records:write"})

	rcA := connectRealtime(t, baseURL, tokenA)
	rcB := connectRealtime(t, baseURL, tokenB)
	subscribe(t, rcA, "notes")
	subscribe(t, rcB, "notes")

	// RT-05
	resp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenA, map[string]any{"title": "a's note", "body": "x"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d", resp.StatusCode)
	}

	ev := readEvent(t, rcA)
	if ev.Type != "created" || ev.Record["title"] != "a's note" {
		t.Fatalf("want user A's own event, got %+v", ev)
	}
	expectNoEvent(t, rcB)
}

func TestRealtime_UnsubscribeStopsOnlyThatCollection(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema, tagsSchema)
	token := restToken(t, baseURL, "rt-unsub@example.com", []string{"records:read", "records:write"})

	rc := connectRealtime(t, baseURL, token)
	subscribe(t, rc, "notes")
	subscribe(t, rc, "tags")

	// RT-07
	unsubscribe(t, rc, "notes")

	noteResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "x", "body": "x"})
	if noteResp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d", noteResp.StatusCode)
	}
	expectNoEvent(t, rc) // unsubscribed from notes

	tagResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/tags", token, map[string]any{"name": "x"})
	if tagResp.StatusCode != http.StatusCreated {
		t.Fatalf("create tag: want 201, got %d", tagResp.StatusCode)
	}
	ev := readEvent(t, rc) // still subscribed to tags
	if ev.Type != "created" || ev.Collection != "tags" {
		t.Fatalf("want a created/tags event, got %+v", ev)
	}
}

func TestRealtime_ClosingConnectionCleansUpSubscription(t *testing.T) {
	baseURL := testserver.NewWithSchema(t, notesSchema)
	tokenA := restToken(t, baseURL, "rt-close-a@example.com", []string{"records:read", "records:write"})
	tokenB := restToken(t, baseURL, "rt-close-b@example.com", []string{"records:read", "records:write"})

	rcA := connectRealtime(t, baseURL, tokenA)
	rcB := connectRealtime(t, baseURL, tokenB)
	subscribe(t, rcA, "notes")
	subscribe(t, rcB, "notes")

	// RT-08: closing one connection must not disrupt the broker's state
	// for any other subscriber.
	_ = rcA.conn.Close()
	time.Sleep(200 * time.Millisecond) // let the server notice the close

	resp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", tokenB, map[string]any{"title": "still here", "body": "x"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d", resp.StatusCode)
	}
	ev := readEvent(t, rcB)
	if ev.Type != "created" || ev.Record["title"] != "still here" {
		t.Fatalf("want connB's own event after connA closed, got %+v", ev)
	}
}

func TestRealtime_DeniedReadProducesNoEvents(t *testing.T) {
	// RT-06
	baseURL := testserver.NewWithSchema(t, notesSchema, policyRow("notes", "read", "denied"))
	token := restToken(t, baseURL, "rt-denied@example.com", []string{"records:read", "records:write"})

	rc := connectRealtime(t, baseURL, token)
	subscribe(t, rc, "notes")

	// create isn't denied, so the write itself succeeds...
	resp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "x", "body": "x"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d", resp.StatusCode)
	}
	// ...but nobody is authorized to read this collection at all, so no
	// subscriber — including its own creator — is notified of it either.
	expectNoEvent(t, rc)
}

func TestRealtime_AdminUIDataBrowserWritesPublish(t *testing.T) {
	// RT-11
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	token := restToken(t, baseURL, "rt-adminui@example.com", []string{"records:read", "records:write"})

	// The customer creates their own row first, so the admin's writes
	// below (which set owner_id explicitly rather than auto-stamping it,
	// per ADMINUI-13) target a real subject rather than a guess.
	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "before", "body": "x"})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d: %s", createResp.StatusCode, bodyString(t, createResp))
	}
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)
	ownerID, _ := created["owner_id"].(string)

	rc := connectRealtime(t, baseURL, token)
	subscribe(t, rc, "notes")

	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	// The data browser's own create publishes too — a fresh row, same
	// owner_id, since AdminCreateRow uses the submitted owner_id as-is.
	if _, err := owner.PostForm(baseURL+"/admin/ui/data/notes", url.Values{
		"title": {"admin-created"}, "body": {"x"}, "owner_id": {ownerID},
	}); err != nil {
		t.Fatalf("admin create: %v", err)
	}
	ev := readEvent(t, rc)
	if ev.Type != "created" || ev.Collection != "notes" || ev.Record["title"] != "admin-created" {
		t.Fatalf("want a created/notes event from the admin's own create, got %+v", ev)
	}

	// The data browser's edit.
	updResp, err := owner.PostForm(baseURL+"/admin/ui/data/notes/"+id, url.Values{
		"title": {"edited-by-admin"}, "body": {"x"}, "owner_id": {ownerID},
	})
	if err != nil {
		t.Fatalf("admin update: %v", err)
	}
	if updResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin update: want 303, got %d: %s", updResp.StatusCode, bodyString(t, updResp))
	}
	ev = readEvent(t, rc)
	if ev.Type != "updated" || ev.Collection != "notes" || ev.Record["title"] != "edited-by-admin" {
		t.Fatalf("want an updated/notes event from the admin's own edit, got %+v", ev)
	}

	// The data browser's delete.
	delResp, err := owner.PostForm(baseURL+"/admin/ui/data/notes/"+id+"/delete", nil)
	if err != nil {
		t.Fatalf("admin delete: %v", err)
	}
	if delResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin delete: want 303, got %d: %s", delResp.StatusCode, bodyString(t, delResp))
	}
	ev = readEvent(t, rc)
	if ev.Type != "deleted" || ev.Collection != "notes" || ev.ID != id {
		t.Fatalf("want a deleted/notes event from the admin's own delete, got %+v", ev)
	}
}

func TestRealtime_SQLStudioWritesDoNotPublish(t *testing.T) {
	// RT-11's carve-out: SQL Studio's raw SQL isn't parsed to figure out
	// which collection/row it touched, so it never publishes — the write
	// still lands (confirmed via the admin's own subsequent GET) and is
	// still audit-logged (ADMINUI-20), just not pushed live.
	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema: []string{notesSchema},
	})
	token := restToken(t, baseURL, "rt-sqlstudio@example.com", []string{"records:read", "records:write"})
	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "before", "body": "x"})
	created := decodeJSONMap(t, createResp)
	ownerID, _ := created["owner_id"].(string)

	rc := connectRealtime(t, baseURL, token)
	subscribe(t, rc, "notes")

	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)

	query := fmt.Sprintf("INSERT INTO notes (id, owner_id, title, body) VALUES ('sql-studio-row', '%s', 'via-sql', 'x')", ownerID)
	sqlResp, err := owner.PostForm(baseURL+"/admin/ui/sql", url.Values{"query": {query}})
	if err != nil {
		t.Fatalf("sql studio insert: %v", err)
	}
	if sqlResp.StatusCode != http.StatusOK {
		t.Fatalf("sql studio insert: want 200, got %d: %s", sqlResp.StatusCode, bodyString(t, sqlResp))
	}

	rowsBody := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes"))
	if !strings.Contains(rowsBody, "via-sql") {
		t.Fatalf("setup: want the SQL Studio insert to have actually landed, got: %s", rowsBody)
	}
	expectNoEvent(t, rc)
}
