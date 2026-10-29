#!/usr/bin/env bash

set -uo pipefail

NAMESPACE="${NAMESPACE:-shop}"
NODEPORT="${NODEPORT:-30080}"
TRIES="${TRIES:-10}"
IMAGE="nicolaka/netshoot:v0.13"
DBG="matrix-probe"

pod_of() { kubectl -n "$NAMESPACE" get pods -l "app.kubernetes.io/name=$1" -o jsonpath='{.items[0].metadata.name}'; }

ensure_probe() {
  local pod="$1"
  local running
  running=$(kubectl -n "$NAMESPACE" get pod "$pod" -o jsonpath="{.status.ephemeralContainerStatuses[?(@.name=='$DBG')].state.running}")
  if [ -z "$running" ]; then
    kubectl -n "$NAMESPACE" debug "$pod" --image="$IMAGE" --container="$DBG" --profile=general -- sleep infinity >/dev/null 2>&1
    for _ in $(seq 1 30); do
      running=$(kubectl -n "$NAMESPACE" get pod "$pod" -o jsonpath="{.status.ephemeralContainerStatuses[?(@.name=='$DBG')].state.running}")
      [ -n "$running" ] && break
      sleep 1
    done
  fi
}

ensure_outsider() {
  if ! kubectl -n "$NAMESPACE" get pod outsider >/dev/null 2>&1; then
    kubectl -n "$NAMESPACE" run outsider --image="$IMAGE" --restart=Never --labels=role=outsider --command -- sleep infinity >/dev/null
  fi
  kubectl -n "$NAMESPACE" wait --for=condition=Ready pod/outsider --timeout=60s >/dev/null
}

run_in() {
  local pod="$1" ctr="$2"; shift 2
  kubectl -n "$NAMESPACE" exec "$pod" -c "$ctr" -- "$@" >/dev/null 2>&1
}

tcp()  { run_in "$1" "$2" nc -z -w 2 "$3" "$4"; }
dns()  { run_in "$1" "$2" nslookup -timeout=2 -retry=1 "$3"; }

nodeport() {
  local node="$1" target="$2" ok=0
  for _ in $(seq 1 "$TRIES"); do
    docker exec "$node" curl -s -m 2 -o /dev/null "http://$target:$NODEPORT/health" && ok=$((ok + 1))
  done
  echo "$ok"
}

printf '%-3s %-12s %-28s %-10s %-10s %s\n' "#" "откуда" "куда" "ожидаемо" "фактически" ""
row() {
  local n="$1" from="$2" to="$3" expect="$4" actual="$5" mark
  if [ "$expect" = "?" ]; then mark="ℹ️"
  elif [ "$expect" = "$actual" ]; then mark="✅"
  else mark="❌"; fi
  printf '%-3s %-12s %-28s %-10s %-10s %s\n' "$n" "$from" "$to" "$expect" "$actual" "$mark"
}
res() { if "$@"; then echo "проходит"; else echo "закрыто"; fi; }

API=$(pod_of api); WORKER=$(pod_of worker); LOADGEN=$(pod_of loadgen)
for p in "$API" "$WORKER" "$LOADGEN"; do ensure_probe "$p"; done
ensure_outsider

DB=shop-db-rw; APISVC=shop-api; WORKERSVC=shop-worker

row 1  loadgen    "api:8080"            проходит "$(res tcp "$LOADGEN" "$DBG" "$APISVC" 8080)"
row 2  api        "postgres:5432"       проходит "$(res tcp "$API" "$DBG" "$DB" 5432)"
row 3  worker     "postgres:5432"       проходит "$(res tcp "$WORKER" "$DBG" "$DB" 5432)"
row 4  api        "DNS $DB"             проходит "$(res dns "$API" "$DBG" "$DB")"
row 5  outsider   "postgres:5432"       закрыто  "$(res tcp outsider outsider "$DB" 5432)"
row 6  outsider   "api:8080"            закрыто  "$(res tcp outsider outsider "$APISVC" 8080)"
row 7  outsider   "DNS $DB"             проходит "$(res dns outsider outsider "$DB")"
row 8  api        "worker:8080"         закрыто  "$(res tcp "$API" "$DBG" "$WORKERSVC" 8080)"
row 9  loadgen    "postgres:5432"       закрыто  "$(res tcp "$LOADGEN" "$DBG" "$DB" 5432)"
row 10 worker     "api:8080"            закрыто  "$(res tcp "$WORKER" "$DBG" "$APISVC" 8080)"
row 11 api        "1.1.1.1:443"         закрыто  "$(res tcp "$API" "$DBG" 1.1.1.1 443)"

WORKER_NODE_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' lab5-worker)
row 12 "нода worker" "NodePort localhost" "$TRIES/$TRIES" "$(nodeport lab5-worker localhost)/$TRIES"
row 13 "control-plane" "NodePort ноды worker" "$TRIES/$TRIES" "$(nodeport lab5-control-plane "$WORKER_NODE_IP")/$TRIES"
