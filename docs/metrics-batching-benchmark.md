# Metrics batching experiment

Date: 2026-10-24. Raw benchmark artifacts are local-only and ignored by Git; regenerate them with `scripts/benchmark.sh`.

## Change under test

The synchronous baseline performed one SQLite transaction per request: increment a counter, insert an event, prune old events, and commit. The experiment adds a bounded 8,192-item in-memory queue and one SQLite writer. It drains already-queued writes into batches of at most 256 without a fixed batching delay, increments each counter by the full batch size, and inserts only the newest 20 events from that batch because older events would be pruned immediately. `Record` still waits for its batch commit. `Close` stops admission and drains accepted events.

This is not durable queueing: a process crash can lose events not yet committed. The HTTP handler logs store failures and continues its functional response. SQLite remains WAL with `synchronous=NORMAL`.

## HTTP results

Host: Go 1.27.3, Linux/amd64, Intel Core i5-8265U, 8 logical CPUs, 15.5 GiB RAM. App and synthetic zero-work upstream ran on loopback on the same host. Mixed workload is 80% honeypot route / 20% proxied route. Lower rates use 128 load-generator workers; rates above 5,000 use 512 workers. The health-only control bypasses metric persistence and uses 4,096 workers. Capacity rates ran twice for 8 seconds.

| Mixed offered rate | Delivered rate | Client queue drops | HTTP/network errors | p99 median / worst |
|---:|---:|---:|---:|---:|
| 3,200 rps | 3,199.9 rps | 0 | 0 | 4.21 / 7.08 ms |
| 5,000 rps | 4,999.9 rps | 0 | 0 | 2.25 / 2.59 ms |
| 10,000 rps | 9,995.9 rps | 0 | 0 | 40.22 / 49.55 ms |
| 20,000 rps | 19,980.0 rps | 0 | 0 | 58.34 / 60.35 ms |
| 60,000-rps spike | 22,235.3 rps | 559,544 | 0 | 65.85 / 65.85 ms |
| 10,000-rps soak, 60 s | 9,999.9 rps | 0 | 0 | 13.67 / 13.67 ms |
| 20,000-rps recovery | 19,994.4 rps | 0 | 0 | 52.55 / 52.55 ms |

The highest tested mixed rate meeting the local benchmark heuristic in both repetitions was 20,000 rps. At 60,000 offered rps, bounded load-generator queue drops indicate overload; those are not HTTP rejections. The 100,000-rps health-only control delivered 54,085.6 rps but had 447,791 client queue drops and p99 of 380 ms. Therefore this run does **not** demonstrate 100,000 end-to-end HTTP requests/s, even without SQLite writes; the generator and app shared the same host and the control itself was overloaded.

The previous published campaign's highest sampled healthy step was 3,200 rps, and its 5,000-rps spike delivered about 4,249 rps with 9,816 generator-queue drops. The new result is substantially better under the tested mixed workload, but benchmark variability, changed generator concurrency at high rates, and same-host contention mean it is not a production capacity claim or a precise limit.

## Store microbenchmarks

On the same host, the serial `BenchmarkMetricsStoreRecord` measured 152.8–285.2 µs/op in three 1-second samples (3,222–3,224 B/op, 65 allocations). The 128-caller `BenchmarkMetricsStoreRecordParallel` measured 3.67–4.12 µs/op (about 243,000–272,000 operations/s aggregate, 279–284 B/op, 3–4 allocations). The parallel store microbenchmark is not an end-to-end HTTP measurement.

## Resources and caveats

Sampled app peak was 40.5% of host CPU and 146.7 MiB RSS; generator peak was 47.1% and 284.1 MiB RSS. The temporary database plus WAL/SHM peaked at 3.99 MiB. Resource sampling was one-second `/proc` sampling. The SQLite microbenchmark and HTTP load generator are both local synthetic workloads; rerun with representative deployment hardware and upstream traffic before capacity planning.
