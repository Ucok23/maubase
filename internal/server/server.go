// Package server wires the HTTP API: routing, middleware, and handlers.
package server

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Ucok23/maubase/internal/adminui"
	"github.com/Ucok23/maubase/internal/audit"
	"github.com/Ucok23/maubase/internal/auth"
	"github.com/Ucok23/maubase/internal/email"
	"github.com/Ucok23/maubase/internal/oauth"
	"github.com/Ucok23/maubase/internal/ownerauth"
	"github.com/Ucok23/maubase/internal/ratelimit"
	"github.com/Ucok23/maubase/internal/realtime"
	"github.com/Ucok23/maubase/internal/restapi"
	"github.com/Ucok23/maubase/internal/social"
	"github.com/Ucok23/maubase/internal/storage"
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
	// registerLimiter is deliberately its own bucket, not loginLimiter's:
	// POST /oauth/register is called incidentally by all sorts of
	// legitimate setup (a client registering once before ever attempting
	// a login), so sharing loginLimiter's budget would let registration
	// traffic eat into — or outright exhaust — an IP's login-attempt
	// allowance, an unrelated-endpoint interference this project
	// otherwise avoids (see internal/adminui.Server's own separate
	// limiter instance, for the same reason).
	registerLimiter *ratelimit.Limiter
	// ownerLoginLimiter is POST /admin/auth/login's own bucket, separate
	// from loginLimiter: without this, a client hammering the
	// customer-plane /api/auth/login or /forgot-password from some IP
	// (a shared office/NAT address an admin also happens to use, say)
	// exhausted the very budget an admin needed for /admin/auth/login,
	// and vice versa — an incident responder could be locked out of the
	// owner plane by unrelated customer-plane traffic. Matches
	// internal/adminui.Server's own separate limiter for the HTML login
	// page, which already got this right.
	ownerLoginLimiter *ratelimit.Limiter

	email            email.Sender
	passwordResetURL string

	// socialProviders is keyed by name ("google", "github"); a provider
	// missing from the map (its client id/secret weren't configured)
	// makes GET /api/auth/social/{provider} 404, same as naming a
	// provider that doesn't exist at all — see spec/social-login.md.
	socialProviders     map[string]social.Provider
	socialLoginRedirect string
}

// New wires the full HTTP API. loginRateLimit/Window configure the
// throttle on the login endpoints (see internal/ratelimit); pass 0 for
// loginRateLimit to disable it (unlimited attempts) — useful for tests
// that aren't exercising rate-limiting and don't want to think about it.
// emailSender/passwordResetURL back POST /api/auth/forgot-password — see
// spec/password-reset.md; emailSender is typically email.NoopSender{}
// (from config.Load, when MAUBASE_RESEND_API_KEY/EMAIL_FROM aren't set)
// or email.NewFakeSender() in tests that need to inspect what was "sent".
// socialProviders/socialLoginRedirect back "Continue with <provider>" —
// see spec/social-login.md; an empty/nil map is fine, it just means no
// provider is offered.
func New(authSvc *auth.Service, oauthSvc *oauth.Server, ownerAuthSvc *ownerauth.Service, restapiSvc *restapi.Server, storageSvc *storage.Server, realtimeSvc *realtime.Server, adminuiSvc *adminui.Server, auditLog *audit.Log, loginRateLimit int, loginRateWindow time.Duration, emailSender email.Sender, passwordResetURL string, socialProviders map[string]social.Provider, socialLoginRedirect string) *Server {
	s := &Server{
		auth: authSvc, oauth: oauthSvc, ownerAuth: ownerAuthSvc, restapi: restapiSvc, storage: storageSvc, realtime: realtimeSvc, adminui: adminuiSvc, audit: auditLog,
		loginLimiter:        ratelimit.New(loginRateLimit, loginRateWindow),
		registerLimiter:     ratelimit.New(loginRateLimit, loginRateWindow),
		ownerLoginLimiter:   ratelimit.New(loginRateLimit, loginRateWindow),
		email:               emailSender,
		passwordResetURL:    passwordResetURL,
		socialProviders:     socialProviders,
		socialLoginRedirect: socialLoginRedirect,
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
		// Shares loginLimiter's per-IP budget rather than a separate one:
		// both are "someone repeatedly hitting an auth endpoint for this
		// email/IP" abuse, and forgot-password is the more attractive
		// target for outright email-bombing a victim, so it deserves the
		// same throttle at minimum, not a looser one of its own.
		r.With(s.rateLimitLogin).Post("/forgot-password", s.handleForgotPassword)
		// reset-password's 256-bit token makes brute force impractical
		// regardless, but throttling it anyway keeps every "guess a
		// secret" endpoint in this group consistent.
		r.With(s.rateLimitLogin).Post("/reset-password", s.handleResetPassword)
		// social-login-start is the more concrete risk of the two: each
		// attempt does a real outbound HTTP exchange with the upstream
		// provider, so an unauthenticated inbound endpoint could otherwise
		// generate unbounded outbound requests — an SSRF-adjacent/DoS-
		// amplification surface, and the kind of thing that gets a
		// deployment's own registered OAuth client flagged by the
		// provider. The callback is left unthrottled: it's driven by a
		// redirect from the provider itself after a real user completed
		// that provider's own login, not directly attacker-triggerable at
		// volume the way the start endpoint is.
		r.With(s.rateLimitLogin).Get("/social/{provider}", s.handleSocialStart)
		r.Get("/social/{provider}/callback", s.handleSocialCallback)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/me", s.handleMe)
			r.Get("/me/export", s.handleExportAccount)
			r.Delete("/me", s.handleDeleteAccount)
			r.Get("/me/consents", s.handleListConsents)
			r.Delete("/me/consents/{client_id}", s.handleRevokeConsent)
		})
	})

	// OAuth 2.1 authorization server: lets this server's users authorize
	// third-party apps (MCP clients included) against its API. See
	// internal/oauth for the Fosite wiring.
	r.Get("/oauth/authorize", s.oauth.HandleAuthorize)
	// Rate-limited: this same route's embedded login form calls
	// s.auth.Login exactly like POST /api/auth/login does, so without
	// this it was a complete bypass of that endpoint's brute-force
	// protection — register a throwaway client (unauthenticated,
	// trivial) and guess passwords here instead. Shares loginLimiter's
	// per-IP budget rather than a separate one, same reasoning as
	// forgot-password below: a legitimate flow only ever POSTs here
	// once for login and once for consent, well within any reasonable
	// window.
	r.With(s.rateLimitLogin).Post("/oauth/authorize", s.oauth.HandleAuthorize)
	r.Post("/oauth/token", s.oauth.HandleToken)
	r.Post("/oauth/revoke", s.oauth.HandleRevoke)
	// Dynamic client registration (RFC 7591) is unauthenticated by design
	// — that's the whole point, an MCP client needs zero setup — which
	// also makes it a classic anonymous-endpoint abuse target (unbounded
	// oauth_clients row growth). Its own limiter, not loginLimiter's
	// shared budget — see registerLimiter's doc comment.
	r.With(s.rateLimitRegister).Post("/oauth/register", s.oauth.HandleRegister)
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
		// Its own limiter, not loginLimiter's shared budget — see
		// ownerLoginLimiter's doc comment.
		r.With(s.rateLimitOwnerLogin).Post("/login", s.handleOwnerLogin)
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
	return rateLimitMiddleware(s.loginLimiter, "too many login attempts", nil, next)
}

// rateLimitRegister throttles POST /oauth/register per client IP, via its
// own limiter instance — see registerLimiter's doc comment for why this
// isn't just rateLimitLogin again.
func (s *Server) rateLimitRegister(next http.Handler) http.Handler {
	return rateLimitMiddleware(s.registerLimiter, "too many registration attempts", nil, next)
}

// rateLimitOwnerLogin throttles POST /admin/auth/login per client IP, via
// its own limiter instance — see ownerLoginLimiter's doc comment. Unlike
// the customer-plane rateLimitLogin above, a rejection here is also
// audit-logged (EventLoginRateLimited): the owner-plane audit trail
// otherwise records a failed login only once a credential was actually
// looked up (EventLoginFailed), so a sustained brute-force attempt that
// gets throttled left zero trace of the attempts the limiter itself
// stopped — exactly what an incident review most wants to see. There's
// no known account to name as target yet (the request never reached
// handleOwnerLogin), so the client IP that got throttled is recorded in
// metadata instead.
func (s *Server) rateLimitOwnerLogin(next http.Handler) http.Handler {
	return rateLimitMiddleware(s.ownerLoginLimiter, "too many login attempts", func(r *http.Request, key string) {
		s.audit.RecordLogged(r.Context(), audit.EventLoginRateLimited, audit.Actor{}, audit.Target{}, map[string]any{"client_ip": key})
	}, next)
}

// rateLimitMiddleware is the shared throttle: onLimited, if non-nil, runs
// once per actually-rejected (429) request, before the 429 itself is
// written — used by rateLimitOwnerLogin to audit-log a rejection; every
// other caller passes nil since they have nothing extra to do here.
func rateLimitMiddleware(limiter *ratelimit.Limiter, message string, onLimited func(r *http.Request, clientIP string), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limiter.Limit <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		key := clientIP(r)
		if ok, retryAfter := limiter.Allow(key); !ok {
			if onLimited != nil {
				onLimited(r, key)
			}
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": message})
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
