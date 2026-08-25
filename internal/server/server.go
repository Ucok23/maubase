// Package server wires the HTTP API: routing, middleware, and handlers.
package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"baas/internal/audit"
	"baas/internal/auth"
	"baas/internal/oauth"
	"baas/internal/ownerauth"
	"baas/internal/restapi"
)

type Server struct {
	auth      *auth.Service
	oauth     *oauth.Server
	ownerAuth *ownerauth.Service
	restapi   *restapi.Server
	audit     *audit.Log
	router    chi.Router
}

func New(authSvc *auth.Service, oauthSvc *oauth.Server, ownerAuthSvc *ownerauth.Service, restapiSvc *restapi.Server, auditLog *audit.Log) *Server {
	s := &Server{auth: authSvc, oauth: oauthSvc, ownerAuth: ownerAuthSvc, restapi: restapiSvc, audit: auditLog}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/signup", s.handleSignUp)
		r.Post("/login", s.handleLogin)
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

	// Owner plane: the team running this deployment. Entirely separate
	// cookie/session/table from the customer plane above and the OAuth
	// layer — see internal/ownerauth's package doc.
	r.Route("/admin/auth", func(r chi.Router) {
		r.Post("/login", s.handleOwnerLogin)
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
