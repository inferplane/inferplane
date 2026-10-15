#!/bin/sh
# One-shot task. ECS injects credentials; no secret is read by deploy tooling.
set -eu
: "${APP_DB_PASSWORD:?APP_DB_PASSWORD must be injected by ECS}"
: "${PGPASSWORD:?PGPASSWORD must be injected by ECS}"
: "${PGHOST:?PGHOST is required}"
: "${PGSSLMODE:?PGSSLMODE is required}"
[ "$PGSSLMODE" = verify-full ] || { echo "TLS verification required" >&2; exit 1; }
case "$APP_DB_PASSWORD" in
  *[!a-zA-Z0-9]*) echo "Invalid generated application password" >&2; exit 1 ;;
esac
[ "${#APP_DB_PASSWORD}" -eq 40 ] || { echo "Invalid generated application password length" >&2; exit 1; }

# \getenv keeps both passwords out of process arguments and SQL source files.
# psql quotes the variable as a SQL literal and never echoes the generated SQL.
if ! psql --no-psqlrc --quiet --set=ON_ERROR_STOP=1 > /dev/null 2>/dev/null <<'SQL'
\getenv app_password APP_DB_PASSWORD
BEGIN;
SELECT format('CREATE ROLE inferplane_cp LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'inferplane_cp')
\gexec
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE inferplane TO inferplane_cp;
GRANT USAGE, CREATE ON SCHEMA public TO inferplane_cp;
COMMIT;
SQL
then
  echo "Policy database bootstrap failed; details withheld to protect credentials" >&2
  exit 1
fi
export PGUSER=inferplane_cp PGPASSWORD="$APP_DB_PASSWORD"
unset APP_DB_PASSWORD
if ! psql --no-psqlrc --quiet --set=ON_ERROR_STOP=1 -c 'SELECT 1' > /dev/null 2>/dev/null; then
  echo "Application database authentication failed" >&2
  exit 1
fi
echo "Policy database bootstrap complete"
