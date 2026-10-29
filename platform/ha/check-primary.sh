#!/bin/sh
set -eu
export PGPASSWORD="$POSTGRES_PASSWORD"
export PGCONNECT_TIMEOUT=3
export PGOPTIONS='-c statement_timeout=5000'
psql -h postgres -U demo -d demo -At -v ON_ERROR_STOP=1 -c 'SELECT pg_is_in_recovery()'
