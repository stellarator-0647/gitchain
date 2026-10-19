#!/usr/bin/env bash
# Runs the core loop against a running devnet (infra/scripts/devnet.sh up):
#   send -> sync -> anchor -> export proof -> vote deletion -> tombstone
#   -> export proof of the deleted message.
# Messages are signed by the dev signer with Anvil TEST keys.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/.devnet/demo"
mkdir -p "$OUT"
export KEY_A=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
export KEY_B=0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d
DEPLOY="$ROOT/deployments/31337.json"
command -v cygpath >/dev/null && DEPLOY="$(cygpath -m "$DEPLOY")"
DEV="$ROOT/bin/gitchain-dev"

# JSON goes through stdin: on Windows, command-line arguments are re-encoded
# to the ANSI code page and would corrupt non-ASCII text.
api() { # node method path [json]
  local port=$((8080 + $(printf '%d' "'$1") - 96))
  printf '%s' "${4:-}" | curl -sf -X "$2" -H "X-GitChain-Token: devtoken-$1" \
    -H "Content-Type: application/json; charset=utf-8" ${4:+--data-binary @-} "http://127.0.0.1:$port/api$3"
}
field() { PYTHONIOENCODING=utf-8 python -c "import json,sys; d=json.loads(sys.stdin.buffer.read().decode('utf-8')); print($1)"; }
# until_ok N CMD...: retry CMD once per second, up to N times.
until_ok() { local n=$1; shift; for _ in $(seq 1 "$n"); do if "$@" >/dev/null 2>&1; then return 0; fi; sleep 1; done; echo "gave up: $*" >&2; return 1; }
all_have() { # python condition that every node's history must satisfy
  for n in a b c; do api "$n" GET /history | field "$1" | grep -q True || return 1; done
}
step() { printf '\n== %s\n' "$*"; }

step "1. three members send messages"
M1=$(api a POST /dev/send '{"body":"甲方：图纸今天发出"}' | field 'd["eventId"]')
api b POST /dev/send '{"body":"乙方：收到，周三开工"}' >/dev/null
api c POST /dev/send '{"body":"监理：周五现场验收"}' >/dev/null
echo "first message $M1"

step "2. wait for sync, then anchor on node a"
until_ok 60 all_have 'd["total"]==3'
until_ok 30 api a POST /anchor/now
until_ok 60 all_have 'all(m["evidence"]=="anchored" for m in d["messages"])'
for n in a b c; do
  api "$n" GET /history | field '" ".join(m["evidence"]+"/"+m["content"] for m in d["messages"])' | sed "s/^/node $n: /"
done

step "3. node b exports a proof of the first message (with content)"
api b GET "/proof/$M1?content=1" >"$OUT/proof-$M1.json"
field '"checkpoint", d["inclusion"]["checkpointIndex"], "leaves", d["inclusion"]["count"], "body", d["content"]["body"]' <"$OUT/proof-$M1.json"

step "4. authorise deletion of the first message (2 of 3 votes)"
PID=$("$DEV" propose -deploy "$DEPLOY" -key-env KEY_A -room demo -target "$M1" -reason "sent by mistake" 2>/dev/null | awk '/proposal/{print $2}')
"$DEV" vote -deploy "$DEPLOY" -key-env KEY_A -id "$PID" >/dev/null 2>&1
"$DEV" vote -deploy "$DEPLOY" -key-env KEY_B -id "$PID" >/dev/null 2>&1
"$DEV" execute -deploy "$DEPLOY" -key-env KEY_B -id "$PID" 2>/dev/null
until_ok 60 all_have 'any(m["eventId"]=="'"$M1"'" and m["content"]=="purged" for m in d["messages"])'
for n in a b c; do
  api "$n" GET /history | field '[m["redaction"]+"/"+m["content"] for m in d["messages"] if m["eventId"]=="'"$M1"'"][0]' | sed "s/^/node $n: /"
done

step "5. member b signs the tombstone; node a anchors it"
DRAFT=$(api b POST /events/tombstone-draft "{\"proposalId\":$PID}" | field 'd["draftId"]')
api b POST /dev/sign "{\"draftId\":\"$DRAFT\"}" >/dev/null
until_ok 60 all_have 'd["total"]==4'
until_ok 30 api a POST /anchor/now
until_ok 60 all_have 'all(m["evidence"]=="anchored" for m in d["messages"])'
api c GET /history | field '[m["redaction"] for m in d["messages"] if m["eventId"]=="'"$M1"'"][0]' | sed 's/^/node c sees redaction: /'

step "6. proof of the deleted message carries the tombstone, not the body"
api c GET "/proof/$M1?content=1" >"$OUT/proof-deleted-$M1.json"
field '"content", d["content"], "tombstone proposal", d["redaction"]["proposalId"], "tombstone anchored", d["redaction"]["tombstoneInclusion"] is not None' <"$OUT/proof-deleted-$M1.json"

step "proof bundles written to .devnet/demo/"
