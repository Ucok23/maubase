package restapi

import "database/sql"

// scanRows reads every row into a JSON-ready map keyed by column name.
// Returns an empty (never nil) slice so a zero-row result encodes as `[]`
// rather than `null`.
func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rec := make(map[string]any, len(cols))
		for i, c := range cols {
			rec[c] = normalizeValue(vals[i])
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// normalizeValue converts driver-returned values into JSON-friendly ones.
// modernc.org/sqlite returns TEXT columns as []byte in some paths; there's
// no BLOB support in v1, so every []byte is assumed to be text.
func normalizeValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}
