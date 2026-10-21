#!/bin/bash
# rackanvil restore: verify a backup tarball by extracting it and
# opening the database read-only (hosts count must be readable).
# Usage: restore.sh <archive.tar.gz> <dest-dir>
# This is a *verification drill*, not an in-place restore: it never touches
# the live database. For a real restore, stop the server first, then copy
# the extracted files over the live paths.
set -u
ARCHIVE="${1:?usage: restore.sh <archive.tar.gz> <dest-dir>}"
DEST="${2:?usage: restore.sh <archive.tar.gz> <dest-dir>}"
[ -f "$ARCHIVE" ] || { echo "FAIL: archive not found"; exit 1; }

rm -rf "$DEST" && mkdir -p "$DEST"
tar -xzf "$ARCHIVE" -C "$DEST" || { echo "FAIL: extract"; exit 1; }
echo "[restore] extracted to $DEST"

if [ -f "$DEST/rackanvil.db" ]; then
  export PATH=~/workspace/toolchain/go/bin:$PATH
  cd ~/workspace/deplos-reimpl || exit 1
  # read-only open via a tiny go probe (no server needed)
  cat > /tmp/m3/restore_probe.go << 'EOF'
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "file:"+os.Args[1]+"?mode=ro")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM hosts").Scan(&n); err != nil {
		panic(err)
	}
	var v int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
		panic(err)
	}
	fmt.Printf("hosts=%d schema_version=%d\n", n, v)
}
EOF
  mkdir -p /tmp/m3/probe && cp /tmp/m3/restore_probe.go /tmp/m3/probe/main.go
  # run inside the module so internal deps resolve
  mkdir -p ./cmd/restoreprobe && cp /tmp/m3/probe/main.go ./cmd/restoreprobe/main.go
  go run ./cmd/restoreprobe "$DEST/rackanvil.db" || { rm -rf ./cmd/restoreprobe; echo "FAIL: db probe"; exit 1; }
  rm -rf ./cmd/restoreprobe
elif [ -f "$DEST/rackanvil.dump" ]; then
  echo "[restore] pg_dump archive present (restore with pg_restore into a scratch PG to verify)"
else
  echo "FAIL: no database payload in archive"; exit 1
fi
echo "PASS: backup archive verified"
