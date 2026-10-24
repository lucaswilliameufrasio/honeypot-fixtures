#!/usr/bin/env python3
"""Select the highest rate whose repeated capacity runs meet the local heuristic."""

import json
import sys
from collections import defaultdict
from pathlib import Path


ERROR_RATIO_LIMIT = 0.005
P99_LIMIT_MS = 250.0


def main():
    directory = Path(sys.argv[1])
    fallback = int(sys.argv[2])
    grouped = defaultdict(list)
    for path in directory.glob("capacity-*-rep*.json"):
        with path.open(encoding="utf-8") as input_file:
            case = json.load(input_file)
        grouped[case["offered_rate_rps"]].append(case)

    healthy = []
    for rate, runs in grouped.items():
        if len(runs) < 2:
            continue
        all_healthy = True
        for run in runs:
            offered = max(run["offered"], 1)
            failure_ratio = (run["errors"] + run["dropped"]) / offered
            generator_delivery = run["actual_offered_rps"] / run["offered_rate_rps"]
            if generator_delivery < 0.98 or failure_ratio > ERROR_RATIO_LIMIT or run["latency_p99_ms"] > P99_LIMIT_MS:
                all_healthy = False
                break
        if all_healthy:
            healthy.append(rate)
    print(max(healthy) if healthy else fallback)


if __name__ == "__main__":
    main()
