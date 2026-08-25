// Package server wires the HTTP API: routing, middleware, and handlers.
package server

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"maubase/internal/adminui"
	"maubase/internal/audit"
	"maubase/internal/auth"
	"maubase/internal/oauth"
	"maubase/internal/ownerauth"
	"maubase/internal/ratelimit"
	"maubase/internal/realtime"
	"maubase/internal/restapi"
	"maubase/internal/storage"
)

type Server struct {
	auth      *auth.Service
	oauth     *oauth.Server
	ownerAuth *ownerauth.Service
	restapi   *restapi.Server
	storage   *storage.Server
	realtime  *realtime.Server
	adminui   *adminui.Server
	audit     *audit.Log
	router    chi.Router

	loginLimiter *ratelimit.Limiter
}

// New wires the full HTTP API. loginRateLimit/Window configure the
// throttle on the login endpoints (see internal/ratelimit); pass 0 for
// loginRateLimit to disable it (unlimited attempts) — useful for tests
// that aren't exercising rate-limiting and don't want to think about it.
func New(authSvc *auth.Service, oauthSvc *oauth.Server, ownerAuthSvc *ownerauth.Service, restapiSvc *restapi.Server, storageSvc *storage.Server, realtimeSvc *realtime.Server, adminuiSvc *adminui.Server, auditLog *audit.Log, loginRateLimit int, loginRateWindow time.Duration) *Server {
	s := &Server{
		auth: authSvc, oauth: oauthSvc, ownerAuth: ownerAuthSvc, restapi: restapiSvc, storage: storageSvc, realtime: realtimeSvc, adminui: adminuiSvc, audit: auditLog,
		loginLimiter: ratelimit.New(loginRateLimit, loginRateWindow),
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/signup", s.handleSignUp)
		r.With(s.rateLimitLogin).Post("/login", s.handleLogin)
		r.Post("/logout", s.handleLogout)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/me", s.handleMe)
			r.Get("/me/export", s.handleExportAccount)
			r.Delete("/me", s.handleDeleteAccount)
		})
	})

	// OAuth 2.1 authorization server: lets this server's users authorize
	// third-party apps (MCP clients included) against its API. See
	// internal/oauth for the Fosite wiring.
	r.Get("/oauth/authorize", s.oauth.HandleAuthorize)
	r.Post("/oauth/authorize", s.oauth.HandleAuthorize)
	r.Post("/oauth/token", s.oauth.HandleToken)
	r.Post("/oauth/revoke", s.oauth.HandleRevoke)
	r.Post("/oauth/register", s.oauth.HandleRegister)
	r.Get("/.well-known/oauth-authorization-server", s.oauth.HandleAuthServerMetadata)
	r.Get("/.well-known/jwks.json", s.oauth.HandleJWKS)

	// Demo resource protected by an OAuth access token; proved the full
	// register -> authorize -> token -> call loop end-to-end before the
	// real auto-REST layer below existed. Kept as a minimal liveness
	// check for the OAuth layer independent of any application schema.
	r.Get("/api/oauth/whoami", s.oauth.RequireScope("profile", s.oauth.HandleWhoAmI))

	// Auto-REST: every non-reserved table in the database, as a
	// /api/data/{table} CRUD resource. See internal/restapi.
	s.restapi.Mount(r)

	// File storage: upload/download endpoints outside auto-REST (files
	// need multipart/raw-byte handling generic JSON CRUD doesn't do). See
	// internal/storage.
	s.storage.Mount(r)

	// Realtime: GET /api/realtime, a WebSocket stream of auto-REST's own
	// write events. See internal/realtime.
	s.realtime.Mount(r)

	// Owner plane: the team running this deployment. Entirely separate
	// cookie/session/table from the customer plane above and the OAuth
	// layer — see internal/ownerauth's package doc.
	r.Route("/admin/auth", func(r chi.Router) {
		r.With(s.rateLimitLogin).Post("/login", s.handleOwnerLogin)
		r.Post("/logout", s.handleOwnerLogout)
		r.Group(func(r chi.Router) {
			r.Use(s.requireOwnerRole(ownerauth.RoleViewer))
			r.Get("/me", s.handleOwnerMe)
		})
	})
	r.Route("/admin/owners", func(r chi.Router) {
		r.Use(s.requireOwnerRole(ownerauth.RoleAdmin))
		r.Get("/", s.handleListOwners)
		r.Group(func(r chi.Router) {
			r.Use(s.requireOwnerRole(ownerauth.RoleOwner))
			r.Post("/", s.handleCreateOwner)
			r.Delete("/{id}", s.handleDeleteOwner)
		})
	})
	r.Route("/admin/audit-log", func(r chi.Router) {
		r.Use(s.requireOwnerRole(ownerauth.RoleAdmin))
		r.Get("/", s.handleListAuditLog)
	})
	r.Route("/admin/maintenance", func(r chi.Router) {
		r.Use(s.requireOwnerRole(ownerauth.RoleAdmin))
		r.Post("/purge-sessions", s.handlePurgeSessions)
	})

	// Embedded admin UI: /admin/ui/*, the owner plane's browser-facing
	// surface — same session cookie and roles as the JSON routes above.
	// See internal/adminui.
	s.adminui.Mount(r)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	s.router = r
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// rateLimitLogin throttles a login endpoint per client IP (see
// internal/ratelimit), regardless of whether the attempt succeeds — the
// point is bounding brute-force guessing, not just repeated failures. A
// loginLimiter with Limit <= 0 (server built with loginRateLimit 0) is
// treated as disabled, since ratelimit.Limiter itself has no "unlimited"
// mode.
func (s *Server) rateLimitLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.loginLimiter.Limit <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		key := clientIP(r)
		if ok, retryAfter := s.loginLimiter.Allow(key); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many login attempts"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the caller's address from r.RemoteAddr, which
// middleware.RealIP (registered above) has already normalized from
// X-Forwarded-For/X-Real-IP when present.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
