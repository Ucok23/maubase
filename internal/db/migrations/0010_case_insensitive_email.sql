-- Defense in depth for email case-sensitivity (spec/identity.md IDNT-14,
-- spec/owner-plane.md OWNR-24): internal/auth and internal/ownerauth now
-- normalize (lowercase + trim) email at every write/lookup site, but
-- SQLite's plain UNIQUE on users.email / owner_users.email is still
-- case-sensitive at the schema level. A unique index with COLLATE NOCASE
-- enforces case-insensitive uniqueness independently of application-layer
-- normalization — e.g. against a raw INSERT via SQL Studio that bypasses
-- internal/auth entirely. SQLite can't ALTER COLUMN to add collation to
-- an existing column, so this is a second unique index rather than a
-- change to the original UNIQUE constraint; either one failing rejects
-- the insert.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_nocase ON users(email COLLATE NOCASE);
CREATE UNIQUE INDEX IF NOT EXISTS idx_owner_users_email_nocase ON owner_users(email COLLATE NOCASE);
