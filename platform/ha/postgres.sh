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
if [ ! -s "$PGDATA/PG_VERSION" ] && [ "$NODE_NAME" != "$INITIAL_PRIMARY" ]; then
  until PGPASSWORD="$POSTGRES_PASSWORD" pg_isready -h "$INITIAL_PRIMARY" -U demo -d demo; do sleep 1; done
  export PGPASSWORD="$POSTGRES_PASSWORD"
  gosu postgres pg_basebackup -d "host=$INITIAL_PRIMARY user=demo application_name=$NODE_NAME" -D "$PGDATA" -R -X stream
fi
[ "${RECLONE_ONLY:-}" != "true" ] || exit 0
exec /usr/local/bin/docker-entrypoint.sh postgres -c wal_level=replica -c max_wal_senders=10 -c hot_standby=on -c synchronous_commit=remote_apply
