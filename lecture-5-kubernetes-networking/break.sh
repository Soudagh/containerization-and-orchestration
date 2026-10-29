#!/usr/bin/env bash
# break.sh - вносит в shop скрытую поломку связности: случайно выбирает один из файлов breaks/*.yaml
# и накатывает его поверх values чарта через helm upgrade. Какая поломка выбрана, не печатает.
#
#   ./break.sh          - внести случайную поломку
#   ./break.sh fix      - вернуть чистые values
#   ./break.sh reveal   - показать, какая поломка была внесена (после диагностики)
#
# После любого upgrade перезапускает api, worker и loadgen: так симптом появляется сразу,
# а не когда старые keep-alive соединения и пулы к базе случайно закроются.
set -euo pipefail

cd "$(dirname "$0")"
NAMESPACE="${NAMESPACE:-shop}"
ANSWER=.break-answer

restart() {
  kubectl -n "$NAMESPACE" rollout restart deploy/shop-api deploy/shop-worker deploy/shop-loadgen >/dev/null
}

case "${1:-break}" in
  break)
    files=(breaks/*.yaml)
    pick="${files[RANDOM % ${#files[@]}]}"
    helm upgrade shop chart/shop -n "$NAMESPACE" --reset-values -f "$pick" >/dev/null
    restart
    base64 <<<"$pick" > "$ANSWER"
    echo "поломка внесена. ищи по симптомам и hubble, файлы breaks/ не открывай"
    ;;
  fix)
    helm upgrade shop chart/shop -n "$NAMESPACE" --reset-values >/dev/null
    restart
    echo "чистые values накатаны"
    ;;
  reveal)
    [ -f "$ANSWER" ] || { echo "поломка не вносилась"; exit 1; }
    pick=$(base64 -d < "$ANSWER")
    echo "$pick:"
    cat "$pick"
    ;;
  *) echo "usage: $0 [break|fix|reveal]" >&2; exit 1 ;;
esac
