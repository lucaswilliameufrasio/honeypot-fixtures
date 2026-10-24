# Benchmark report

Date: 2026-10-24. Raw results: ignored directory `benchmark-results/20261024T052632Z-1925272/`.

## Setup

- Go 1.27.3; Linux amd64; Intel Core i5-8265U; 8 logical CPUs; 15.5 GiB RAM.
- Release `go build` binary; SQLite `modernc.org/sqlite` in WAL mode with `synchronous=NORMAL` and Goose migrations.
- App and synthetic zero-work API upstream ran on loopback on the same host; no TLS or external network.
- 80% honeypot / 20% proxy mix; 128 concurrent HTTP workers; open-loop target rates; bounded load-generator queue of 1,024.
- Capacity steps ran for 8 seconds each, twice (ascending then descending). The campaign also included warmup, 10-second honeypot-only and proxy-only runs, a 15-second spike, a 60-second soak, and a 10-second recovery check.
- Benchmark-only healthy heuristic: offered rate at least 98% of target, errors plus client queue drops at most 0.5%, and p99 at most 250 ms. This is not a production SLA.

## Results

| Scenario | Target rps | Delivered rps | Client queue drops | HTTP/network errors | p95 ms | p99 median / worst ms |
|---|---:|---:|---:|---:|---:|---:|
| Honeypot only | 100 | 100.1 | 0 | 0 | 2.01 | 3.49 / 3.49 |
| Proxy only | 100 | 100.1 | 0 | 0 | 2.80 | 5.60 / 5.60 |
| Mixed, capacity ×2 | 400 | 400.0 | 0 | 0 | 31.11 | 72.21 / 140.84 |
| Mixed, capacity ×2 | 800 | 792.3 | 0 | 0 | 128.99 | 211.66 / 420.19 |
| Mixed, capacity ×2 | 1,200 | 1,199.9 | 0 | 0 | 128.15 | 331.35 / 457.12 |
| Mixed, capacity ×2 | 1,600 | 1,599.9 | 0 | 0 | 10.48 | 54.54 / 83.28 |
| Mixed, capacity ×2 | 2,400 | 2,399.8 | 0 | 0 | 21.11 | 62.89 / 93.92 |
| Mixed, capacity ×2 | 3,200 | 3,176.4 | 0 | 0 | 25.23 | 87.43 / 97.02 |
| Mixed, soak 1,200 for 60 s | 1,200 | 1,200.0 | 0 | 0 | 1.79 | 7.30 / 7.30 |
| Mixed, spike | 5,000 | 4,249.2 | 9,816 | 0 | 87.57 | 147.81 / 147.81 |
| Mixed, recovery | 3,200 | 3,199.9 | 0 | 0 | 24.55 | 69.80 / 69.80 |

The highest tested step meeting the heuristic in both repetitions was 3,200 rps. At the 5,000-rps spike, the load generator's bounded pending queue dropped 9,816 of 75,000 offered requests; these were **not** HTTP rejections by the application. All requests that reached the app returned HTTP 200. The 60-second 1,200-rps soak completed 72,001 requests with no errors or queue drops.

The result is non-monotonic: one repetition at 800 and 1,200 rps had p99 outliers above 400 ms, while both repetitions at 1,600–3,200 rps were below 100 ms. This indicates substantial local tail-latency variability, so the test does not establish a precise capacity knee. It demonstrates a stable tested point at 3,200 rps and overload in the 5,000-rps spike on this workload/host; it does not establish production capacity.

## Resources and storage microbenchmark

- App sampled peak: 19.0% of host CPU capacity, 37.8 MiB RSS.
- Load generator sampled peak: 6.4% of host CPU capacity, 25.4 MiB RSS.
- Peak SQLite database plus WAL/SHM: 3.99 MiB; the benchmark database lived in temporary scratch and was removed after the run.
- `BenchmarkMetricsStoreRecord`, three 1-second samples: 103.7–116.9 µs/op, about 1,200 B/op and 37 allocations/op.

Resource sampling used one-second `/proc` reads. The generator and app shared the same host, and the upstream was intentionally trivial. The report should be rerun on deployment hardware with representative TLS, upstream latency, and traffic mix before using it for capacity planning.
