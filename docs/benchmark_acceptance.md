# Benchmark acceptance harness

`orchestrator/benchmark` evaluates recorded benchmark windows without querying
hardware, starting a worker, contacting PostgreSQL, or changing pipeline
configuration. It is an acceptance gate, not a benchmark runner and it does
not create performance results.

## Measurement window

Each `MeasurementWindow` is one stable, bounded run of exactly one workload
class. Record the same workload class for the before and after windows.

| Field | Unit | Meaning |
| --- | --- | --- |
| `cpuAvgUtilizationPercent` | percent | Average host CPU utilization during the window. |
| `gpuAvgUtilizationPercent` | percent | Average GPU utilization during the window. |
| `throughputPerSecond` | completed work items/s | Completed comparable work divided by window duration. |
| `p95WaitSeconds` | seconds | 95th percentile admission, queue, or resource wait. |
| `oomCount` | count | OOM failures observed in the window. |
| `peakRamBytes` | bytes | Highest host RAM usage attributed to the benchmark boundary. |
| `peakVramBytes` | bytes | Highest dedicated VRAM usage attributed to the benchmark boundary. |

Use one of these representative workload classes:

- `short_mix`: a short, single-track mix; catches fixed scheduling overhead.
- `cue_album`: a multi-track CUE file; exercises queueing and file/track fan-out.
- `long_track`: a long single track; exercises peak host-memory behavior.
- `demucs_stem_wavefront`: Demucs followed by bounded per-stem processing; exercises CPU/GPU hand-off and resource waits.
- `parallel_4_to_8`: the same fixed corpus at 4 and 8 queued tasks; exercises sustained overlap, throughput, and p95 admission wait.

Phase 3 closure requires a before/after pair for every listed class. The
`EvaluateSuite` API rejects incomplete evidence, including a missing 4–8
parallel run.

Do not compare different classes, input corpora, concurrency limits, model
versions, or measurement definitions. Capture those run conditions beside the
JSON files in the benchmark evidence; the evaluator intentionally does not
guess them.

## Acceptance policy

The pure `benchmark.Evaluate(before, after, limits)` function passes only when:

1. Both windows report `oomCount: 0`.
2. After throughput is at least before throughput (no tolerance-based regression).
3. After peak RAM and peak VRAM are at or below mandatory byte limits.
4. Optional CPU average, GPU average, and p95 wait ceilings, when nonzero, are met.

The report always includes CPU/GPU utilization and p95 wait even when their
optional ceilings are disabled. A malformed, incomplete, or incomparable input
is an evaluator error rather than a pass or fail result.

## Offline command

The command consumes already-recorded JSON and needs no GPU or live service:

```powershell
Set-Location .\orchestrator
go run .\cmd\benchmark-acceptance `
  -before .\evidence\before.json `
  -after .\evidence\after.json `
  -limits .\evidence\limits.json
```

Example input shapes (values are illustrative limits and measurements, not
benchmark claims):

`before.json` and `after.json` use this shape:

```json
{
  "workload": "demucs_stem_wavefront",
  "startedAt": "2026-09-13T00:00:00Z",
  "endedAt": "2026-09-13T00:10:00Z",
  "cpuAvgUtilizationPercent": 72.5,
  "gpuAvgUtilizationPercent": 64.0,
  "throughputPerSecond": 1.25,
  "p95WaitSeconds": 1.4,
  "oomCount": 0,
  "peakRamBytes": 17179869184,
  "peakVramBytes": 8589934592
}
```

`limits.json` uses this shape:

```json
{
  "maxRamBytes": 21474836480,
  "maxVramBytes": 10737418240,
  "maxCpuAvgUtilizationPercent": 90,
  "maxGpuAvgUtilizationPercent": 90,
  "maxP95WaitSeconds": 5
}
```

The command emits a JSON `Evaluation`, exits `0` for acceptance, `1` for a
measured rejection, and `2` for invalid input or command errors. Its
`throughputRatio` is `null` when the before window has zero throughput, because
a multiplicative ratio is then undefined; the no-regression comparison still
applies.
