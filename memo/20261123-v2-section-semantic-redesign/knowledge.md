# Knowledge: Acoustically Sound `extract_section_Large()` Redesign & 103 GB Idle-RAM Speedup Analysis

- **source**: `F:\repo\flac_analyzer_forwin` (`origin/v2`)
- **scope**: `project:flac_analyzer_forwin`
- **verified-date**: 2026-11-23

## 1. Acoustic Section Redesign (`analyzer/librosa_dsp.py`)
- Replaced ratio-based 5% sliding RMS + unconditional top-`k` (`argsort[-k:]`) in `extract_section_Large(ctx)` (which collapsed all tracks >13m to `section_count=40, section_length_std≈0.02s`) with fixed physical 2.0s block RMS pooling (`np.add.reduceat`), Gaussian smoothing (`sigma=1.0`), and prominence-gated `scipy.signal.find_peaks` (`height >= max(med + 1.2*std, 0.12*rms)`, `prominence >= max(0.8*std, 0.08*rms)`, `distance >= min_section_sec / block_sec`).
- Verified semantically (37/37 Python tests + Go tests): silence & 75m stationary signal yield `section_count == 1, std == 0.0`; symmetric & asymmetric synthetic transitions yield exact `section_count == 3` and matching variance.
- Real DB comparison across all 185 `testFLAC/*` rows (`updated=185, missing=0`): `無限回廊` (75.1m) `40 (0.055s) -> 59 (83.51s)`, `ユビノタクト` `40 (0.02s) -> 9..74 (12.3..95.2s)`, `桃色CODE` #5/#6 `40 (0.01s) -> 29 (20.9s) / 18 (46.0s)`.

## 2. Live Resource Telemetry & RAMDisk / Idle-RAM Acceleration Findings
- Live production `orchestrator.exe` (PID 12296) telemetry: `RAM total=127.9 GB, used=24.8 GB (19.4%), avail=103.1 GB`, `VRAM used=9,066 MiB / 16,303 MiB`.
- RAMDisk cannot execute CUDA kernels directly (WDDM Shared GPU Memory handles VRAM overflow automatically). Instead, the >100 GB idle host RAM accelerates throughput cleanly via:
  1. Expanding `admission.go` `DownstreamTracks: 1 -> 4..8` (SHM or RAMDisk `storage_mode="disk"`) so Demucs GPU stays ~100% busy while multiple CPU workers drain separated stems in parallel;
  2. Writing chunked GPU `torch.stft` `*.spectro.npy` to RAMDisk `R:\flac-cache` (consumed via existing `spectro_path` `mmap_mode="r"` in `AudioContext.spectro`), skipping 7-stem CPU `librosa.stft` with zero NVMe wear;
  3. Host RAM decode + `soxr` lookahead prefetch (2–4 tracks).
