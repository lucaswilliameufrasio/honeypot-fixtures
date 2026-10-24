#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

for required in go python3 curl; do
  if ! command -v "$required" >/dev/null 2>&1; then
    printf 'Missing required tool: %s\n' "$required" >&2
    exit 2
  fi
done

mkdir -p benchmark-results /tmp/opencode
run_id=$(date -u +%Y%m%dT%H%M%SZ)-$$
output_dir="$repo_root/benchmark-results/$run_id"
mkdir -m 700 "$output_dir"
scratch=$(mktemp -d /tmp/opencode/honeypot-bench.XXXXXX)
app_pid=""
upstream_pid=""
sampler_pid=""
loadgen_pid=""
loadgen_sampler_pid=""

cleanup() {
  for pid in "$loadgen_sampler_pid" "$loadgen_pid" "$sampler_pid" "$app_pid" "$upstream_pid"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  rm -rf -- "$scratch"
}
trap cleanup EXIT INT TERM

pick_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

wait_http() {
  local url=$1
  local attempt
  for attempt in $(seq 1 100); do
    if curl --silent --fail --max-time 1 "$url" >/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

app_port=$(pick_port)
upstream_port=$(pick_port)
app_addr="127.0.0.1:$app_port"
upstream_addr="127.0.0.1:$upstream_port"
upstream_url="http://127.0.0.1:$upstream_port"
database_path="$scratch/metrics.sqlite"

go build -o "$scratch/honeypot" .
go build -o "$scratch/loadgen" ./cmd/loadgen
go build -o "$scratch/bench-upstream" ./cmd/bench-upstream

"$scratch/bench-upstream" -addr "$upstream_addr" >"$output_dir/upstream.log" 2>&1 &
upstream_pid=$!
if ! wait_http "$upstream_url/__bench/health"; then
  printf 'Local benchmark upstream did not become ready\n' >&2
  exit 1
fi

HONEYPOT_LISTEN_ADDR="$app_addr" \
HONEYPOT_UPSTREAM="$upstream_url" \
HONEYPOT_DB_PATH="$database_path" \
  "$scratch/honeypot" >"$output_dir/app.log" 2>&1 &
app_pid=$!
if ! wait_http "http://$app_addr/healthz"; then
  printf 'Honeypot did not become ready\n' >&2
  exit 1
fi

python3 - "$output_dir/metadata.json" <<'PY'
import json
import os
import platform
import subprocess
import sys
from datetime import datetime, timezone

memory_total_kib = 0
with open("/proc/meminfo", encoding="utf-8") as memory_file:
    for line in memory_file:
        if line.startswith("MemTotal:"):
            memory_total_kib = int(line.split()[1])
            break

metadata = {
    "started_at_utc": datetime.now(timezone.utc).isoformat(),
    "go_version": subprocess.run(["go", "version"], check=True, capture_output=True, text=True).stdout.strip(),
    "os": platform.system(),
    "arch": platform.machine(),
    "cpu_count": os.cpu_count() or 1,
    "memory_total_gib": memory_total_kib / 1024 / 1024,
    "commit": subprocess.run(["git", "rev-parse", "--short", "HEAD"], check=True, capture_output=True, text=True).stdout.strip(),
    "worktree_dirty": bool(subprocess.run(["git", "status", "--porcelain"], check=True, capture_output=True, text=True).stdout.strip()),
    "measurement": "local loopback; release build; SQLite WAL; synthetic no-op API upstream",
}
with open(sys.argv[1], "w", encoding="utf-8") as output_file:
    json.dump(metadata, output_file, indent=2)
    output_file.write("\n")
PY

  python3 scripts/sample_process.py "$app_pid" "$database_path" "$output_dir/resources.jsonl" &
sampler_pid=$!

run_case() {
  local name=$1
  local mode=$2
  local rate=$3
  local duration=$4
  local workers=128
  local queue_size=1024
  if ((rate > 5000)); then
    workers=512
    queue_size=8192
  fi
  if [[ "$name" == "health-only" ]]; then
    workers=4096
    queue_size=8192
  fi
  printf 'Benchmark %-24s mode=%-8s rate=%-5s duration=%s\n' "$name" "$mode" "$rate" "$duration"
  "$scratch/loadgen" \
    -url "http://$app_addr" \
    -mode "$mode" \
    -rate "$rate" \
    -duration "$duration" \
    -workers "$workers" \
    -queue "$queue_size" >"$output_dir/$name.json" &
  loadgen_pid=$!
  python3 scripts/sample_process.py "$loadgen_pid" "$database_path" "$output_dir/resources-loadgen-$name.jsonl" &
  loadgen_sampler_pid=$!
  wait "$loadgen_pid"
  loadgen_pid=""
  wait "$loadgen_sampler_pid" || true
  loadgen_sampler_pid=""
}

run_case warmup mixed 20 3s
run_case honeypot-only honeypot 100 10s
run_case proxy-only proxy 100 10s
run_case health-only health 100000 10s

default_rates=(50 100 200 400 800 1200 1600 2400 3200 5000 10000 20000 40000 80000 100000)
read -r -a rates <<< "${BENCHMARK_RATES:-${default_rates[*]}}"
if ((${#rates[@]} == 0)); then
  printf 'BENCHMARK_RATES must contain at least one offered rate\n' >&2
  exit 2
fi
for repetition in 1 2; do
  if [[ "$repetition" == 1 ]]; then
    ordered_rates=("${rates[@]}")
  else
    ordered_rates=()
    for ((index=${#rates[@]}-1; index>=0; index--)); do
      ordered_rates+=("${rates[index]}")
    done
  fi
  for rate in "${ordered_rates[@]}"; do
    run_case "capacity-${rate}-rep${repetition}" mixed "$rate" 8s
  done
done

healthy_rate=$(python3 scripts/select_benchmark_rate.py "$output_dir" 50)
spike_rate=$((healthy_rate * 3))
if ((spike_rate > 100000)); then
  spike_rate=100000
fi
soak_rate=$((healthy_rate * 3 / 4))
if ((soak_rate < 1)); then
  soak_rate=1
fi
if ((soak_rate > 10000)); then
  soak_rate=10000
fi
run_case "spike-${spike_rate}" mixed "$spike_rate" 15s
run_case "soak-${soak_rate}" mixed "$soak_rate" 60s
run_case recovery mixed "$healthy_rate" 10s

kill -TERM "$sampler_pid" 2>/dev/null || true
wait "$sampler_pid" 2>/dev/null || true
sampler_pid=""
kill -TERM "$app_pid" 2>/dev/null || true
wait "$app_pid" 2>/dev/null || true
app_pid=""
kill -TERM "$upstream_pid" 2>/dev/null || true
wait "$upstream_pid" 2>/dev/null || true
upstream_pid=""

go test -run '^$' -bench '^BenchmarkMetricsStoreRecord(Parallel)?$' -benchmem -benchtime=1s -count=3 ./... >"$output_dir/sqlite-microbench.txt"
python3 scripts/summarize_benchmark.py "$output_dir"
printf '\nBenchmark artifacts: %s\n' "${output_dir#"$repo_root/"}"
