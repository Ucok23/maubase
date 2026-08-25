-- File storage: metadata for uploaded files. The bytes themselves live on
-- disk (see internal/storage.LocalBackend), addressed by storage_key —
-- kept separate from id so a future non-local backend (S3, etc.) can use
-- its own key scheme without a migration. This table is deliberately
-- reserved from auto-REST (internal/restapi's reservedTables): uploads
-- and downloads need multipart/binary handling generic JSON CRUD doesn't
-- do, so internal/storage mounts its own /api/storage/files routes
-- instead.

CREATE TABLE IF NOT EXISTS files (
    id           TEXT PRIMARY KEY,
    owner_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename     TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL,
    storage_key  TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_files_owner_id ON files(owner_id);
