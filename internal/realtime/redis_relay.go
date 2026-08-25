package realtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// RedisRelay is a Relay backed by Redis pub/sub — the fix for
// spec/realtime.md's documented v1 limit (fan-out was in-process only,
// so a subscriber connected to one instance never saw a write that
// landed on another). Every maubase process sharing the same Redis
// instance and channel name sees every other process's events.
type RedisRelay struct {
	client   *redis.Client
	channel  string
	originID string
}

// NewRedisRelay connects to a Redis instance (redisURL is a redis://
// or rediss:// connection string — see redis.ParseURL for the exact
// grammar) and relays on channel. A deployment running more than one
// thing through the same Redis instance should give each its own
// channel name; this package doesn't namespace it for you.
func NewRedisRelay(redisURL, channel string) (*RedisRelay, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &RedisRelay{client: redis.NewClient(opts), channel: channel, originID: uuid.NewString()}, nil
}

// relayMessage is the wire format one process's Publish sends and every
// other process's Run parses back out — Event plus the ownerID
// Broker.Publish already takes, wrapped together since Redis pub/sub
// messages are a single payload, not a pair of arguments. OriginID is
// this RedisRelay instance's own random id (one per process, set at
// NewRedisRelay), stamped on every message it publishes so its own Run
// loop can recognize and drop it — see Run's doc for why that matters.
type relayMessage struct {
	Event    Event  `json:"event"`
	OwnerID  string `json:"owner_id,omitempty"`
	OriginID string `json:"origin_id"`
}

func (r *RedisRelay) Publish(ctx context.Context, ev Event, ownerID string) error {
	payload, err := json.Marshal(relayMessage{Event: ev, OwnerID: ownerID, OriginID: r.originID})
	if err != nil {
		return fmt.Errorf("marshal relay message: %w", err)
	}
	return r.client.Publish(ctx, r.channel, payload).Err()
}

// Run subscribes to channel and, for every message that didn't
// originate from this same RedisRelay instance, calls handler. The
// self-check is necessary, not an optimization: Redis pub/sub delivers
// a PUBLISH to every subscriber of that channel, including one held by
// the very client that published it, so without it a Broker's own
// Publish would double-deliver to its own local subscribers — once
// immediately (Broker.Publish's own publishLocal call) and once more,
// a moment later, via this exact round trip through Redis.
func (r *RedisRelay) Run(ctx context.Context, handler func(ev Event, ownerID string)) error {
	sub := r.client.Subscribe(ctx, r.channel)
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			var m relayMessage
			if err := json.Unmarshal([]byte(msg.Payload), &m); err != nil {
				// A malformed message (a stray publisher on the same
				// channel, say) shouldn't take down the whole relay loop.
				continue
			}
			if m.OriginID == r.originID {
				continue // our own publish, echoed back — see doc above
			}
			handler(m.Event, m.OwnerID)
		}
	}
}
