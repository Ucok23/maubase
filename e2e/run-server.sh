#!/usr/bin/env bash
# Builds and runs a throwaway maubase instance for the Playwright e2e
# suite: a fresh SQLite DB and storage dir on every run, a fixed port, and
# a bootstrap owner so the admin UI login screen has something to sign in
# with (spec/owner-plane.md OWNR-01). Playwright's webServer config (see
# playwright.config.ts) shells out to this and waits for the login page to
# respond before running any test.
set -euo pipefail
cd "$(dirname "$0")/.."

datadir="e2e/.data"
rm -rf "$datadir"
mkdir -p "$datadir"

export MAUBASE_ADDR="${MAUBASE_ADDR:-:8811}"
export MAUBASE_ISSUER="${MAUBASE_ISSUER:-http://127.0.0.1:8811}"
export MAUBASE_DB_PATH="$datadir/e2e.db"
export MAUBASE_STORAGE_DIR="$datadir/storage"
# Deliberately pointed at a directory that doesn't exist: this e2e suite
# only exercises maubase's own surfaces, no application schema — a missing
# MigrationsDir is fine (see internal/config.Config's doc comment).
export MAUBASE_MIGRATIONS_DIR="$datadir/migrations"
export MAUBASE_BOOTSTRAP_OWNER_EMAIL="${MAUBASE_BOOTSTRAP_OWNER_EMAIL:-owner@e2e.test}"
export MAUBASE_BOOTSTRAP_OWNER_PASSWORD="${MAUBASE_BOOTSTRAP_OWNER_PASSWORD:-e2e-password-123}"

go build -o "$datadir/maubase" ./cmd/maubase
exec "$datadir/maubase" serve
