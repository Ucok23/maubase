-- Row-level access-rule overrides for auto-REST collections (see
-- spec/access-rules.md). Reserved out of auto-REST itself
-- (internal/restapi/registry.go's reservedTables), same as `files` —
-- there's no /api/data/_policies. A deployment declares rows here via
-- its own application migrations (internal/db.MigrateDir), the same
-- "no separate admin API in v1" convention spec/auto-rest.md's own
-- tables already follow.
CREATE TABLE IF NOT EXISTS _policies (
    collection TEXT NOT NULL,
    operation  TEXT NOT NULL CHECK (operation IN ('read', 'create', 'update', 'delete')),
    rule       TEXT NOT NULL CHECK (rule IN ('owner', 'shared', 'denied')),
    PRIMARY KEY (collection, operation)
);
