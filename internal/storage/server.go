package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"maubase/internal/oauth"
)

const (
	scopeRead  = "files:read"
	scopeWrite = "files:write"

	defaultLimit = 50
	maxLimit     = 200
)

// ErrNotFound covers both "no such file" and "not this caller's file" —
// deliberately indistinguishable to an API caller, same as
// internal/restapi's row-scoped lookups.
var ErrNotFound = errors.New("file not found")

// FileMeta is a file's metadata row — everything except the bytes
// themselves, which live in a Backend addressed by ID.
type FileMeta struct {
	ID          string
	OwnerID     string
	Filename    string
	ContentType string
	SizeBytes   int64
	CreatedAt   time.Time
}

// Server is the file storage layer's HTTP surface plus its metadata
// storage — the pattern internal/restapi.Server also uses, as opposed to
// internal/auth's split of a business-logic Service from HTTP handlers
// living in internal/server: a self-contained resource like this one
// doesn't need that split.
type Server struct {
	db             *sql.DB
	backend        Backend
	oauth          *oauth.Server
	maxUploadBytes int64
}

func NewServer(db *sql.DB, backend Backend, oauthSvc *oauth.Server, maxUploadBytes int64) *Server {
	return &Server{db: db, backend: backend, oauth: oauthSvc, maxUploadBytes: maxUploadBytes}
}

// Mount registers /api/storage/files[/{id}[/content]] onto r. Every
// method requires an OAuth access token with the appropriate scope
// (files:read for GET, files:write for everything else) — there is no
// anonymous access, matching internal/restapi.
func (s *Server) Mount(r chi.Router) {
	r.Route("/api/storage/files", func(r chi.Router) {
		r.Get("/", s.oauth.RequireScope(scopeRead, s.handleList))
		r.Post("/", s.oauth.RequireScope(scopeWrite, s.handleUpload))
		r.Get("/{id}", s.oauth.RequireScope(scopeRead, s.handleGet))
		r.Get("/{id}/content", s.oauth.RequireScope(scopeRead, s.handleDownload))
		r.Delete("/{id}", s.oauth.RequireScope(scopeWrite, s.handleDelete))
	})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	ownerID, _ := oauth.SubjectFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, s.maxUploadBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart upload, or file too large"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `missing "file" field`})
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	id := uuid.NewString()
	size, err := s.backend.Put(r.Context(), id, file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO files (id, owner_id, filename, content_type, size_bytes, storage_key)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, ownerID, header.Filename, contentType, size, id)
	if err != nil {
		_ = s.backend.Delete(r.Context(), id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	meta, err := s.get(r.Context(), ownerID, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusCreated, fileJSON(meta))
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	ownerID, _ := oauth.SubjectFromContext(r.Context())
	limit, offset := parsePagination(r)

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, owner_id, filename, content_type, size_bytes, created_at
		FROM files WHERE owner_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?
	`, ownerID, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	files, err := scanFiles(rows)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	out := make([]map[string]any, len(files))
	for i, f := range files {
		out[i] = fileJSON(f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": out, "limit": limit, "offset": offset})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	ownerID, _ := oauth.SubjectFromContext(r.Context())
	meta, err := s.get(r.Context(), ownerID, chi.URLParam(r, "id"))
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, fileJSON(meta))
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	ownerID, _ := oauth.SubjectFromContext(r.Context())
	meta, err := s.get(r.Context(), ownerID, chi.URLParam(r, "id"))
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	rc, err := s.backend.Open(r.Context(), meta.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.SizeBytes, 10))
	// mime.FormatMediaType (not a hand-built fmt.Sprintf(`filename=%q`,
	// ...)) so an uploaded filename with a quote or backslash gets a
	// correctly-escaped quoted-string, and one with a control character
	// or non-ASCII text (RFC 6266's `filename=` is otherwise limited to
	// ASCII) gets the RFC 5987/8187 `filename*=utf-8''<percent-encoded>`
	// form automatically instead. The storage key itself is always a
	// fresh UUID (see s.backend.Put above), so there's no path-traversal
	// risk from meta.Filename either way — this is purely about the
	// header rendering correctly for the browser's Save dialog.
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": meta.Filename}))
	_, _ = io.Copy(w, rc)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	ownerID, _ := oauth.SubjectFromContext(r.Context())
	id := chi.URLParam(r, "id")

	meta, err := s.get(r.Context(), ownerID, id)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.backend.Delete(r.Context(), meta.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM files WHERE id = ?`, meta.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) get(ctx context.Context, ownerID, id string) (*FileMeta, error) {
	var m FileMeta
	err := s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, filename, content_type, size_bytes, created_at
		FROM files WHERE id = ? AND owner_id = ?
	`, id, ownerID).Scan(&m.ID, &m.OwnerID, &m.Filename, &m.ContentType, &m.SizeBytes, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup file: %w", err)
	}
	return &m, nil
}

// ExportOwned lists metadata for every file subj owns, for account export
// (GET /api/auth/me/export — see spec/identity.md IDNT-09). Raw bytes
// aren't included; a caller downloads those individually via
// GET /api/storage/files/{id}/content.
func (s *Server) ExportOwned(ctx context.Context, subj string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner_id, filename, content_type, size_bytes, created_at
		FROM files WHERE owner_id = ? ORDER BY created_at
	`, subj)
	if err != nil {
		return nil, fmt.Errorf("list owned files: %w", err)
	}
	files, err := scanFiles(rows)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(files))
	for i, f := range files {
		out[i] = fileJSON(f)
	}
	return out, nil
}

// DeleteOwned removes every file (bytes and metadata) subj owns, for
// account erasure (DELETE /api/auth/me — see spec/identity.md IDNT-10).
func (s *Server) DeleteOwned(ctx context.Context, subj string) error {
	// Ordered so a partial failure's "everything before the failure point
	// is fully cleaned, everything from it onward is untouched" guarantee
	// (see the loop below) is deterministic — upload order — rather than
	// whatever unspecified order a plain table scan happens to return.
	// rowid (files.id is a TEXT primary key, so SQLite still maintains a
	// hidden rowid alongside it) breaks created_at ties in actual
	// insertion order — CURRENT_TIMESTAMP's one-second resolution means
	// uploads seconds apart can easily collide, and ordering by id (a
	// random UUID) on tie would make processing order effectively random.
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM files WHERE owner_id = ? ORDER BY created_at, rowid`, subj)
	if err != nil {
		return fmt.Errorf("list owned files: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan file id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	// Bytes and metadata are deleted per file, immediately after each
	// other, rather than deleting every file's bytes in a loop and only
	// then bulk-deleting all the metadata rows at the end. That ordering
	// meant a failure partway through (a permissions error, or bytes
	// already gone in a way Delete doesn't tolerate — see its own
	// os.IsNotExist handling) left every file's metadata row in place
	// regardless of which ones had already had their bytes removed:
	// GET /api/storage/files/{id} would still 200 for a file whose bytes
	// were already gone, with .../content then 500ing instead of 404ing.
	// Per-file ordering means a failure on file N leaves files before it
	// fully cleaned (bytes and metadata both gone) and file N onward
	// untouched (metadata still there, so still discoverable and
	// retryable) — a retry of the whole call converges correctly instead
	// of leaving orphaned metadata forever.
	for _, id := range ids {
		if err := s.backend.Delete(ctx, id); err != nil {
			return fmt.Errorf("delete file bytes %s: %w", id, err)
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete file metadata %s: %w", id, err)
		}
	}
	return nil
}

func scanFiles(rows *sql.Rows) ([]*FileMeta, error) {
	defer rows.Close()
	var out []*FileMeta
	for rows.Next() {
		var m FileMeta
		if err := rows.Scan(&m.ID, &m.OwnerID, &m.Filename, &m.ContentType, &m.SizeBytes, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func fileJSON(m *FileMeta) map[string]any {
	return map[string]any{
		"id":           m.ID,
		"filename":     m.Filename,
		"content_type": m.ContentType,
		"size_bytes":   m.SizeBytes,
		"created_at":   m.CreatedAt,
	}
}

// parsePagination is internal/restapi's parsePagination, duplicated here
// rather than shared across packages for a helper this small: ?limit=&
// offset=, defaulting to defaultLimit/0, an out-of-range limit falling
// back to the default rather than being clamped.
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
