package realtime

import "testing"

// This file is a deliberate, narrow exception to this project's usual
// black-box-only testing convention (see README's "Testing" section and
// every other package's tests under test/): RT-13's guarantee is about
// Broker.subs, an unexported field with no HTTP-observable surface at
// all — there is no request/response pair that differs before and after
// this fix, only internal memory growth. A black-box test has no way to
// assert on it; a white-box test in this same package, with direct field
// access, is the only honest way to lock in the invariant.

func TestBroker_UnsubscribePrunesEmptyCollectionEntry(t *testing.T) {
	// RT-13
	b := NewBroker()
	c := b.NewConn("subject-a")

	for i := range 50 {
		collection := randomCollectionName(i)
		b.Subscribe(c, collection)
		if _, ok := b.subs[collection]; !ok {
			t.Fatalf("setup: want %q present in subs after Subscribe", collection)
		}
		b.Unsubscribe(c, collection)
		if _, ok := b.subs[collection]; ok {
			t.Fatalf("want %q removed from subs entirely once its last subscriber unsubscribed, got %v", collection, b.subs[collection])
		}
	}
	if len(b.subs) != 0 {
		t.Fatalf("want subs empty after unsubscribing from every collection, got %d entries: %v", len(b.subs), b.subs)
	}
}

func TestBroker_ClosePrunesEveryEmptyCollectionEntry(t *testing.T) {
	// RT-13
	b := NewBroker()
	c := b.NewConn("subject-a")

	for i := range 50 {
		b.Subscribe(c, randomCollectionName(i))
	}
	if len(b.subs) != 50 {
		t.Fatalf("setup: want 50 entries in subs before Close, got %d", len(b.subs))
	}

	b.Close(c)
	if len(b.subs) != 0 {
		t.Fatalf("want every collection's entry pruned once its only subscriber closed, got %d entries left: %v", len(b.subs), b.subs)
	}
}

// TestBroker_UnsubscribeLeavesOtherSubscribersEntryIntact guards against
// an overzealous fix: pruning a collection's entry must only happen once
// its subscriber set is actually empty, never just because one of
// several subscribers left.
func TestBroker_UnsubscribeLeavesOtherSubscribersEntryIntact(t *testing.T) {
	a := &Conn{Subject: "subject-a", events: make(chan Event, 1)}
	b2 := &Conn{Subject: "subject-b", events: make(chan Event, 1)}
	b := NewBroker()
	b.Subscribe(a, "notes")
	b.Subscribe(b2, "notes")

	b.Unsubscribe(a, "notes")
	set, ok := b.subs["notes"]
	if !ok {
		t.Fatalf("want notes' entry still present while b2 remains subscribed, got none")
	}
	if _, stillThere := set[b2]; !stillThere {
		t.Fatalf("want b2 still a subscriber of notes, got %v", set)
	}
}

func randomCollectionName(i int) string {
	// Deliberately not real table names — readPump never validates
	// msg.Collection against the schema, so a bogus, attacker-chosen
	// string is exactly the input this guards against.
	return "bogus-collection-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}
