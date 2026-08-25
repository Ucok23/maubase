package realtime

import "context"

// Relay lets Broker fan events out across process boundaries — see
// RedisRelay for the one implementation. A single-process deployment
// (NewBroker, the default, and everything spec/realtime.md describes
// before this file existed) doesn't need one at all: a nil Relay just
// means Publish only ever does the in-process fan-out it always did.
//
// WebSocket connections themselves are never portable across
// processes — Relay only carries the *event*, so each process's own
// Broker can still only hand it to whichever of its own local
// subscribers care. Nothing about per-connection subscription state is
// (or needs to be) shared.
type Relay interface {
	// Publish sends ev (and the ownerID it's scoped to, "" if none) to
	// every other process sharing this Relay.
	Publish(ctx context.Context, ev Event, ownerID string) error
	// Run receives events other processes published via Publish, calling
	// handler for each, until ctx is canceled or an unrecoverable error
	// occurs (in which case it returns that error; ctx.Err() on a normal
	// cancellation). Broker calls this exactly once, from a background
	// goroutine, for the process's lifetime — see NewBrokerWithRelay.
	Run(ctx context.Context, handler func(ev Event, ownerID string)) error
}
