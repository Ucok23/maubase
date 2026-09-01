package e2e_test

import (
	"net/http"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/Ucok23/maubase/internal/realtime"
	"github.com/Ucok23/maubase/internal/testserver"
)

// Scenario: spec/realtime.md RT-09. Two separate server processes (here,
// two testserver instances — separate DBs, separate Brokers, separate
// ports, exactly like two real maubase processes behind a load balancer)
// share one Redis instance as their Relay. A write against instance A
// must reach a subscriber connected to instance B, proving fan-out isn't
// actually in-process-only once a Relay is configured — the fix for the
// limit spec/realtime.md documented before RedisRelay existed.
func TestRealtime_RelayDeliversAcrossProcesses(t *testing.T) {
	mr := miniredis.RunT(t) // pure-Go in-process Redis server; RunT wires its own t.Cleanup

	relayA, err := realtime.NewRedisRelay("redis://"+mr.Addr(), "test:realtime")
	if err != nil {
		t.Fatalf("new relay A: %v", err)
	}
	relayB, err := realtime.NewRedisRelay("redis://"+mr.Addr(), "test:realtime")
	if err != nil {
		t.Fatalf("new relay B: %v", err)
	}

	// tagsSchema, not notesSchema: it has no owner_id, so Broker.Publish
	// passes ownerID "" and every subscriber sees it regardless of
	// Conn.Subject. notesSchema is owner-scoped, and the two "users"
	// below are on two entirely separate instances' DBs with unrelated
	// subject ids — an owner-scoped event would just be filtered out by
	// Broker.publishLocal on the receiving side, which isn't what this
	// test is trying to prove.
	baseURLA := testserver.NewWithRelay(t, relayA, tagsSchema)
	baseURLB := testserver.NewWithRelay(t, relayB, tagsSchema)

	tokenB := restToken(t, baseURLB, "rt-relay-b@example.com", []string{"records:read", "records:write"})
	rcB := connectRealtime(t, baseURLB, tokenB)
	subscribe(t, rcB, "tags")

	// Also subscribe on instance A itself — the same process that's
	// about to make the write — to prove RedisRelay's origin-id check
	// works: without it, Redis echoing a PUBLISH back to the publisher's
	// own subscription would double-deliver this event to rcA (once via
	// Broker.Publish's immediate local fan-out, once more via the
	// round trip through Redis).
	tokenA := restToken(t, baseURLA, "rt-relay-a@example.com", []string{"records:read", "records:write"})
	rcA := connectRealtime(t, baseURLA, tokenA)
	subscribe(t, rcA, "tags")

	resp := doAuthed(t, http.MethodPost, baseURLA+"/api/data/tags", tokenA, map[string]any{"name": "cross-process"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create tag on instance A: want 201, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	// RT-09: instance B's subscriber sees the write that happened on
	// instance A, relayed through Redis rather than in-process.
	ev := readEvent(t, rcB)
	if ev.Type != "created" || ev.Collection != "tags" {
		t.Fatalf("want a created/tags event relayed from instance A, got %+v", ev)
	}
	if ev.Record["name"] != "cross-process" {
		t.Fatalf("want the record created on instance A, got %+v", ev.Record)
	}

	// Instance A's own subscriber sees it exactly once, not twice.
	evA := readEvent(t, rcA)
	if evA.Type != "created" || evA.Collection != "tags" {
		t.Fatalf("want a created/tags event on instance A's own subscriber, got %+v", evA)
	}
	expectNoEvent(t, rcA)
}
