#!/usr/bin/env python3
"""Create a concise local benchmark report from the harness artifacts."""

import json
import statistics
import sys
from pathlib import Path


HEALTHY_ERROR_RATIO = 0.005
HEALTHY_P99_MS = 250.0
MIN_GENERATOR_DELIVERY = 0.98


def load_json(path):
    with path.open(encoding="utf-8") as input_file:
        return json.load(input_file)


def is_healthy(case):
    offered = max(case["offered"], 1)
    failures = case["errors"] + case["dropped"]
    generator_delivery = case["actual_offered_rps"] / max(case["offered_rate_rps"], 1)
    return generator_delivery >= 0.98 and failures / offered <= HEALTHY_ERROR_RATIO and case["latency_p99_ms"] <= HEALTHY_P99_MS


def main():
    output_dir = Path(sys.argv[1])
    metadata = load_json(output_dir / "metadata.json")
    cases = {path.stem: load_json(path) for path in output_dir.glob("*.json") if path.name != "metadata.json"}
    capacity_runs = {}
    for name, case in cases.items():
        if name.startswith("capacity-"):
            capacity_runs.setdefault(case["offered_rate_rps"], []).append(case)
    capacity = sorted(capacity_runs.items())
    healthy_rates = [
        rate
        for rate, runs in capacity
        if len(runs) >= 2 and all(is_healthy(case) for case in runs)
    ]
    resources = []
    resource_path = output_dir / "resources.jsonl"
    if resource_path.exists():
        with resource_path.open(encoding="utf-8") as input_file:
            resources = [json.loads(line) for line in input_file if line.strip()]

    lines = [
        "# Honeypot benchmark report",
        "",
        f"- Run: {metadata['started_at_utc']}",
        f"- Runtime: {metadata['go_version']}; {metadata['os']} {metadata['arch']}; {metadata['cpu_count']} logical CPUs; {metadata['memory_total_gib']:.1f} GiB RAM",
        f"- Build: optimized `go build`; source commit `{metadata['commit']}`; worktree dirty: `{str(metadata['worktree_dirty']).lower()}`",
        "- Scope: loopback only, one honeypot process, local synthetic echo upstream, SQLite WAL; no TLS or external network.",
        "- Mixed profile: 80% exact honeypot route, 20% proxied API route; 128 closed HTTP workers offered at an open-loop fixed rate; 1024-slot bounded load queue.",
        f"- Healthy-step heuristic (benchmark-only, not an SLA): generator offers ≥ {MIN_GENERATOR_DELIVERY * 100:.0f}% of target, combined error/drop ratio ≤ {HEALTHY_ERROR_RATIO * 100:.1f}%, and p99 ≤ {HEALTHY_P99_MS:.0f} ms.",
        "",
        "## HTTP load phases",
        "",
        "| Scenario | Target rps | Actual offered rps | Offered | Completed | Delivered rps | Client queue drops | Errors | p50 ms | p95 ms | p99 median/max ms |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    ordered_names = ["warmup", "honeypot-only", "proxy-only"]
    ordered_names.extend(sorted(name for name in cases if name.startswith("spike-")))
    ordered_names.extend(sorted(name for name in cases if name.startswith("soak-")))
    if "recovery" in cases:
        ordered_names.append("recovery")
    for name in ordered_names:
        case = cases.get(name)
        if case is None:
            continue
        lines.append(
            f"| {name} | {case['offered_rate_rps']} | {case['actual_offered_rps']:.1f} | {case['offered']} | {case['completed']} | {case['delivered_rps']:.1f} | {case['dropped']} | {case['errors']} | {case['latency_p50_ms']:.2f} | {case['latency_p95_ms']:.2f} | {case['latency_p99_ms']:.2f}/{case['latency_p99_ms']:.2f} |"
        )
    for rate, runs in capacity:
        median = lambda key: statistics.median(case[key] for case in runs)
        lines.append(
            f"| capacity ×{len(runs)} | {rate} | {median('actual_offered_rps'):.1f} | {sum(case['offered'] for case in runs)} | {sum(case['completed'] for case in runs)} | {median('delivered_rps'):.1f} | {sum(case['dropped'] for case in runs)} | {sum(case['errors'] for case in runs)} | {median('latency_p50_ms'):.2f} | {median('latency_p95_ms'):.2f} | {median('latency_p99_ms'):.2f}/{max(case['latency_p99_ms'] for case in runs):.2f} |"
        )
    if healthy_rates:
        lines.extend(["", f"Highest sampled rate step meeting the heuristic in both repetitions: **{max(healthy_rates)} offered requests/s**."])
    else:
        lines.extend(["", "No capacity step met the benchmark heuristic."])
    if healthy_rates and any(rate < max(healthy_rates) and not all(is_healthy(case) for case in runs) for rate, runs in capacity):
        lines.extend(["", "Caution: the health result is non-monotonic across tested rates; outlier tail latency at lower steps means this campaign does not identify a precise capacity knee."])

    if resources:
        max_cpu = max(sample["cpu_percent_of_host"] for sample in resources)
        max_rss = max(sample["rss_bytes"] for sample in resources) / (1024 * 1024)
        max_db = max(sample["sqlite_files_bytes"] for sample in resources) / (1024 * 1024)
        lines.extend(
            [
                "",
                "## Resource samples",
                "",
                f"- App peak sampled CPU: {max_cpu:.1f}% of host capacity.",
                f"- App peak sampled RSS: {max_rss:.1f} MiB.",
                f"- Peak SQLite database + WAL/SHM size during run: {max_db:.2f} MiB.",
            ]
        )
    generator_samples = []
    for path in output_dir.glob("resources-loadgen-*.jsonl"):
        with path.open(encoding="utf-8") as input_file:
            generator_samples.extend(json.loads(line) for line in input_file if line.strip())
    if generator_samples:
        generator_cpu = max(sample["cpu_percent_of_host"] for sample in generator_samples)
        generator_rss = max(sample["rss_bytes"] for sample in generator_samples) / (1024 * 1024)
        lines.extend(
            [
                f"- Load-generator peak sampled CPU: {generator_cpu:.1f}% of host capacity.",
                f"- Load-generator peak sampled RSS: {generator_rss:.1f} MiB.",
            ]
        )
    microbench = output_dir / "sqlite-microbench.txt"
    if microbench.exists():
        lines.extend(["", "## SQLite store microbenchmark", "", "```text", microbench.read_text(encoding="utf-8").strip(), "```"])
    lines.extend(
        [
            "",
            "## Interpretation and limitations",
            "",
            "This result characterizes this local Linux/amd64 host and the synthetic, zero-work upstream. It is not a production capacity claim. The open-loop generator runs on the same host and can itself become the limiter; actual offered rate must reach at least 98% of target to count as a healthy step. Compare offered, started, completed, dropped, generator CPU/RSS, and delivered rate. Resource values are one-second `/proc` samples. The SQLite database was temporary and removed after the run; counters and recent events were exercised through the real application path.",
            "Capacity steps are short and only repeated twice; the median p99 is shown with the worst p99 in the pair because local scheduling can create outlier tails. `Client queue drops` are offered requests the bounded load generator could not enqueue; they are not HTTP rejection responses. The spike is deliberately above the healthy-step threshold and reports overload behavior rather than a healthy capacity claim.",
            "",
        ]
    )
    report = "\n".join(lines)
    report_path = output_dir / "summary.md"
    report_path.write_text(report, encoding="utf-8")
    print(report)
    print(f"\nSummary saved to {report_path}")


if __name__ == "__main__":
    main()
