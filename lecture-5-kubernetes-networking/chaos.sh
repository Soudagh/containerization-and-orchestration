#!/usr/bin/env bash
# chaos.sh - непрерывный хаос подов shop: раз в INTERVAL секунд убивает случайный под api или worker.
#
# Переменные окружения:
#   MODE      graceful (по умолчанию) - обычный kubectl delete: SIGTERM, preStop, endpoint убирается заранее
#             force                   - --grace-period=0 --force: объект исчезает сразу, процесс получает SIGKILL
#   INTERVAL  секунд между убийствами (по умолчанию 5)
#   NAMESPACE namespace shop (по умолчанию shop)
#
# Остановка - Ctrl+C. Каждое убийство печатается с временем, чтобы сопоставить с графиком.
set -euo pipefail

MODE="${MODE:-graceful}"
INTERVAL="${INTERVAL:-5}"
NAMESPACE="${NAMESPACE:-shop}"
SELECTOR='app.kubernetes.io/name in (api,worker)'

case "$MODE" in
  graceful) DELETE_ARGS=(--wait=false) ;;
  force)    DELETE_ARGS=(--wait=false --grace-period=0 --force) ;;
  *) echo "unknown MODE=$MODE (graceful|force)" >&2; exit 1 ;;
esac

echo "chaos: mode=$MODE interval=${INTERVAL}s namespace=$NAMESPACE, Ctrl+C to stop"
trap 'echo; echo "chaos: stopped"; exit 0' INT TERM

while true; do
  # Только живые поды: уже умирающие (Terminating) повторно не трогаем.
  pods=()
  while IFS= read -r p; do pods+=("$p"); done < <(
    kubectl -n "$NAMESPACE" get pods -l "$SELECTOR" \
      -o jsonpath='{range .items[*]}{.metadata.deletionTimestamp}{"|"}{.metadata.name}{"|"}{.status.podIP}{"\n"}{end}' \
      | awk -F'|' '$1 == "" && $2 != "" {print $2 "|" $3}'
  )

  if [ "${#pods[@]}" -eq 0 ]; then
    echo "$(date '+%H:%M:%S') no running pods, waiting"
  else
    victim="${pods[RANDOM % ${#pods[@]}]}"
    name="${victim%%|*}"
    ip="${victim##*|}"
    kubectl -n "$NAMESPACE" delete pod "$name" "${DELETE_ARGS[@]}" >/dev/null 2>&1 || true
    echo "$(date '+%H:%M:%S') killed $name ($ip) mode=$MODE"
  fi

  sleep "$INTERVAL"
done
