package realtime

import (
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/ory/fosite"

	"github.com/Ucok23/maubase/internal/oauth"
)

// scopeRead matches internal/restapi's scopeRead: connecting requires
// exactly what a GET would (spec/realtime.md RT-01). Duplicated as its
// own constant rather than imported, the same call internal/storage
// already made for a string this small.
const scopeRead = "records:read"

// Server is the realtime layer's HTTP surface: one WebSocket endpoint,
// backed by a Broker that internal/restapi's write handlers publish to.
type Server struct {
	broker   *Broker
	oauth    *oauth.Server
	upgrader websocket.Upgrader
}

func NewServer(broker *Broker, oauthSvc *oauth.Server) *Server {
	return &Server{
		broker: broker,
		oauth:  oauthSvc,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// Any origin: this is an API meant to be called from
			// arbitrary client apps, the same posture every other route
			// here already takes (nothing else restricts CORS either).
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Mount registers GET /api/realtime onto r.
func (s *Server) Mount(r chi.Router) {
	r.Get("/api/realtime", s.handleConnect)
}

// handleConnect authenticates the handshake, then upgrades. Rejecting
// happens before any upgrade, so there's never a window where an
// unauthenticated socket is open (RT-01) — fosite.AccessTokenFromRequest
// already accepts either an Authorization: Bearer header or an
// ?access_token= query parameter (RFC 6750), which covers browser
// WebSocket clients that can't set arbitrary handshake headers.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	token := fosite.AccessTokenFromRequest(r)
	if token == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	subject, err := s.oauth.Authenticate(r.Context(), token, scopeRead)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response on failure.
	}

	c := s.broker.NewConn(subject)

	// gorilla/websocket requires at most one goroutine reading and at
	// most one writing at a time; readPump/writePump split exactly that
	// way. writePump exits once Broker.Close(c) closes c's event channel
	// below, and wg.Wait() makes sure that's finished — and so no more
	// concurrent writes are possible — before conn.Close().
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.writePump(conn, c)
	}()

	s.readPump(conn, c) // blocks until the connection closes

	s.broker.Close(c)
	wg.Wait()
	_ = conn.Close()
}

// readPump handles subscribe/unsubscribe control messages until the
// connection closes. Unknown message types are ignored rather than
// closing the connection, so a future message type stays
// forward-compatible with older clients.
func (s *Server) readPump(conn *websocket.Conn, c *Conn) {
	for {
		var msg struct {
			Type       string `json:"type"`
			Collection string `json:"collection"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if msg.Collection == "" {
			continue
		}
		switch msg.Type {
		case "subscribe":
			s.broker.Subscribe(c, msg.Collection)
		case "unsubscribe":
			s.broker.Unsubscribe(c, msg.Collection)
		}
	}
}

// writePump forwards every Event c receives to conn as JSON, until c's
// event channel is closed (by Broker.Close) or a write fails.
func (s *Server) writePump(conn *websocket.Conn, c *Conn) {
	for ev := range c.events {
		if err := conn.WriteJSON(ev); err != nil {
			return
		}
	}
}
