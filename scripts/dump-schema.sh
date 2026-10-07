#!/usr/bin/env bash
#
# Rebuild api/schema.sql by applying every migration to a throwaway database.
#
# schema.sql is the readable single source of truth a new developer reads top to
# bottom. goose migrations are a chronological list and do not serve that
# purpose. CI runs this and fails on a diff, which is what stops the file
# drifting (backend-standards.md 9).
#
# The Postgres client tools must be at least the server version. Set
# QAVIA_PG_IN_DOCKER=1 to run psql and pg_dump inside the compose container
# instead of using whatever happens to be installed on the host, which is the
# normal local setup. CI installs a matching client and leaves it unset.

set -euo pipefail

cd "$(dirname "$0")/.."

: "${DATABASE_URL:?DATABASE_URL must be set}"

SCRATCH_DB="qavia_schema_dump_$$"

# goose always runs on the host, so its URL comes from DATABASE_URL.
HOST_BASE="${DATABASE_URL%/*}"
GOOSE_URL="${HOST_BASE}/${SCRATCH_DB}?sslmode=disable"

if [[ "${QAVIA_PG_IN_DOCKER:-0}" == "1" ]]; then
  RUN=(docker compose -f infra/docker/compose.yaml exec -T postgres)
  CLIENT_BASE="postgres://qavia:qavia@localhost:5432"
else
  RUN=()
  CLIENT_BASE="$HOST_BASE"
fi

ADMIN_URL="${CLIENT_BASE}/postgres?sslmode=disable"
CLIENT_URL="${CLIENT_BASE}/${SCRATCH_DB}?sslmode=disable"

cleanup() {
  "${RUN[@]}" psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS ${SCRATCH_DB};" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${RUN[@]}" psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS ${SCRATCH_DB};"
"${RUN[@]}" psql "$ADMIN_URL" -q -c "CREATE DATABASE ${SCRATCH_DB};"

(cd api && goose -dir migrations postgres "$GOOSE_URL" up >/dev/null)

{
  cat <<'HEADER'
-- Qavia schema. GENERATED FILE, do not edit.
--
-- Rebuilt by scripts/dump-schema.sh, which applies every migration in
-- api/migrations to a scratch database and dumps the result. CI regenerates it
-- and fails on a diff, so a hand-edited migration cannot merge.
--
-- Read this top to bottom to understand the data model. Change it by writing a
-- migration.

HEADER

  # Dropped lines, in order: pg_dump comments, session SET statements, the
  # set_config preamble, and \restrict / \unrestrict. The last two carry a random
  # nonce per dump, so keeping them would flip the diff on every run and turn the
  # CI check into noise instead of a signal.
  "${RUN[@]}" pg_dump "$CLIENT_URL" \
    --schema-only \
    --no-owner \
    --no-privileges \
    --no-comments \
    --exclude-table=goose_db_version \
    --exclude-table=goose_db_version_id_seq \
    | grep -Ev '^(--|SET |SELECT pg_catalog\.set_config|\\restrict|\\unrestrict)' \
    | cat -s
} > api/schema.sql

echo "wrote api/schema.sql"
