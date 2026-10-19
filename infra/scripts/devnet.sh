#!/usr/bin/env bash
# Starts a local three-member GitChain devnet:
#   Anvil chain -> deploy contracts -> deployments/31337.json -> create room
#   -> bare Git remote -> node A (anchor node) + nodes B and C.
# Uses Anvil's publicly known TEST accounts. Never put real keys here.
#
#   infra/scripts/devnet.sh up      start everything (state in .devnet/)
#   infra/scripts/devnet.sh down    stop processes
#   infra/scripts/devnet.sh reset   stop and delete .devnet/
#   infra/scripts/devnet.sh restart-nodes   rebuild and restart nodes only
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
RUN="$ROOT/.devnet"
RPC="http://127.0.0.1:8545"
ROOM="${ROOM:-demo}"

# Anvil default accounts 0, 1, 2 (test only).
export KEY_A=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
export KEY_B=0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d
export KEY_C=0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a
ADDR_A=0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266
ADDR_B=0x70997970C51812dc3A010C7d01b50e0d17dc79C8
ADDR_C=0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC

winpath() { if command -v cygpath >/dev/null; then cygpath -m "$1"; else echo "$1"; fi; }

wait_port() { # host:port
  for _ in $(seq 1 60); do
    if (exec 3<>"/dev/tcp/${1%:*}/${1#*:}") 2>/dev/null; then exec 3>&-; return 0; fi
    sleep 0.5
  done
  echo "timeout waiting for $1" >&2; return 1
}

start_node() { # name port wallet key-var extra...
  local name=$1 port=$2 wallet=$3 keyvar=$4; shift 4
  "$ROOT/bin/gitchain" -data "$(winpath "$RUN/$name")" -deploy "$(winpath "$ROOT/deployments/31337.json")" \
    -room "$ROOM" -wallet "$wallet" -remote "$(winpath "$RUN/remote.git")" \
    -listen "127.0.0.1:$port" -token "devtoken-$name" -dev-key-env "$keyvar" "$@" \
    >"$RUN/$name.log" 2>&1 &
  echo $! >"$RUN/$name.pid"
  wait_port "127.0.0.1:$port"
  echo "node $name: http://127.0.0.1:$port  token devtoken-$name  log .devnet/$name.log"
}

up() {
  mkdir -p "$RUN" "$ROOT/deployments"
  (cd "$ROOT" && CGO_ENABLED=0 go build -o bin/ ./cmd/...)

  anvil --silent --port 8545 >"$RUN/anvil.log" 2>&1 &
  echo $! >"$RUN/anvil.pid"
  wait_port 127.0.0.1:8545

  (cd "$ROOT/contracts" && forge script script/Deploy.s.sol --rpc-url "$RPC" --broadcast --private-key "$KEY_A" >"$RUN/deploy.log" 2>&1)
  "$ROOT/bin/gitchain-dev" write-deploy -broadcast "$(winpath "$ROOT/contracts/broadcast/Deploy.s.sol/31337/run-latest.json")" \
    -rpc "$RPC" -chain-id 31337 -out "$(winpath "$ROOT/deployments/31337.json")"
  "$ROOT/bin/gitchain-dev" create-room -deploy "$(winpath "$ROOT/deployments/31337.json")" -key-env KEY_A \
    -room "$ROOM" -members "$ADDR_A,$ADDR_B,$ADDR_C" -anchor-writer "$ADDR_A"

  git init -q --bare -b main "$RUN/remote.git"
  start_nodes
}

start_nodes() {
  start_node a 8081 "$ADDR_A" KEY_A -init -anchor-key-env KEY_A -anchor-n 5 -anchor-every 1m -sync-every 2s
  start_node b 8082 "$ADDR_B" KEY_B -sync-every 2s
  start_node c 8083 "$ADDR_C" KEY_C -sync-every 2s
}

# restart-nodes: rebuild and restart the three nodes, keeping chain and repos.
restart_nodes() {
  for n in a b c; do
    [ -f "$RUN/$n.pid" ] && kill "$(cat "$RUN/$n.pid")" 2>/dev/null || true
    rm -f "$RUN/$n.pid"
  done
  sleep 1
  (cd "$ROOT" && CGO_ENABLED=0 go build -o bin/ ./cmd/...)
  start_nodes
}

down() {
  for p in "$RUN"/*.pid; do
    [ -f "$p" ] || continue
    kill "$(cat "$p")" 2>/dev/null || true
    rm -f "$p"
  done
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  restart-nodes) restart_nodes ;;
  reset) down; rm -rf "$RUN" "$ROOT/contracts/broadcast" "$ROOT/deployments/31337.json" ;;
  *) echo "usage: $0 up|down|reset|restart-nodes"; exit 2 ;;
esac
