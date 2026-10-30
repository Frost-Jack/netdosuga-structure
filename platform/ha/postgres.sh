#!/bin/sh
set -eu
mkdir -p /data
chown postgres:postgres /data
# On fresh volumes the explicit persisted router target chooses bootstrap primary.
# Existing data and manual promotion state always win; this is not an election.
if [ ! -s "$PGDATA/PG_VERSION" ] && [ "${RECLONE_FROM:-}" = "" ]; then
  INITIAL_PRIMARY=$(awk '$1 == "server" && $2 == "node0" {split($3,a,":"); print a[1]}' /ha/postgres.cfg)
  case "$INITIAL_PRIMARY" in pg-a|pg-b) ;; *) echo 'Invalid bootstrap route' >&2; exit 1 ;; esac
fi
if [ "${RECLONE_FROM:-}" != "" ]; then
  if [ -d "$PGDATA" ]; then mv "$PGDATA" "/data/before-rejoin-$(date +%s)"; fi
  export INITIAL_PRIMARY="$RECLONE_FROM"
fi
# A fenced former primary must never return as an independent writable node.
# The explicit rejoin path archives it before this check and creates standby.signal.
ROUTED_PRIMARY=$(awk '$1 == "server" && $2 == "node0" {split($3,a,":"); print a[1]}' /ha/postgres.cfg)
case "$ROUTED_PRIMARY" in pg-a|pg-b) ;; *) echo 'Invalid persisted primary route' >&2; exit 1 ;; esac
if [ -s "$PGDATA/PG_VERSION" ] && [ "$NODE_NAME" != "$ROUTED_PRIMARY" ] && [ ! -f "$PGDATA/standby.signal" ]; then
  echo "Refusing to start fenced former primary $NODE_NAME; run explicit rejoin from $ROUTED_PRIMARY" >&2
  exit 1
fi
if [ ! -s "$PGDATA/PG_VERSION" ] && [ "$NODE_NAME" != "$INITIAL_PRIMARY" ]; then
  until PGPASSWORD="$POSTGRES_PASSWORD" pg_isready -h "$INITIAL_PRIMARY" -U demo -d demo; do sleep 1; done
  export PGPASSWORD="$POSTGRES_PASSWORD"
  gosu postgres pg_basebackup -d "host=$INITIAL_PRIMARY user=demo application_name=$NODE_NAME" -D "$PGDATA" -R -X stream
fi
[ "${RECLONE_ONLY:-}" != "true" ] || exit 0
# Bootstrap needs an asynchronous primary until the first basebackup completes.
# A persisted synchronous primary keeps its setting while the replica reconnects.
(
  export PGPASSWORD="$POSTGRES_PASSWORD"
  until pg_isready -h 127.0.0.1 -U demo -d demo >/dev/null 2>&1; do sleep 1; done
  while :; do
    role=$(psql -h 127.0.0.1 -U demo -d demo -Atqc 'select pg_is_in_recovery()' 2>/dev/null || true)
    [ "$role" != t ] || exit 0
    streaming=$(psql -h 127.0.0.1 -U demo -d demo -Atqc "select count(*) from pg_stat_replication where state='streaming'" 2>/dev/null || true)
    if [ "$role" = f ] && [ "${streaming:-0}" -gt 0 ]; then
      psql -h 127.0.0.1 -U demo -d demo -v ON_ERROR_STOP=1 -c "ALTER SYSTEM SET synchronous_standby_names = '*'" -c 'SELECT pg_reload_conf()' >/dev/null
      exit 0
    fi
    sleep 1
  done
) &
exec /usr/local/bin/docker-entrypoint.sh postgres -c wal_level=replica -c max_wal_senders=10 -c hot_standby=on -c synchronous_commit=remote_apply
