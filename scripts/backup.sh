#!/bin/bash
# rackanvil backup: database + boot assets -> timestamped tarball.
# Usage: backup.sh <db-dsn> <assets-dir> <out-dir>
#   sqlite:/path/to.db  -> file copy (db must be quiescent; server stopped or WAL checkpointed)
#   postgres://...      -> pg_dump (custom format)
set -u
DSN="${1:?usage: backup.sh <db-dsn> <assets-dir> <out-dir>}"
ASSETS="${2:?usage: backup.sh <db-dsn> <assets-dir> <out-dir>}"
OUT="${3:?usage: backup.sh <db-dsn> <assets-dir> <out-dir>}"
TS=$(date +%Y%m%d-%H%M%S)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$OUT"

echo "[backup] $TS"
if [[ "$DSN" == sqlite:* ]]; then
  DBF="${DSN#sqlite:}"
  DBF="${DBF#file:}"
  [ -f "$DBF" ] || { echo "FAIL: db file not found: $DBF"; exit 1; }
  cp "$DBF" "$WORK/rackanvil.db"
  # copy WAL/SHM sidecars if present (consistent enough for a stopped server)
  for ext in -wal -shm; do [ -f "$DBF$ext" ] && cp "$DBF$ext" "$WORK/rackanvil.db$ext"; done
  echo "[backup] sqlite db copied: $DBF"
elif [[ "$DSN" == postgres://* || "$DSN" == postgresql://* ]]; then
  command -v pg_dump >/dev/null || { echo "FAIL: pg_dump not found"; exit 1; }
  pg_dump --format=custom --file="$WORK/rackanvil.dump" "$DSN" || { echo "FAIL: pg_dump"; exit 1; }
  echo "[backup] pg_dump done"
else
  echo "FAIL: unsupported DSN (want sqlite: or postgres://)"; exit 1
fi

if [ -d "$ASSETS" ]; then
  cp -a "$ASSETS" "$WORK/assets"
  echo "[backup] assets copied: $ASSETS"
fi

ARCHIVE="$OUT/rackanvil-backup-$TS.tar.gz"
tar -czf "$ARCHIVE" -C "$WORK" .
echo "[backup] wrote $ARCHIVE ($(du -h "$ARCHIVE" | cut -f1))"
echo "PASS"
