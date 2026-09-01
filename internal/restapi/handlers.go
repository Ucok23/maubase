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

	"github.com/Ucok23/maubase/internal/oauth"
	"github.com/Ucok23/maubase/internal/realtime"
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
	// maxBodyBytes caps a single create/update request body's size,
	// matching internal/storage.Server's maxUploadBytes: a larger body is
	// rejected before it's fully read into memory, rather than letting
	// any authenticated records:write caller force the server to buffer
	// an arbitrarily large JSON payload. See decodeBody.
	maxBodyBytes int64
}

func NewServer(db *sql.DB, registry *Registry, oauthSvc *oauth.Server, broker *realtime.Broker, maxBodyBytes int64) *Server {
	s := &Server{db: db, oauth: oauthSvc, broker: broker, maxBodyBytes: maxBodyBytes}
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

// ownerGateValue returns the value that should gate realtime visibility of
// an event on col: the row's actual owner_id (read out of record) if col's
// *read* rule is RuleOwner (only that subject could ever GET this row), or
// "" if reads are RuleShared (every subscriber authorized to read the
// collection could GET it, regardless of who wrote it) — matching
// REST-OWNERSHIP-01/02's visibility exactly, not just whether the table
// happens to have an owner_id column.
//
// This is deliberately the row's owner, not the acting caller's subject:
// col.UpdateRule/DeleteRule can be RuleShared while ReadRule stays at its
// RuleOwner default (spec/access-rules.md ACCESS-02), so a caller writing
// someone else's row is not who is entitled to see it afterward
// (spec/access-rules.md ACCESS-10).
func ownerGateValue(col *Collection, record map[string]any) string {
	if col.ReadRule != RuleOwner || col.OwnerColumn == "" {
		return ""
	}
	v, ok := record[col.OwnerColumn]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
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
	limit, offset, err := parsePagination(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

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
	body, ok := s.decodeBody(w, r, col)
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
		writeWriteError(w, err)
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
	s.publish(col, realtime.Event{Type: "created", Collection: col.Name, Record: record}, ownerGateValue(col, record))
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
	body, ok := s.decodeBody(w, r, col)
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
		writeWriteError(w, err)
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
	s.publish(col, realtime.Event{Type: "updated", Collection: col.Name, Record: record}, ownerGateValue(col, record))
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

	// Captured before the DELETE runs — the row won't exist to query
	// afterward — so the realtime event is gated by the row's actual
	// owner_id rather than DeleteRule's authorization scope. Those can
	// differ (spec/access-rules.md ACCESS-10: e.g. delete: shared with the
	// default read: owner), and it's the row's owner, not whoever deleted
	// it, who was ever entitled to see it. Best-effort: if the lookup
	// fails, gateVal stays "" and the event just won't reach anyone, same
	// as if reads were RuleShared.
	var gateVal string
	if col.ReadRule == RuleOwner && col.OwnerColumn != "" {
		gateVal, _ = s.ownerValueForPK(r.Context(), col, id)
	}

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
	s.publish(col, realtime.Event{Type: "deleted", Collection: col.Name, ID: id}, gateVal)
	w.WriteHeader(http.StatusNoContent)
}

// ExportOwned returns every row across every owner-scoped collection that
// belongs to subj, keyed by collection name — the data behind a "give me
// everything you have on me" account export. Shared (non-owner-scoped)
// tables are excluded: their rows aren't specifically the caller's by the
// same ownership convention enforced everywhere else in this package. A
// collection with read: denied is also excluded (spec/access-rules.md
// ACCESS-06/09): that policy means no caller may read it through this API
// regardless of scope, and export is a read — handing the same data back
// through a different endpoint than the one the policy locked down would
// contradict every other RuleDenied enforcement point in this package.
func (s *Server) ExportOwned(ctx context.Context, subj string) (map[string][]map[string]any, error) {
	out := map[string][]map[string]any{}
	for _, col := range s.reg().ownedCollections() {
		if col.ReadRule == RuleDenied {
			continue
		}
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

// ownerValueForPK returns the value of col's owner column for the row
// identified by pkValue, stringified. Used by handleDelete to capture a
// row's actual owner *before* removing it, since the DELETE itself doesn't
// return column values and the row won't exist to query afterward.
func (s *Server) ownerValueForPK(ctx context.Context, col *Collection, pkValue any) (string, error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?", quoteIdent(col.OwnerColumn), quoteIdent(col.Name), quoteIdent(col.PKColumn))
	var v any
	if err := s.db.QueryRowContext(ctx, query, pkValue).Scan(&v); err != nil {
		return "", err
	}
	v = normalizeValue(v)
	if v == nil {
		return "", nil
	}
	return fmt.Sprint(v), nil
}

// --- helpers ---------------------------------------------------------------

// writeWriteError classifies a failed INSERT/UPDATE and writes the
// response a client should see: a constraint violation SQLite itself
// caught (NOT NULL, CHECK, FOREIGN KEY, UNIQUE) is bad input from the
// caller — 400 or 409, not a 500 indistinguishable from an actual
// internal fault. Anything else (a real driver/connection error) still
// falls through to a generic 500, unchanged from before.
func writeWriteError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already exists"})
	case strings.Contains(msg, "NOT NULL constraint failed"):
		field := fieldFromConstraintError(msg, "NOT NULL constraint failed: ")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("missing required field %q", field)})
	case strings.Contains(msg, "CHECK constraint failed"):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a field's value violates a constraint on this table"})
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a field references a row that doesn't exist"})
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// fieldFromConstraintError pulls the column name out of a SQLite
// constraint-violation message shaped like
// "constraint failed: NOT NULL constraint failed: table.column (1299)" —
// prefix is everything up to and including "NOT NULL constraint failed: ".
// Falls back to "unknown" if the message doesn't have the expected shape
// (a driver upgrade changing the wording, say) — a less specific message
// is fine, mis-parsing into something worse silently isn't.
func fieldFromConstraintError(msg, prefix string) string {
	idx := strings.Index(msg, prefix)
	if idx < 0 {
		return "unknown"
	}
	rest := msg[idx+len(prefix):]
	if end := strings.IndexAny(rest, " ("); end >= 0 {
		rest = rest[:end]
	}
	if dot := strings.LastIndex(rest, "."); dot >= 0 {
		rest = rest[dot+1:]
	}
	return rest
}

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
// instead of silently doing nothing. The body is capped at s.maxBodyBytes
// (via http.MaxBytesReader) before anything reads it, so an oversized
// payload is rejected — 413, not the generic 400 a truncated/invalid JSON
// body gets — without being fully buffered into memory first.
func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, col *Collection) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	defer r.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return nil, false
		}
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
	for k, v := range body {
		if !col.HasColumn(k) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown field %q", k)})
			return nil, false
		}
		// A JSON array or object as a column value has no SQLite column
		// type it could ever bind to — database/sql's driver rejects it
		// at Exec time with an "unsupported type" error naming a
		// positional argument, not a field, which used to surface as an
		// opaque 500. Caught here instead, before the query is even
		// built, with a message that actually names the field.
		switch v.(type) {
		case map[string]any, []any:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("field %q must be a scalar value (string, number, boolean, or null), not an object/array", k)})
			return nil, false
		}
	}
	return body, true
}

// parsePagination reads limit/offset from the query string. Absent
// params silently default (limit to defaultLimit, offset to 0) — that's
// "unset," not invalid, and asking for it is the common case. But a
// param that *is* present and out of range (non-numeric, non-positive,
// or over maxLimit for limit; non-numeric or negative for offset)
// returns an error rather than being silently treated the same as
// absent: a caller explicitly asking for limit=999999 or limit=201 (over
// a stated max of 200) got the *default* back with no indication
// anything was truncated, easy to mistake for "that's everything there
// is" (spec/auto-rest.md REST-PAGINATION-01/02).
func parsePagination(r *http.Request) (limit, offset int, err error) {
	limit, offset = defaultLimit, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n <= 0 {
			return 0, 0, fmt.Errorf("limit must be a positive integer, got %q", v)
		}
		if n > maxLimit {
			return 0, 0, fmt.Errorf("limit must not exceed %d, got %d", maxLimit, n)
		}
		limit = n
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer, got %q", v)
		}
		offset = n
	}
	return limit, offset, nil
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
