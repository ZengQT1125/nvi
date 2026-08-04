#!/bin/sh
set -eu

data_dir="${GATEWAY_DATA_DIR:-}"
if [ -z "$data_dir" ]; then
  case "${GATEWAY_STORE_PATH:-}" in
    *.json) data_dir="$(dirname "$GATEWAY_STORE_PATH")/data" ;;
    "") data_dir="/app/var/data" ;;
    *) data_dir="$GATEWAY_STORE_PATH" ;;
  esac
fi

xray_dir="${XRAY_CORE_DIR:-/app/bin/xray}"
export GATEWAY_DATA_DIR="$data_dir"
export XRAY_CORE_DIR="$xray_dir"
export BACKEND_PORT="${BACKEND_PORT:-${PORT:-18080}}"
export FRONTEND_PORT="${FRONTEND_PORT:-14000}"

mkdir -p "$data_dir/store" "$data_dir/logs" "$xray_dir"

backend_pid=""
frontend_pid=""

cleanup() {
  status=${1:-0}
  if [ -n "$frontend_pid" ] && kill -0 "$frontend_pid" 2>/dev/null; then
    kill "$frontend_pid" 2>/dev/null || true
  fi
  if [ -n "$backend_pid" ] && kill -0 "$backend_pid" 2>/dev/null; then
    kill "$backend_pid" 2>/dev/null || true
  fi
  wait 2>/dev/null || true
  exit "$status"
}

trap 'cleanup 0' INT TERM

/app/nvidia-api-gateway &
backend_pid=$!

cd /app/frontend
npm run start &
frontend_pid=$!

while kill -0 "$backend_pid" 2>/dev/null && kill -0 "$frontend_pid" 2>/dev/null; do
  sleep 1
done

status=0
if ! kill -0 "$backend_pid" 2>/dev/null; then
  wait "$backend_pid" || status=$?
fi
if ! kill -0 "$frontend_pid" 2>/dev/null; then
  wait "$frontend_pid" || status=$?
fi

cleanup "$status"
