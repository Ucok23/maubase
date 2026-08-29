// Package realtime is the realtime layer: a WebSocket endpoint
// (server.go) backed by a pub/sub broker that internal/restapi's write
// handlers publish to after every successful create/update/delete. See
// spec/realtime.md for the externally observable behavior this
// implements.
//
// Unlike Postgres's LISTEN/NOTIFY or a binlog, there's no database-level
// change feed here — and none is needed. Every write already goes
// through auto-REST's handlers (that's the whole point of auto-REST:
// there's no other way into the data), so the same process doing the
// write already knows exactly what changed and can hand it straight to
// this broker. That's simpler than tailing a change log, not a
// workaround for SQLite lacking one.
//
// Fan-out is in-process by default (NewBroker) — fine for this
// project's usual single-server-process shape, where
// internal/db.Open's SetMaxOpenConns(1) already reflects SQLite having
// one writer anyway. Run more than one maubase process (behind a load
// balancer, say — SQLite's own file locking is still what actually
// serializes writes across them) and a subscriber connected to one
// instance would never see a write that landed on another purely
// in-memory. NewBrokerWithRelay is the fix — see Relay and RedisRelay.
package realtime

import (
	"context"
	"sync"
	"time"
)

// Event is one row-level change, pushed to every subscriber of its
// Collection that's authorized to see it.
type Event struct {
	Type       string         `json:"type"` // "created", "updated", or "deleted"
	Collection string         `json:"collection"`
	Record     map[string]any `json:"record,omitempty"`
	// ID is set on "deleted" instead of Record, which would otherwise be
	// stale by definition — there's no post-delete row to send.
	ID string `json:"id,omitempty"`
}

// Conn is one realtime connection's subscriber identity: the subject its
// access token was issued for (used to filter owner-scoped events to
// only the rows that subject may see) and the channel Events arrive on.
type Conn struct {
	Subject string
	events  chan Event
}

// Broker fans Events out to subscribed Conns — in-process only unless
// built with a Relay, see the package doc.
type Broker struct {
	mu    sync.Mutex
	subs  map[string]map[*Conn]struct{} // collection -> subscribed conns
	relay Relay
}

// NewBroker builds a single-process broker — the default, and what every
// spec/realtime.md scenario describes.
func NewBroker() *Broker {
	return &Broker{subs: make(map[string]map[*Conn]struct{})}
}

// NewBrokerWithRelay is NewBroker plus relay: Publish additionally
// fans each event out over relay for every other process sharing it to
// see (best-effort — a relay error never blocks or fails the write path
// that triggered the publish, since the event still reaches this
// process's own subscribers either way), and a background goroutine
// runs for the lifetime of ctx, calling relay.Run to receive whatever
// other processes publish and hand it to this process's own local
// subscribers exactly as if it had originated here (never re-relayed
// again — that's what would turn this into an infinite loop across
// instances).
func NewBrokerWithRelay(ctx context.Context, relay Relay) *Broker {
	b := &Broker{subs: make(map[string]map[*Conn]struct{}), relay: relay}
	go func() {
		_ = relay.Run(ctx, func(ev Event, ownerID string) {
			b.publishLocal(ev, ownerID)
		})
	}()
	return b
}

// NewConn registers a new subscriber identity for subject. The caller is
// responsible for calling Close once the underlying connection ends.
func (b *Broker) NewConn(subject string) *Conn {
	return &Conn{Subject: subject, events: make(chan Event, 16)}
}

// Subscribe adds c as a subscriber of collection. Idempotent.
func (b *Broker) Subscribe(c *Conn, collection string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[collection] == nil {
		b.subs[collection] = make(map[*Conn]struct{})
	}
	b.subs[collection][c] = struct{}{}
}

// Unsubscribe removes c from collection's subscribers, leaving any of its
// other subscriptions untouched. A no-op if it wasn't subscribed. Also
// deletes collection's own map entry once it's empty — see Close's doc
// comment for why this matters.
func (b *Broker) Unsubscribe(c *Conn, collection string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if set := b.subs[collection]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(b.subs, collection)
		}
	}
}

// Close removes c from every collection it was subscribed to — deleting
// each one's own map entry once it's left empty, same as Unsubscribe —
// and closes its event channel. Call once, when its connection ends.
// Without pruning empty entries, b.subs would grow unboundedly keyed by
// whatever collection names get subscribed to: readPump passes
// msg.Collection straight through with no validation against real
// tables, so a client (or an attacker) cycling through many distinct,
// possibly bogus, one-off names — subscribe, then disconnect — would
// leave one dead, empty map entry behind per name, forever.
func (b *Broker) Close(c *Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for collection, set := range b.subs {
		delete(set, c)
		if len(set) == 0 {
			delete(b.subs, collection)
		}
	}
	close(c.events)
}

// Publish delivers ev to every current subscriber of ev.Collection on
// this process that's authorized to see it: everyone, if ownerID is ""
// (a shared table, or an owner-scoped one's operation opened up by an
// access-policy override — visibility was already gated by scope at
// subscribe time, not by row); otherwise, only the subscriber whose
// Subject equals ownerID. If this Broker was built with a Relay
// (NewBrokerWithRelay), the event is also handed to it so every other
// process sharing that Relay delivers it to their own local subscribers
// too — see the package doc.
//
// Local delivery is non-blocking and at-most-once: a subscriber whose
// event channel is full (client not reading fast enough) simply misses
// the event rather than stalling the write path that triggered it. See
// spec/realtime.md's note that there's no replay/backfill in v1.
func (b *Broker) Publish(ev Event, ownerID string) {
	b.publishLocal(ev, ownerID)
	if b.relay == nil {
		return
	}
	// Relaying happens off to the side, deliberately: a slow or
	// momentarily-unreachable Redis shouldn't add latency to (or fail)
	// the auto-REST write that triggered this — this process's own
	// subscribers already got their copy above regardless of what
	// happens here.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = b.relay.Publish(ctx, ev, ownerID)
	}()
}

func (b *Broker) publishLocal(ev Event, ownerID string) {
	b.mu.Lock()
	subs := b.subs[ev.Collection]
	conns := make([]*Conn, 0, len(subs))
	for c := range subs {
		if ownerID == "" || c.Subject == ownerID {
			conns = append(conns, c)
		}
	}
	b.mu.Unlock()

	for _, c := range conns {
		select {
		case c.events <- ev:
		default:
		}
	}
}
