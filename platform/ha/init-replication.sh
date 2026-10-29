#!/bin/sh
set -eu
printf '\nhost replication demo all scram-sha-256\n' >> "$PGDATA/pg_hba.conf"
