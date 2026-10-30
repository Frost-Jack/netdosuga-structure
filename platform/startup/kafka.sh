#!/bin/sh
set -eu
mkdir -p /tmp/kraft-combined-logs
chown -R 1000:1000 /tmp/kraft-combined-logs
exec su -p -s /bin/bash appuser -c 'exec /etc/kafka/docker/run'
