#!/usr/bin/env python3
"""Sample a Linux process and SQLite sidecar sizes as JSON lines."""

import json
import os
import sys
import time
from datetime import datetime, timezone


def process_ticks(pid):
    with open(f"/proc/{pid}/stat", encoding="utf-8") as stat_file:
        raw = stat_file.read()
    fields = raw[raw.rfind(")") + 2 :].split()
    return int(fields[11]) + int(fields[12])


def resident_bytes(pid):
    with open(f"/proc/{pid}/status", encoding="utf-8") as status_file:
        for line in status_file:
            if line.startswith("VmRSS:"):
                return int(line.split()[1]) * 1024
    return 0


def sqlite_bytes(database_path):
    total = 0
    for suffix in ("", "-wal", "-shm"):
        try:
            total += os.path.getsize(database_path + suffix)
        except FileNotFoundError:
            pass
    return total


def main():
    pid = int(sys.argv[1])
    database_path = sys.argv[2]
    output_path = sys.argv[3]
    ticks_per_second = os.sysconf("SC_CLK_TCK")
    cpu_count = os.cpu_count() or 1
    previous_time = time.monotonic()
    try:
        previous_ticks = process_ticks(pid)
    except FileNotFoundError:
        return 1

    with open(output_path, "w", encoding="utf-8") as output_file:
        while True:
            time.sleep(1)
            current_time = time.monotonic()
            try:
                current_ticks = process_ticks(pid)
                rss = resident_bytes(pid)
            except FileNotFoundError:
                break
            elapsed = current_time - previous_time
            delta_ticks = max(0, current_ticks - previous_ticks)
            cpu_percent = (delta_ticks / ticks_per_second) / max(elapsed, 0.001) / cpu_count * 100
            record = {
                "time_utc": datetime.now(timezone.utc).isoformat(),
                "cpu_percent_of_host": round(cpu_percent, 3),
                "rss_bytes": rss,
                "sqlite_files_bytes": sqlite_bytes(database_path),
            }
            output_file.write(json.dumps(record, separators=(",", ":")) + "\n")
            output_file.flush()
            previous_time = current_time
            previous_ticks = current_ticks


if __name__ == "__main__":
    raise SystemExit(main())
