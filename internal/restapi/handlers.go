package restapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"maubase/internal/oauth"
	"maubase/internal/realtime"
)

const (
	scopeRead  = "records:read"
	scopeWrite = "records:write"

	defaultLimit = 50
	maxLimit     = 200
)

type Server struct {
	db *sql.DB
	// registry is an atomic pointer rather than a plain field: the admin
	// UI's "create table" and SQL Studio (internal/adminui) can add
	// tables at runtime, and ReloadSchema swaps in a freshly discovered
	// Registry so those show up in auto-REST without a restart — see
	// Registry's own doc comment, which used to say schema changes
	// needed one.
	registry atomic.Pointer[Registry]
	oauth    *oauth.Server
	// broker is nil-able: a caller that doesn't want realtime fan-out
	// (or hasn't built internal/realtime in yet) can pass nil, and
	// publish becomes a no-op. See spec/realtime.md.
	broker *realtime.Broker
}

func NewServer(db *sql.DB, registry *Registry, oauthSvc *oauth.Server, broker *realtime.Broker) *Server {
	s := &Server{db: db, oauth: oauthSvc, broker: broker}
	s.registry.Store(registry)
	return s
}

// ReloadSchema re-runs Discover and swaps it in atomically, so a table
// created or dropped after startup (via internal/adminui's "create
// table" or SQL Studio) is reflected in auto-REST without a restart.
func (s *Server) ReloadSchema(ctx context.Context) error {
	reg, err := Discover(ctx, s.db)
	if err != nil {
		return fmt.Errorf("reload schema: %w", err)
	}
	s.registry.Store(reg)
	return nil
}

func (s *Server) reg() *Registry {
	return s.registry.Load()
}

// publish hands ev to the realtime broker, if there is one — except when
// col.ReadRule is RuleDenied, in which case it does nothing at all: no
// caller is authorized to read that collection, so no subscriber ever
// should have been able to see this event either (spec/realtime.md
// RT-06). ownerID is "" for a RuleShared read (every current subscriber
// of the collection is authorized), or the row's owner_id otherwise
// (only the subscriber with that subject is) — see
// realtime.Broker.Publish.
func (s *Server) publish(col *Collection, ev realtime.Event, ownerID string) {
	if s.broker == nil || col.ReadRule == RuleDenied {
		return
	}
	s.broker.Publish(ev, ownerID)
}

// rowOwnerID returns the value that should gate realtime visibility of an
// event on col: the acting subject if col's *read* rule is RuleOwner
// (only that subject could ever GET this row), or "" if reads are
// RuleShared (every subscriber authorized to read the collection could
// GET it, regardless of who wrote it) — matching REST-OWNERSHIP-01/02's
// visibility exactly, not just whether the table happens to have an
// owner_id column.
func rowOwnerID(col *Collection, subj string) string {
	if col.ReadRule == RuleOwner {
		return subj
	}
	return ""
}

// Mount registers /api/data/{collection}[/{id}] onto r. Every method is
// gated by an OAuth access token with the appropriate scope (records:read
// for GET, records:write for everything else) via s.oauth.RequireScope —
// there is no anonymous access to any collection.
func (s *Server) Mount(r chi.Router) {
	r.Route("/api/data/{collection}", func(r chi.Router) {
		r.Get("/", s.oauth.RequireScope(scopeRead, s.handleList))
		r.Get("/{id}", s.oauth.RequireScope(scopeRead, s.handleGet))
		r.Post("/", s.oauth.RequireScope(scopeWrite, s.handleCreate))
		r.Patch("/{id}", s.oauth.RequireScope(scopeWrite, s.handleUpdate))
		r.Delete("/{id}", s.oauth.RequireScope(scopeWrite, s.handleDelete))
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	col, ok := s.collection(w, r)
	if !ok {
		return
	}
	if !s.authorizeOp(w, col.ReadRule) {
		return
	}
	limit, offset := parsePagination(r)

	query := fmt.Sprintf("SELECT * FROM %s", quoteIdent(col.Name))
	var args []any
	if col.ReadRule == RuleOwner {
		query += fmt.Sprintf(" WHERE %s = ?", quoteIdent(col.OwnerColumn))
		args = append(args, subject(r))
	}
	query += " LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	records, err := scanRows(rows)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "limit": limit, "offset": offset})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	col, ok := s.collection(w, r)
	if !ok {
		return
	}
	if !s.authorizeOp(w, col.ReadRule) {
		return
	}
	record, err := s.fetchByPK(r.Context(), col, chi.URLParam(r, "id"), col.ReadRule == RuleOwner, subject(r))
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	col, ok := s.collection(w, r)
	if !ok {
		return
	}
	if !s.authorizeOp(w, col.CreateRule) {
		return
	}
	body, ok := decodeBody(w, r, col)
	if !ok {
		return
	}

	// The owner (if any) is always the token's subject, never whatever
	// the client sent — this is what makes row-level ownership actually
	// enforceable rather than advisory. Unaffected by CreateRule: a
	// RuleShared create widens *who* may create, never what the created
	// row's ownership is (spec/access-rules.md ACCESS-04).
	if col.OwnerColumn != "" {
		body[col.OwnerColumn] = subject(r)
	}

	// TEXT primary keys are self-managed (generated here if the client
	// didn't supply one); INTEGER ones are left for SQLite's rowid unless
	// the client explicitly supplied a value.
	if !col.PKIsInteger {
		if v, has := body[col.PKColumn]; !has || v == nil || v == "" {
			body[col.PKColumn] = uuid.NewString()
		}
	}

	cols := make([]string, 0, len(body))
	placeholders := make([]string, 0, len(body))
	vals := make([]any, 0, len(body))
	for k, v := range body {
		cols = append(cols, quoteIdent(k))
		placeholders = append(placeholders, "?")
		vals = append(vals, v)
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		quoteIdent(col.Name), strings.Join(cols, ", "), strings.Join(placeholders, ", "))

	res, err := s.db.ExecContext(r.Context(), query, vals...)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "already exists"})
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var pkValue any
	if col.PKIsInteger {
		id, err := res.LastInsertId()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		pkValue = id
	} else {
		pkValue = body[col.PKColumn]
	}

	// Re-fetch rather than echo the input back, so the response reflects
	// whatever the database actually stored (defaults, affinity coercion,
	// etc.), not just what was sent. Unscoped: the INSERT that just
	// succeeded already proves the caller is entitled to this exact row.
	record, err := s.fetchByPK(r.Context(), col, pkValue, false, "")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.publish(col, realtime.Event{Type: "created", Collection: col.Name, Record: record}, rowOwnerID(col, subject(r)))
	writeJSON(w, http.StatusCreated, record)
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	col, ok := s.collection(w, r)
	if !ok {
		return
	}
	if !s.authorizeOp(w, col.UpdateRule) {
		return
	}
	body, ok := decodeBody(w, r, col)
	if !ok {
		return
	}
	sentSomething := len(body) > 0

	// Both are immutable via the client: the primary key never changes,
	// and ownership can't be transferred by editing the field. Silently
	// dropped, not rejected — a client that echoes a whole fetched record
	// back on PATCH (a common pattern) shouldn't get an error just for
	// including its id/owner_id.
	delete(body, col.PKColumn)
	if col.OwnerColumn != "" {
		delete(body, col.OwnerColumn)
	}

	id := chi.URLParam(r, "id")

	if len(body) == 0 {
		if !sentSomething {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no updatable fields in request body"})
			return
		}
		// Everything sent was immutable and got stripped: a legitimate
		// no-op, not an error, but existence/ownership still gate it —
		// PATCHing someone else's (or a nonexistent) record is still 404.
		// Scoped by UpdateRule, not ReadRule: this stands in for the
		// update that would have run, so it should be authorized the
		// same way that update would have been.
		record, err := s.fetchByPK(r.Context(), col, id, col.UpdateRule == RuleOwner, subject(r))
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, record)
		return
	}

	sets := make([]string, 0, len(body))
	vals := make([]any, 0, len(body)+2)
	for k, v := range body {
		sets = append(sets, quoteIdent(k)+" = ?")
		vals = append(vals, v)
	}
	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?", quoteIdent(col.Name), strings.Join(sets, ", "), quoteIdent(col.PKColumn))
	vals = append(vals, id)
	if col.UpdateRule == RuleOwner {
		query += fmt.Sprintf(" AND %s = ?", quoteIdent(col.OwnerColumn))
		vals = append(vals, subject(r))
	}

	res, err := s.db.ExecContext(r.Context(), query, vals...)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Covers both "no such row" and "exists but isn't yours" — the
		// same 404 either way, so a client can't distinguish someone
		// else's record from one that was never there.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// Unscoped: the UPDATE that just succeeded already proves the caller
	// was entitled to this exact row.
	record, err := s.fetchByPK(r.Context(), col, id, false, "")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.publish(col, realtime.Event{Type: "updated", Collection: col.Name, Record: record}, rowOwnerID(col, subject(r)))
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	col, ok := s.collection(w, r)
	if !ok {
		return
	}
	if !s.authorizeOp(w, col.DeleteRule) {
		return
	}
	id := chi.URLParam(r, "id")
	query := fmt.Sprintf("DELETE FROM %s WHERE %s = ?", quoteIdent(col.Name), quoteIdent(col.PKColumn))
	args := []any{id}
	if col.DeleteRule == RuleOwner {
		query += fmt.Sprintf(" AND %s = ?", quoteIdent(col.OwnerColumn))
		args = append(args, subject(r))
	}

	res, err := s.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	s.publish(col, realtime.Event{Type: "deleted", Collection: col.Name, ID: id}, rowOwnerID(col, subject(r)))
	w.WriteHeader(http.StatusNoContent)
}

// ExportOwned returns every row across every owner-scoped collection that
// belongs to subj, keyed by collection name — the data behind a "give me
// everything you have on me" account export. Shared (non-owner-scoped)
// tables are excluded: their rows aren't specifically the caller's by the
// same ownership convention enforced everywhere else in this package.
func (s *Server) ExportOwned(ctx context.Context, subj string) (map[string][]map[string]any, error) {
	out := map[string][]map[string]any{}
	for _, col := range s.reg().ownedCollections() {
		query := fmt.Sprintf("SELECT * FROM %s WHERE %s = ?", quoteIdent(col.Name), quoteIdent(col.OwnerColumn))
		rows, err := s.db.QueryContext(ctx, query, subj)
		if err != nil {
			return nil, err
		}
		records, err := scanRows(rows)
		if err != nil {
			return nil, err
		}
		out[col.Name] = records
	}
	return out, nil
}

// DeleteOwned deletes every row across every owner-scoped collection that
// belongs to subj. Intended to run as part of account deletion, so no
// application data is left behind, orphaned, once the user it belonged to
// is gone. Also publishes a "deleted" realtime event per row removed this
// way (spec/realtime.md RT-04) — subj is the only subscriber who could
// ever have been authorized to see any of these rows, so that's the only
// one this can notify, same as an ordinary DELETE would.
func (s *Server) DeleteOwned(ctx context.Context, subj string) error {
	for _, col := range s.reg().ownedCollections() {
		var ids []string
		if s.broker != nil {
			var err error
			ids, err = s.pkValuesForOwner(ctx, col, subj)
			if err != nil {
				return err
			}
		}
		query := fmt.Sprintf("DELETE FROM %s WHERE %s = ?", quoteIdent(col.Name), quoteIdent(col.OwnerColumn))
		if _, err := s.db.ExecContext(ctx, query, subj); err != nil {
			return err
		}
		for _, id := range ids {
			s.publish(col, realtime.Event{Type: "deleted", Collection: col.Name, ID: id}, subj)
		}
	}
	return nil
}

// pkValuesForOwner returns the primary-key value of every row col has
// with owner_id = subj, stringified — used only to name rows in realtime
// "deleted" events ahead of a bulk DeleteOwned, since the bulk DELETE
// itself doesn't return which rows it removed.
func (s *Server) pkValuesForOwner(ctx context.Context, col *Collection, subj string) ([]string, error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?", quoteIdent(col.PKColumn), quoteIdent(col.Name), quoteIdent(col.OwnerColumn))
	rows, err := s.db.QueryContext(ctx, query, subj)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		ids = append(ids, fmt.Sprint(v))
	}
	return ids, rows.Err()
}

// --- helpers ---------------------------------------------------------------

// collection resolves the {collection} URL param, writing a 404 (and
// returning ok=false) if it doesn't name an exposed table. This runs
// after RequireScope, so an unauthenticated or wrongly-scoped request
// never learns whether a collection exists at all.
func (s *Server) collection(w http.ResponseWriter, r *http.Request) (*Collection, bool) {
	col, ok := s.reg().Get(chi.URLParam(r, "collection"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such collection"})
		return nil, false
	}
	return col, true
}

// authorizeOp rejects a request whose operation is RuleDenied for its
// collection, writing 403 (and returning ok=false) — distinct from the
// 404 an unknown collection or an ownership violation gets: the
// operation exists and the caller may well be authorized in general, but
// this specific action has been turned off for this collection at the
// API layer. See spec/access-rules.md ACCESS-05/06/07.
func (s *Server) authorizeOp(w http.ResponseWriter, rule Rule) bool {
	if rule == RuleDenied {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this operation is disabled for this collection"})
		return false
	}
	return true
}

// fetchByPK looks up one row by primary key. scopeToOwner adds an
// AND owner_id = subj clause when true — pass col.ReadRule == RuleOwner
// for a genuine read (handleGet, or an update/delete's pre-check standing
// in for one), or false for a lookup right after a write whose own WHERE
// clause already proved the caller was entitled to that exact row.
func (s *Server) fetchByPK(ctx context.Context, col *Collection, pkValue any, scopeToOwner bool, subj string) (map[string]any, error) {
	query := fmt.Sprintf("SELECT * FROM %s WHERE %s = ?", quoteIdent(col.Name), quoteIdent(col.PKColumn))
	args := []any{pkValue}
	if scopeToOwner {
		query += fmt.Sprintf(" AND %s = ?", quoteIdent(col.OwnerColumn))
		args = append(args, subj)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	records, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, sql.ErrNoRows
	}
	return records[0], nil
}

// decodeBody parses the request body as a JSON object and rejects any key
// that isn't a real column on the collection — a typo'd field fails loudly
// instead of silently doing nothing.
func decodeBody(w http.ResponseWriter, r *http.Request, col *Collection) (map[string]any, bool) {
	defer r.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return nil, false
	}
	if body == nil {
		// A literal JSON `null` body decodes successfully but leaves body
		// nil rather than empty (encoding/json's documented behavior for
		// a map destination) — every caller of decodeBody goes on to
		// write into this map (owner-stamping, PK generation), which
		// panics on a nil map. Any other non-object top-level JSON value
		// (an array, a string, a number) fails the Decode above already,
		// since json.Unmarshal refuses to put those into a map — null is
		// the one value that decodes into "no error, but also no map."
		body = map[string]any{}
	}
	for k := range body {
		if !col.HasColumn(k) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown field %q", k)})
			return nil, false
		}
	}
	return body, true
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

// subject returns the access token's subject (user id), set by
// oauth.RequireScope earlier in the middleware chain.
func subject(r *http.Request) string {
	s, _ := oauth.SubjectFromContext(r.Context())
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
