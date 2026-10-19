#!/usr/bin/env bash
# Re-exports contract ABIs into internal/chain/abi after a contract change.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
for c in RoomRegistry AnchorLog Governance; do
  (cd "$ROOT/contracts" && forge inspect "$c" abi --json) >"$ROOT/internal/chain/abi/$c.json"
done
echo "ABIs written to internal/chain/abi/"
