package restapi

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// The methods in this file back the embedded admin UI's data browser
// (internal/adminui, spec/admin-ui.md ADMINUI-11..15): unscoped by
// owner_id and unaffected by _policies entirely, since an owner
// administering/support-viewing their own deployment's data is a
// different capability from a customer-facing, OAuth-token-authenticated
// request — /api/data/*'s row-scoping and RuleDenied/RuleShared rules
// simply don't apply here. Callers are responsible for their own
// owner-plane role gating; these methods only check that the named
// collection exists.

// AdminCollections returns every collection auto-REST exposes, sorted by
// name.
func (s *Server) AdminCollections() []*Collection {
	return s.registry.All()
}

// AdminCollection resolves a single collection by name.
func (s *Server) AdminCollection(name string) (*Collection, bool) {
	return s.registry.Get(name)
}

// AdminListRows returns every row in col, unfiltered by owner_id,
// paginated.
func (s *Server) AdminListRows(ctx context.Context, col *Collection, limit, offset int) ([]map[string]any, error) {
	query := fmt.Sprintf("SELECT * FROM %s ORDER BY %s LIMIT ? OFFSET ?", quoteIdent(col.Name), quoteIdent(col.PKColumn))
	rows, err := s.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list rows: %w", err)
	}
	return scanRows(rows)
}

// AdminCountRows returns col's total row count, for pagination.
func (s *Server) AdminCountRows(ctx context.Context, col *Collection) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdent(col.Name))).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count rows: %w", err)
	}
	return n, nil
}

// AdminGetRow fetches one row by primary key, unscoped.
func (s *Server) AdminGetRow(ctx context.Context, col *Collection, pkValue any) (map[string]any, error) {
	return s.fetchByPK(ctx, col, pkValue, false, "")
}

// AdminCreateRow inserts a row from body. Unlike the customer-facing
// POST /api/data/{table}, body's owner_id (if any) is used as-is rather
// than overridden to a caller's subject — reassigning ownership directly
// is a deliberate admin-only capability (ADMINUI-13).
func (s *Server) AdminCreateRow(ctx context.Context, col *Collection, body map[string]any) (map[string]any, error) {
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

	res, err := s.db.ExecContext(ctx, query, vals...)
	if err != nil {
		return nil, fmt.Errorf("insert row: %w", err)
	}

	var pkValue any
	if col.PKIsInteger {
		id, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("read inserted id: %w", err)
		}
		pkValue = id
	} else {
		pkValue = body[col.PKColumn]
	}
	return s.AdminGetRow(ctx, col, pkValue)
}

// AdminUpdateRow updates whichever of body's fields are real columns
// other than the primary key, unscoped, and — unlike
// PATCH /api/data/{table}/{id} — allows changing owner_id.
func (s *Server) AdminUpdateRow(ctx context.Context, col *Collection, pkValue any, body map[string]any) (map[string]any, error) {
	delete(body, col.PKColumn)
	if len(body) == 0 {
		return s.AdminGetRow(ctx, col, pkValue)
	}

	sets := make([]string, 0, len(body))
	vals := make([]any, 0, len(body)+1)
	for k, v := range body {
		sets = append(sets, quoteIdent(k)+" = ?")
		vals = append(vals, v)
	}
	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?", quoteIdent(col.Name), strings.Join(sets, ", "), quoteIdent(col.PKColumn))
	vals = append(vals, pkValue)

	if _, err := s.db.ExecContext(ctx, query, vals...); err != nil {
		return nil, fmt.Errorf("update row: %w", err)
	}
	return s.AdminGetRow(ctx, col, pkValue)
}

// AdminDeleteRow deletes one row by primary key, unscoped. Returns
// sql.ErrNoRows if there was no such row.
func (s *Server) AdminDeleteRow(ctx context.Context, col *Collection, pkValue any) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE %s = ?", quoteIdent(col.Name), quoteIdent(col.PKColumn))
	res, err := s.db.ExecContext(ctx, query, pkValue)
	if err != nil {
		return fmt.Errorf("delete row: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
