#!/usr/bin/env bash
set -euo pipefail
LAB=${1:-/tmp/prm-lab}
for n in replica primary; do
  [ -f "$LAB/$n/mysqld.pid" ] && kill "$(cat "$LAB/$n/mysqld.pid")" 2>/dev/null || true
done
echo "Laboratoire arrêté ($LAB)"
