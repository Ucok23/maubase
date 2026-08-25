// Package realtime is the realtime layer: a WebSocket endpoint
// (server.go) backed by an in-process pub/sub broker that
// internal/restapi's write handlers publish to after every successful
// create/update/delete. See spec/realtime.md for the externally
// observable behavior this implements.
//
// Unlike Postgres's LISTEN/NOTIFY or a binlog, there's no database-level
// change feed here — and none is needed. Every write already goes
// through auto-REST's handlers (that's the whole point of auto-REST:
// there's no other way into the data), so the same process doing the
// write already knows exactly what changed and can hand it straight to
// this broker. That's simpler than tailing a change log, not a
// workaround for SQLite lacking one.
//
// The tradeoff: this only fans out within one process. internal/db.Open
// already pins SetMaxOpenConns(1) because SQLite has one writer anyway,
// so this project's whole design assumes a single server process. If
// that ever changed — multiple app instances behind a load balancer — a
// subscriber connected to one instance would never see a write that
// landed on another, since Broker is just in-memory. That would need an
// external broker (Redis pub/sub, NATS) instead; not needed here.
package realtime

import "sync"

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

// Broker fans Events out to subscribed Conns, entirely in-process — see
// the package doc for why that's sufficient here, and its one limit.
type Broker struct {
	mu   sync.Mutex
	subs map[string]map[*Conn]struct{} // collection -> subscribed conns
}

func NewBroker() *Broker {
	return &Broker{subs: make(map[string]map[*Conn]struct{})}
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
// other subscriptions untouched. A no-op if it wasn't subscribed.
func (b *Broker) Unsubscribe(c *Conn, collection string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if set := b.subs[collection]; set != nil {
		delete(set, c)
	}
}

// Close removes c from every collection it was subscribed to and closes
// its event channel — call once, when its connection ends.
func (b *Broker) Close(c *Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, set := range b.subs {
		delete(set, c)
	}
	close(c.events)
}

// Publish delivers ev to every current subscriber of ev.Collection that's
// authorized to see it: everyone, if ownerID is "" (a shared table, or an
// owner-scoped one's operation opened up by an access-policy override —
// visibility was already gated by scope at subscribe time, not by row);
// otherwise, only the subscriber whose Subject equals ownerID.
//
// Delivery is non-blocking and at-most-once: a subscriber whose event
// channel is full (client not reading fast enough) simply misses the
// event rather than stalling the write path that triggered it. See
// spec/realtime.md's note that there's no replay/backfill in v1.
func (b *Broker) Publish(ev Event, ownerID string) {
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
