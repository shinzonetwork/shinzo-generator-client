#!/usr/bin/env bash
set -eo pipefail

# ============================================================================
# Rust FFI Embedded Indexer Runner
#
# Usage:
#   ./scripts/run_rust_ffi.sh              # Start indexer (rocksdb, embedded)
#   ./scripts/run_rust_ffi.sh stop         # Graceful shutdown
#   ./scripts/run_rust_ffi.sh clean        # Stop + wipe all data
#   ./scripts/run_rust_ffi.sh status       # Show running process, disk, block
#   ./scripts/run_rust_ffi.sh monitor      # Live RSS/CPU/disk every 5s
#   ./scripts/run_rust_ffi.sh logs         # Tail indexer log
#
# Environment:
#   STORE=rocksdb|fjall|redb    Storage backend (default: rocksdb)
#   CONCURRENCY=16              Concurrent block workers
#   START_HEIGHT_OVERRIDE=N     Ethereum start block
#   ROCKS_BLOCK_CACHE_MB=1024   RocksDB tuning (see below)
# ============================================================================

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BASE_DIR="/tmp/shinzo-test"
INDEXER_LOG="${BASE_DIR}/indexer.log"
WATCHDOG_LOG="${BASE_DIR}/watchdog.log"
PIDS_FILE="${BASE_DIR}/pids"

STORE="${STORE:-rocksdb}"
CONCURRENCY="${CONCURRENCY:-16}"
RECEIPT_WORKERS="${RECEIPT_WORKERS:-8}"
START_HEIGHT="${START_HEIGHT_OVERRIDE:-23700000}"
WATCHDOG_DISK_LIMIT_GB="${WATCHDOG_DISK_LIMIT_GB:-200}"
WATCHDOG_RSS_LIMIT_MB="${WATCHDOG_RSS_LIMIT_MB:-12000}"

# RocksDB tuning defaults (override via env)
: "${ROCKS_BLOCK_CACHE_MB:=1024}"
: "${ROCKS_WRITE_BUFFER_MB:=128}"
: "${ROCKS_MAX_WRITE_BUFFERS:=6}"
: "${ROCKS_COMPACTIONS:=8}"
: "${ROCKS_FLUSHES:=4}"
: "${ROCKS_L0_SLOWDOWN:=30}"
: "${ROCKS_L0_STOP:=48}"
: "${ROCKS_TARGET_FILE_MB:=128}"
: "${ROCKS_LEVEL_BASE_MB:=512}"
: "${ROCKS_BLOCK_SIZE_KB:=16}"
: "${ROCKS_COMPRESSION:=lz4}"
: "${ROCKS_COMPACTION_STYLE:=level}"

# ---- helpers ----

die() { echo "ERROR: $*" >&2; exit 1; }

load_pids() {
  INDEXER_PID=""
  WATCHDOG_PID=""
  if [ -f "$PIDS_FILE" ]; then source "$PIDS_FILE"; fi
}

save_pids() {
  cat > "$PIDS_FILE" << EOF
INDEXER_PID=${INDEXER_PID:-}
WATCHDOG_PID=${WATCHDOG_PID:-}
EOF
}

is_alive() { [ -n "${1:-}" ] && kill -0 "$1" 2>/dev/null || return 1; }

kill_tracked() {
  load_pids
  local killed=0
  local pid=""
  for pid in ${INDEXER_PID} ${WATCHDOG_PID}; do
    if kill -0 "$pid" 2>/dev/null; then
      echo "Stopping PID ${pid}..."
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
      killed=1
    fi
  done
  pkill -f "block_poster.*shinzo-test" 2>/dev/null || true
  pkill -f "watchdog.sh" 2>/dev/null || true
  rm -f "$PIDS_FILE"
  if [ $killed -eq 0 ]; then echo "No tracked processes to stop."; fi
}

start_watchdog() {
  local pid=$1
  local disk_limit_kb=$(( WATCHDOG_DISK_LIMIT_GB * 1024 * 1024 ))
  local rss_limit_kb=$(( WATCHDOG_RSS_LIMIT_MB * 1024 ))

  cat > "${BASE_DIR}/watchdog.sh" << 'WATCHDOG'
#!/bin/bash
PID=$1; DISK_LIMIT_KB=$2; RSS_LIMIT_KB=$3; LOG=$4
while kill -0 "$PID" 2>/dev/null; do
  AVAIL_KB=$(df -k / | tail -1 | awk '{print $4}')
  RSS_KB=$(ps -o rss= -p "$PID" 2>/dev/null | tr -d ' ')
  [ -z "$RSS_KB" ] && break
  AVAIL_GB=$(( AVAIL_KB / 1024 / 1024 ))
  RSS_MB=$(( RSS_KB / 1024 ))
  if [ "$AVAIL_KB" -lt "$DISK_LIMIT_KB" ]; then
    echo "$(date): DISK LOW (${AVAIL_GB}GB free) - killing PID $PID" >> "$LOG"
    kill "$PID" 2>/dev/null; exit 0
  fi
  if [ "$RSS_KB" -gt "$RSS_LIMIT_KB" ]; then
    echo "$(date): RSS HIGH (${RSS_MB}MB) - killing PID $PID" >> "$LOG"
    kill "$PID" 2>/dev/null; exit 0
  fi
  echo "$(date): OK disk=${AVAIL_GB}GB rss=${RSS_MB}MB" >> "$LOG"
  sleep 60
done
WATCHDOG
  chmod +x "${BASE_DIR}/watchdog.sh"
  bash "${BASE_DIR}/watchdog.sh" "$pid" "$disk_limit_kb" "$rss_limit_kb" "$WATCHDOG_LOG" &
  WATCHDOG_PID=$!
  echo "  Watchdog:     PID ${WATCHDOG_PID} (disk <${WATCHDOG_DISK_LIMIT_GB}GB, RSS <${WATCHDOG_RSS_LIMIT_MB}MB)"
}

# ---- commands ----

cmd_stop() { kill_tracked; echo "Done."; }

cmd_clean() {
  kill_tracked
  [ -d "$BASE_DIR" ] && { echo "Wiping ${BASE_DIR}..."; rm -rf "$BASE_DIR"; }
  echo "Clean."
}

cmd_status() {
  echo "=== Shinzo FFI Indexer Status ==="
  echo ""
  if [ ! -d "$BASE_DIR" ]; then
    echo "No test directory. Run: ./scripts/run_rust_ffi.sh"
    return
  fi
  load_pids
  echo "Directory: ${BASE_DIR}"
  du -sh "$BASE_DIR" 2>/dev/null | awk '{print "Disk usage: " $1}'
  echo ""
  if is_alive "${INDEXER_PID:-}"; then
    local rss
    rss=$(ps -o rss= -p "$INDEXER_PID" 2>/dev/null | awk '{printf "%.0fMB", $1/1024}')
    echo "Indexer: running (PID ${INDEXER_PID}, RSS ${rss})"
  else
    echo "Indexer: stopped"
  fi
  if is_alive "${WATCHDOG_PID:-}"; then
    echo "Watchdog: running (PID ${WATCHDOG_PID})"
  fi
  echo ""
  # Latest block from log
  local latest
  latest=$(grep -oE 'height[=: ]*[0-9]+' "$INDEXER_LOG" 2>/dev/null | tail -1 | grep -oE '[0-9]+$' || echo "?")
  echo "Latest block: ${latest}"
  local errors
  errors=$(tail -200 "$INDEXER_LOG" 2>/dev/null | grep -c "ERROR" || echo "0")
  echo "Recent errors: ${errors}"
}

cmd_logs() { [ -f "$INDEXER_LOG" ] && tail -f "$INDEXER_LOG" || echo "No log"; }

cmd_monitor() {
  load_pids
  if ! is_alive "${INDEXER_PID:-}"; then
    echo "Indexer not running. Start first."
    return
  fi
  echo "=== Monitoring (Ctrl-C to stop) ==="
  while true; do
    local ts rss cpu disk latest errors
    ts=$(date +"%H:%M:%S")
    if is_alive "${INDEXER_PID:-}"; then
      rss=$(ps -o rss= -p "$INDEXER_PID" 2>/dev/null | awk '{printf "%.0fMB", $1/1024}')
      cpu=$(ps -o %cpu= -p "$INDEXER_PID" 2>/dev/null | awk '{printf "%.0f%%", $1}')
    else
      echo "[${ts}] INDEXER DIED"; tail -10 "$INDEXER_LOG" 2>/dev/null; break
    fi
    disk=$(du -sh "$BASE_DIR" 2>/dev/null | awk '{print $1}')
    latest=$(grep -oE 'height[=: ]*[0-9]+' "$INDEXER_LOG" 2>/dev/null | tail -1 | grep -oE '[0-9]+$' || echo "?")
    errors=$(tail -200 "$INDEXER_LOG" 2>/dev/null | grep -c "ERROR" || echo "0")
    printf "[%s] rss=%s cpu=%s disk=%s block=%s errs=%s\n" "$ts" "$rss" "$cpu" "$disk" "$latest" "$errors"
    sleep 5
  done
}

cmd_start() {
  # Preflight
  if [ ! -f "${ROOT_DIR}/.env" ]; then
    die "No .env file (need GETH_RPC_URL, GETH_WS_URL, GETH_API_KEY)"
  fi
  set -a; source "${ROOT_DIR}/.env"; set +a
  [ -n "${GETH_RPC_URL:-}" ] || die "GETH_RPC_URL not set in .env"

  # Re-apply CLI overrides (.env may have clobbered them)
  START_HEIGHT="${START_HEIGHT_OVERRIDE:-${START_HEIGHT:-23700000}}"

  if [ ! -f "${ROOT_DIR}/block_poster" ]; then
    echo "block_poster binary not found, building..."
    cd "$ROOT_DIR" && go build -o block_poster ./cmd/block_poster/
  fi

  # Stop existing
  load_pids
  if is_alive "${INDEXER_PID:-}"; then
    echo "Stopping existing indexer..."
    kill_tracked
    sleep 1
  fi

  mkdir -p "$BASE_DIR"

  # Generate config
  local config="${BASE_DIR}/indexer-config.yaml"
  cat > "$config" << YAML
defradb:
  url: ""
  keyring_secret: ""
  embedded: true
  use_rust_ffi: true
  p2p:
    enabled: false
    bootstrap_peers: []
    listen_addr: "/ip4/0.0.0.0/tcp/0"
    max_retries: 5
    retry_base_delay_ms: 1000
    reconnect_interval_ms: 60000
    enable_auto_reconnect: false
  store:
    path: "${BASE_DIR}/rust-ffi-data"

geth:
  node_url: "\${GETH_RPC_URL}"
  ws_url: "\${GETH_WS_URL}"
  api_key: "\${GETH_API_KEY}"

indexer:
  start_height: ${START_HEIGHT}
  concurrent_blocks: ${CONCURRENCY}
  receipt_workers: ${RECEIPT_WORKERS}
  max_docs_per_txn: 500
  blocks_per_minute: 0
  health_server_port: 0
  pprof_port: 0
  open_browser_on_start: false

pruner:
  enabled: true
  max_blocks: 10000
  prune_threshold: 100
  interval_seconds: 60
  prune_history: false

logger:
  development: false
YAML

  echo "=== Shinzo Indexer - Rust FFI Embedded ==="
  echo "  Store:        ${STORE}"
  echo "  Concurrency:  ${CONCURRENCY} blocks / ${RECEIPT_WORKERS} receipt workers"
  echo "  Start height: ${START_HEIGHT}"
  echo "  Data:         ${BASE_DIR}/rust-ffi-data"
  echo "  Log:          ${INDEXER_LOG}"

  if [ "${STORE}" = "rocksdb" ]; then
    echo ""
    echo "  RocksDB tuning:"
    echo "    ROCKS_BLOCK_CACHE_MB=${ROCKS_BLOCK_CACHE_MB}"
    echo "    ROCKS_WRITE_BUFFER_MB=${ROCKS_WRITE_BUFFER_MB}"
    echo "    ROCKS_MAX_WRITE_BUFFERS=${ROCKS_MAX_WRITE_BUFFERS}"
    echo "    ROCKS_COMPACTIONS=${ROCKS_COMPACTIONS}"
    echo "    ROCKS_FLUSHES=${ROCKS_FLUSHES}"
    echo "    ROCKS_L0_SLOWDOWN=${ROCKS_L0_SLOWDOWN}"
    echo "    ROCKS_L0_STOP=${ROCKS_L0_STOP}"
    echo "    ROCKS_TARGET_FILE_MB=${ROCKS_TARGET_FILE_MB}"
    echo "    ROCKS_LEVEL_BASE_MB=${ROCKS_LEVEL_BASE_MB}"
    echo "    ROCKS_BLOCK_SIZE_KB=${ROCKS_BLOCK_SIZE_KB}"
    echo "    ROCKS_COMPRESSION=${ROCKS_COMPRESSION}"
    echo "    ROCKS_COMPACTION_STYLE=${ROCKS_COMPACTION_STYLE}"
  fi
  echo ""

  # Start indexer
  cd "$ROOT_DIR"
  STORE="${STORE}" \
    ALL_PROXY="${ALL_PROXY:-}" \
    HTTPS_PROXY="${HTTPS_PROXY:-}" \
    HTTP_PROXY="${HTTP_PROXY:-}" \
    ROCKS_BLOCK_CACHE_MB="${ROCKS_BLOCK_CACHE_MB}" \
    ROCKS_WRITE_BUFFER_MB="${ROCKS_WRITE_BUFFER_MB}" \
    ROCKS_MAX_WRITE_BUFFERS="${ROCKS_MAX_WRITE_BUFFERS}" \
    ROCKS_COMPACTIONS="${ROCKS_COMPACTIONS}" \
    ROCKS_FLUSHES="${ROCKS_FLUSHES}" \
    ROCKS_L0_SLOWDOWN="${ROCKS_L0_SLOWDOWN}" \
    ROCKS_L0_STOP="${ROCKS_L0_STOP}" \
    ROCKS_TARGET_FILE_MB="${ROCKS_TARGET_FILE_MB}" \
    ROCKS_LEVEL_BASE_MB="${ROCKS_LEVEL_BASE_MB}" \
    ROCKS_BLOCK_SIZE_KB="${ROCKS_BLOCK_SIZE_KB}" \
    ROCKS_COMPRESSION="${ROCKS_COMPRESSION}" \
    ROCKS_COMPACTION_STYLE="${ROCKS_COMPACTION_STYLE}" \
    nohup ./block_poster --config "$config" > "$INDEXER_LOG" 2>&1 &
  INDEXER_PID=$!
  echo "  Indexer:      PID ${INDEXER_PID}"

  save_pids
  start_watchdog "$INDEXER_PID"
  save_pids

  sleep 3
  if ! is_alive "$INDEXER_PID"; then
    echo ""
    echo "INDEXER CRASHED ON STARTUP. Last 30 lines:"
    tail -30 "$INDEXER_LOG"
    exit 1
  fi

  echo ""
  echo "=== Running ==="
  echo "  tail -f ${INDEXER_LOG}"
  echo "  ./scripts/run_rust_ffi.sh monitor"
  echo "  ./scripts/run_rust_ffi.sh status"
  echo "  ./scripts/run_rust_ffi.sh stop"
  echo "  ./scripts/run_rust_ffi.sh clean   # stop + wipe data"
  echo ""
  echo "=== Indexer Output (Ctrl-C to detach, process keeps running) ==="
  tail -f "$INDEXER_LOG" || true
}

# ---- main ----

case "${1:-start}" in
  start)   cmd_start ;;
  stop)    cmd_stop ;;
  clean)   cmd_clean ;;
  status)  cmd_status ;;
  monitor) cmd_monitor ;;
  logs)    cmd_logs ;;
  *)       echo "Usage: $0 {start|stop|clean|status|monitor|logs}"; exit 1 ;;
esac
