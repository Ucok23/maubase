// Package adminui is the embedded admin UI: server-rendered HTML under
// /admin/ui/*, using the same owner-plane session cookie and roles
// internal/server's JSON owner-plane routes (/admin/auth, /admin/owners,
// /admin/audit-log, /admin/maintenance) already enforce — this is a
// second, browser-friendly surface over that same plane, not a new
// authorization model. No JS build step: html/template plus a vendored
// htmx.js/pico.min.css, both go:embed'd — the same "deliberately plain,
// no asset pipeline" approach internal/oauth/templates.go's login/consent
// screens already take. See spec/admin-ui.md.
package adminui

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"maubase/internal/audit"
	"maubase/internal/auth"
	"maubase/internal/ownerauth"
	"maubase/internal/restapi"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"add": func(a, b int) int { return a + b },
	"sub": func(a, b int) int { return a - b },
}).ParseFS(templatesFS, "templates/*.html"))

const (
	defaultLimit = 50
	maxLimit     = 200
)

type Server struct {
	auth      *auth.Service
	ownerAuth *ownerauth.Service
	restapi   *restapi.Server
	audit     *audit.Log
}

func NewServer(authSvc *auth.Service, ownerAuthSvc *ownerauth.Service, restapiSvc *restapi.Server, auditLog *audit.Log) *Server {
	return &Server{auth: authSvc, ownerAuth: ownerAuthSvc, restapi: restapiSvc, audit: auditLog}
}

// Mount registers /admin/ui/* onto r.
func (s *Server) Mount(r chi.Router) {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // can't happen: static/* is embedded at build time
	}
	r.Handle("/admin/ui/static/*", http.StripPrefix("/admin/ui/static/", http.FileServer(http.FS(staticSub))))

	r.Route("/admin/ui", func(r chi.Router) {
		r.Get("/login", s.handleLoginPage)
		r.Post("/login", s.handleLoginSubmit)
		r.Post("/logout", s.handleLogout)

		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(ownerauth.RoleViewer))
			r.Get("/", s.handleDashboard)
			r.Get("/data", s.handleDataCollections)
			r.Get("/data/{collection}", s.handleDataRows)
		})
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(ownerauth.RoleDeveloper))
			r.Post("/data/{collection}", s.handleDataCreate)
			r.Get("/data/{collection}/{id}/edit", s.handleDataEditForm)
			r.Post("/data/{collection}/{id}", s.handleDataUpdate)
			r.Post("/data/{collection}/{id}/delete", s.handleDataDelete)
		})
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(ownerauth.RoleAdmin))
			r.Get("/owners", s.handleOwnersPage)
			r.Get("/audit-log", s.handleAuditLogPage)
			r.Get("/maintenance", s.handleMaintenancePage)
			r.Post("/maintenance/purge-sessions", s.handlePurgeSessions)
		})
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(ownerauth.RoleOwner))
			r.Post("/owners", s.handleCreateOwner)
			r.Post("/owners/{id}/delete", s.handleDeleteOwner)
		})
	})
}

// --- shell: login/logout ----------------------------------------------

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// Already signed in: skip straight past the login form.
	if c, err := r.Cookie(ownerauth.SessionCookieName); err == nil {
		if _, err := s.ownerAuth.ValidateSession(r.Context(), c.Value); err == nil {
			http.Redirect(w, r, "/admin/ui", http.StatusSeeOther)
			return
		}
	}
	render(w, "login", map[string]any{"Title": "Sign in"})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render(w, "login", map[string]any{"Title": "Sign in", "Error": "invalid form submission"})
		return
	}
	email, password := r.FormValue("email"), r.FormValue("password")

	session, err := s.ownerAuth.Login(r.Context(), email, password)
	if err != nil {
		// Recorded regardless of whether the email corresponds to a real
		// account, matching handleOwnerLogin's JSON counterpart.
		_ = s.audit.Record(r.Context(), audit.EventLoginFailed, audit.Actor{}, audit.Target{Email: email}, nil)
		render(w, "login", map[string]any{"Title": "Sign in", "Error": "invalid email or password", "Email": email})
		return
	}
	_ = s.audit.Record(r.Context(), audit.EventLogin, audit.Actor{ID: session.OwnerID, Email: email}, audit.Target{}, nil)
	ownerauth.SetCookie(w, session)
	http.Redirect(w, r, "/admin/ui", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(ownerauth.SessionCookieName); err == nil {
		if owner, err := s.ownerAuth.ValidateSession(r.Context(), c.Value); err == nil {
			_ = s.audit.Record(r.Context(), audit.EventLogout, audit.Actor{ID: owner.ID, Email: owner.Email}, audit.Target{}, nil)
		}
		_ = s.ownerAuth.Logout(r.Context(), c.Value)
	}
	ownerauth.ClearCookie(w)
	http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	render(w, "dashboard", map[string]any{"Title": "Dashboard", "Owner": ownerFromContext(r.Context())})
}

// --- owners -------------------------------------------------------------

func (s *Server) handleOwnersPage(w http.ResponseWriter, r *http.Request) {
	owners, err := s.ownerAuth.ListOwners(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	render(w, "owners", map[string]any{"Title": "Owners", "Owner": ownerFromContext(r.Context()), "Owners": owners})
}

func (s *Server) handleCreateOwner(w http.ResponseWriter, r *http.Request) {
	actor := ownerFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	created, err := s.ownerAuth.CreateOwner(r.Context(), r.FormValue("email"), r.FormValue("password"), ownerauth.Role(r.FormValue("role")))
	if err != nil {
		s.renderOwnersWithError(w, r, actor, err)
		return
	}
	_ = s.audit.Record(r.Context(), audit.EventOwnerCreate,
		audit.Actor{ID: actor.ID, Email: actor.Email}, audit.Target{ID: created.ID, Email: created.Email},
		map[string]any{"role": string(created.Role)})
	http.Redirect(w, r, "/admin/ui/owners", http.StatusSeeOther)
}

func (s *Server) handleDeleteOwner(w http.ResponseWriter, r *http.Request) {
	actor := ownerFromContext(r.Context())
	deleted, err := s.ownerAuth.DeleteOwner(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.renderOwnersWithError(w, r, actor, err)
		return
	}
	_ = s.audit.Record(r.Context(), audit.EventOwnerDelete,
		audit.Actor{ID: actor.ID, Email: actor.Email}, audit.Target{ID: deleted.ID, Email: deleted.Email},
		map[string]any{"role": string(deleted.Role)})
	http.Redirect(w, r, "/admin/ui/owners", http.StatusSeeOther)
}

func (s *Server) renderOwnersWithError(w http.ResponseWriter, r *http.Request, actor *ownerauth.Owner, err error) {
	owners, _ := s.ownerAuth.ListOwners(r.Context())
	render(w, "owners", map[string]any{"Title": "Owners", "Owner": actor, "Owners": owners, "Error": err.Error()})
}

// --- audit log ------------------------------------------------------------

func (s *Server) handleAuditLogPage(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	entries, err := s.audit.List(r.Context(), limit, offset)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	render(w, "audit_log", map[string]any{
		"Title": "Audit log", "Owner": ownerFromContext(r.Context()),
		"Entries": entries, "Limit": limit, "Offset": offset,
	})
}

// --- maintenance ------------------------------------------------------------

func (s *Server) handleMaintenancePage(w http.ResponseWriter, r *http.Request) {
	render(w, "maintenance", map[string]any{"Title": "Maintenance", "Owner": ownerFromContext(r.Context())})
}

// handlePurgeSessions mirrors internal/server's JSON
// POST /admin/maintenance/purge-sessions handler — see spec/maintenance.md
// MAINT-01..03, which this reuses exactly (same PurgeExpiredSessions
// calls, same audit event).
func (s *Server) handlePurgeSessions(w http.ResponseWriter, r *http.Request) {
	owner := ownerFromContext(r.Context())

	sessionsPurged, err := s.auth.PurgeExpiredSessions(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ownerSessionsPurged, err := s.ownerAuth.PurgeExpiredSessions(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.audit.Record(r.Context(), audit.EventSessionsPurged, audit.Actor{ID: owner.ID, Email: owner.Email}, audit.Target{}, map[string]any{
		"sessions": sessionsPurged, "owner_sessions": ownerSessionsPurged,
	})
	render(w, "maintenance", map[string]any{
		"Title": "Maintenance", "Owner": owner, "Purged": true,
		"SessionsPurged": sessionsPurged, "OwnerSessionsPurged": ownerSessionsPurged,
	})
}

// --- data browser -----------------------------------------------------------

func (s *Server) handleDataCollections(w http.ResponseWriter, r *http.Request) {
	render(w, "data_collections", map[string]any{
		"Title": "Data", "Owner": ownerFromContext(r.Context()), "Collections": s.restapi.AdminCollections(),
	})
}

func (s *Server) handleDataRows(w http.ResponseWriter, r *http.Request) {
	owner := ownerFromContext(r.Context())
	col, ok := s.restapi.AdminCollection(chi.URLParam(r, "collection"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	limit, offset := parsePagination(r)
	rows, err := s.restapi.AdminListRows(r.Context(), col, limit, offset)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	total, err := s.restapi.AdminCountRows(r.Context(), col)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	render(w, "data_rows", map[string]any{
		"Title": col.Name, "Owner": owner, "Collection": col,
		"Rows": rows, "Limit": limit, "Offset": offset, "Total": total,
		"CanWrite": owner.Role.AtLeast(ownerauth.RoleDeveloper),
	})
}

func (s *Server) handleDataCreate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "collection")
	col, ok := s.restapi.AdminCollection(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if _, err := s.restapi.AdminCreateRow(r.Context(), col, formToBody(r, col)); err != nil {
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/ui/data/"+name, http.StatusSeeOther)
}

func (s *Server) handleDataEditForm(w http.ResponseWriter, r *http.Request) {
	col, ok := s.restapi.AdminCollection(chi.URLParam(r, "collection"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	row, err := s.restapi.AdminGetRow(r.Context(), col, chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	render(w, "data_row_edit", map[string]any{
		"Title": "Edit " + col.Name, "Owner": ownerFromContext(r.Context()), "Collection": col, "Row": row,
	})
}

func (s *Server) handleDataUpdate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "collection")
	col, ok := s.restapi.AdminCollection(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if _, err := s.restapi.AdminUpdateRow(r.Context(), col, chi.URLParam(r, "id"), formToBody(r, col)); err != nil {
		http.Error(w, "update failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/ui/data/"+name, http.StatusSeeOther)
}

// handleDataDelete supports two response shapes: an htmx request (the
// data_rows.html delete button) gets a bare 200 so hx-swap removes the
// row in place; anything else (a JS-disabled fallback, or a direct API
// caller) gets redirected back to the list, same as every other write
// handler here.
func (s *Server) handleDataDelete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "collection")
	col, ok := s.restapi.AdminCollection(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := s.restapi.AdminDeleteRow(r.Context(), col, chi.URLParam(r, "id"))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "delete failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	if r.Header.Get("HX-Request") != "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/ui/data/"+name, http.StatusSeeOther)
}

// formToBody builds a create/update body from r's already-parsed form,
// including only col's real columns — an unknown form field is simply
// ignored rather than rejected, since this is an HTML form a human just
// filled in, not an API contract worth 400ing over a typo.
func formToBody(r *http.Request, col *restapi.Collection) map[string]any {
	body := map[string]any{}
	for _, c := range col.Columns {
		if r.Form.Has(c.Name) {
			body[c.Name] = r.FormValue(c.Name)
		}
	}
	return body
}

// --- auth middleware --------------------------------------------------------

type ctxKey string

const ctxKeyOwner ctxKey = "adminui_owner"

// requireRole authenticates the owner-plane session cookie, redirecting
// to the login page if it's missing or invalid (ADMINUI-01) — never a
// JSON 401, since this is a browser surface — and rendering a 403 page
// if the signed-in owner's role doesn't meet min (ADMINUI-05).
func (s *Server) requireRole(min ownerauth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(ownerauth.SessionCookieName)
			if err != nil {
				http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
				return
			}
			owner, err := s.ownerAuth.ValidateSession(r.Context(), c.Value)
			if err != nil {
				http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
				return
			}
			if !owner.Role.AtLeast(min) {
				w.WriteHeader(http.StatusForbidden)
				render(w, "forbidden", map[string]any{"Title": "Forbidden", "Owner": owner})
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyOwner, owner)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func ownerFromContext(ctx context.Context) *ownerauth.Owner {
	o, _ := ctx.Value(ctxKeyOwner).(*ownerauth.Owner)
	return o
}

func render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func parsePagination(r *http.Request) (limit, offset int) {
	limit, offset = defaultLimit, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxLimit {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}
