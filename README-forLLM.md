@meta=text/POLYGLYPH×SIGMA/1
@origin=impl_ground_truth(F:/repo/flac_analyzer_forwin)
@codec=C:/Users/letwir/.harness/POLYGLYPH.md
@aud=LLM∀architecture
@lang=🇬🇧∪🇯🇵∪∑
@mode=SemanticBootstrap⊕GenerativeDecode
@ontology=Meaning≫SurfaceForm

# 🎵 FLAC Analyzer (Win) — LLM Architecture & Ground-Truth Spec

§0 ⚓ EMOJI_LEGEND (context_defined)
🎵:=Audio/FLAC/CUE | ⚙️:=Go_Orchestrator | 🧠:=GPU_ML_DSP(Demucs/CUDA)
⚡:=CPU_Lane(Librosa/Essentia) | 🛡️:=Admission/Gatekeeper/Guard | 💾:=SHM/WORM/DiskMmap
🗄️:=DB(SQLite/PostgreSQL/DLQ) | 🏷️:=VorbisComment_Tagger | 📊:=Prometheus/pprof/Dashboard
🔧:=Ops/CLI/Tooling | ⚠️:=Invariant/Delta/Gotcha

§1 🎯 SYSTEM_IDENTITY
System := Windows_64bit ∘ Go_Orchestrator(module=`flac_analyzer/orchestrator`, go=1.26.4) ⋈ Python_Daemons(`.venv`) ⋈ PostgreSQL(`raw.library_flac`)
Purpose: FLAC(+CUE_sheet|embedded_cuesheet) ↦ {Decode, MD5, Demucs_6s_Sep, DSP_Features(Librosa⊕Tensor_CUDA), Essentia_ONNX_Pred, VorbisTag_Upsert, PG_Persist}.
Binaries: `orchestrator.exe` ≡ `single-orchestrator.exe` (same source `./orchestrator` via `update.ps1` / `init.bat`; runtime behavior selected by CLI flags).
⚠️ SingletonLock: `lifecycle_lock_windows.go` acquires Win32 Mutex `Local\FlacAnalyzerOrchestratorLifetime` ⇒ `¬concurrent(daemon, -single-file)`.
⚠️ JobObject: `InitGlobalJob()` binds child Python PIDs (`AssignPidToJob`) ⇒ orchestrator exit/kill terminates child tree.

§2 ⚙️ ENTRYPOINTS, CLI & HTTP_ENDPOINTS
CLI(`orchestrator/main.go`):
  default(no `-single-file`):
    ResetStaleTasks() ∘ PurgeOrphanedQueueAndCacheFiles(queue_dir, 1h)
    ∘ StartMetrics(`:2112`) ∘ StartDispatcher ∘ StartDlqRetryScheduler
    ∘ StartConfigFileWatcher(`config.toml`, `config_watch_interval_sec`) ∘ StartHTTP(`:8080`).
  `-config <path>`: auto-probe `{config.toml, ../config.toml, orchestrator/config.toml}`.
  `-log-level {debug|info|warn|error}`: overrides `[orchestrator].log_level`.
  `-single-file <path>`: `NumWorkers:=1 ∧ DemucsConcurrentLimit:=1`; sequential CUE-expanded sync execution + sync PG ingest (`synchronousIngest=true`).
    ∧ `-force`: re-analyze completed tracks (`¬combinable(-unreg)`).
    ∧ `-unreg`: dual-DB preflight (SQLite + PG); skips `{PENDING,QUEUED,RUNNING}` in SQLite ∧ `{Skip}` when both SQLite=`COMPLETED` ∧ PG=`complete`. (`requires -single-file`, `¬-force`).
    ∧ `-check-only`: `requires -unreg`; opens SQLite `OpenReadOnly(mode=ro)` ∧ PG read-only preflight; ∅ claims, ∅ writes, ∅ analyzer pools.
    ∧ `-fix-record <id>`: `requires -single-file ∧ ¬{-force,-unreg,-check-only}`; repairs exact PG row where `id=$1 ∧ features='{}'`, verifies `audio_hash` match + guarded `UPDATE` (`repairEmptyJSONQuery`), never falls back to DLQ.

HTTP:
  `:8080/task` (`POST`): stat actual disk `FileSize` → `InspectCue` (`worker_cue.py`, sem=8; fallback single track#1) → `prepareAnalysisTasks` (PG preflight) → `EnqueueDurableBatch` (SQLite) ⇒ `202 Accepted | 200 Skipped`.
  `:8080/reload` (`POST`): TOML reload + `UpdateConfig` diff JSON.
  `:8080/config` (`GET`): active normalized `Config` JSON.
  `:2112/metrics` (`GET`): Prometheus metrics (`metrics/metrics.go`).
  `:2112/debug/pprof/` (`GET`): live Go pprof (`_ "net/http/pprof"`).

§3 🛡️ CONFIG, ADMISSION & RESOURCE_GUARDS (`config/`, `planner/`, `dispatcher/`)
ConfigDefaults(`config/loader.go`):
  `max_ram_ratio=0.625` (clamp `≤0.95`), `cpu_worker_ratio=0.80`, `estimated_worker_ram_gb=1.75` (`config.toml.example` sets `3.5`),
  `min_avail_ram_gb=1.75`, `min_avail_disk_gb=5.0`, `shm_expansion_ratio=3.5`, `shm_retry_count=5`, `shm_retry_delay_sec=8`,
  `enable_virtual_lock=true`, `min_working_set_mb=512`, `max_working_set_mb=max(16384, floor(0.75*RAM_MB))`,
  `skip_dup_by_hash=true`, `enable_disk_mode_fallback=true`, `disk_mode_ram_threshold_ratio=0.8`,
  `gatekeeper_retry_delay_sec=20`, `gatekeeper_max_retries=5`, `config_watch_interval_sec=600`,
  `enable_dlq_retry=true`, `dlq_retry_interval_sec=600`, `db_timeout_sec=20`,
  `max_gpu_utilization_ratio=0.85`, `min_avail_vram_gb=0.5`, `estimated_demucs_vram_gb=1.0`, `enable_gpu_throttle=true`,
  `feature_extract_timeout_sec=300`, `demucs_timeout_sec=300`, `adaptive_timeout_ratio=1.5`, `max_adaptive_timeout_sec=7200`.
⚠️ HardPin: `NormalizeConfig` & `NewDispatcher` force `DemucsConcurrentLimit=1` ∧ `DemucsDaemonCapacity=1` (`NewAdaptiveDemucsScheduler` clamps `maxCapacity>1 ⇒ 1`). Dual-Demucs logic in `demucs_scheduler.go` is compiled/tested but runtime-capped to 1.

PurePlanner(`planner/resource_plan.go`):
  `DefaultResourceProfile`: `StemCount=7`, `FileExpansionRatio=3.5`, `PcmWorkingRatio=1.8`, `DiskModeRamBytes=2GiB`,
    Stages = `{demucs(1GiB_res, 1.8×), librosa(CPU, 0.4×), tensor(0.6×, 512MiB_VRAM), essentia(CPU, 0.25×)}`.
  `SelectStorageMode`: `SHM` iff `(ShmRamBytes + minAvailRam) ≤ 0.8 * effectiveAvail`; else fallback `StorageModeDisk` (spool `*.npy` to `%TEMP%/flac_analyzer_cache/<hash>`).
  `MemoryPressureState`: hysteresis `enter ≥ 90%`, `resume ≤ 82%` ⇒ `ForceDisk=true` ∧ `arenaPool.Close()` when `activeTaskCount==0`.

Gatekeeper & GPU Contract(`gatekeeper.go`, `admission.go`, `sysinfo/gpu_windows.go`):
  `AdmissionController`: atomic token lease over `{HostRAMBytes, DedicatedVRAMBytes, DownstreamTracks=1, CPULanes=max(NumWorkers,1), GPULanes=1, DemucsSlots=1, DiskTempBytes}`.
  Task exceeding node budget ⇒ `ErrPermanentBudgetExcess` (`FAILED`, no infinite retry); transient deficit ⇒ `parkTaskForAdmission` (`FAILED_MAYBE_RETRY`, `next_evaluation_at`, worker remains unblocked!).
  `GpuMetrics` (`P1-I2` contract via `pdh.dll` + WMI): Dedicated VRAM strictly separated from Shared GPU memory;
    `GPURequired ∧ ¬DedicatedVramKnown` (stale `>10s`, multi-adapter ambiguity, or missing `dedicated_vram_total_gb` on WMI 4GB uint32 sentinel) ⇒ NOGO (fail-closed).
  `GPUArbiter` (`gpu_arbiter.go`): FIFO, cancellation-aware lock serializing CUDA ownership between `demucs_daemon.py` & `worker_gpu_daemon.py`.

§4 🔄 PIPELINE_MORPHISM & WAVEFRONT_EXECUTION
Preflight(`analysis_preflight.go` ∘ `analysis_decision.go`):
  Query PG `raw.library_flac` with `xmin::text::bigint` (`RowVersion`).
  Complete ⇔ `analyzed_at≠NULL ∧ meta.analysis_schema_version==1 ∧ nonEmpty(features.mix) ∧ nonEmpty(predictions) ∧ ∀s∈{bass,drums,vocals,other,guitar,piano}: nonEmpty(features.demucs[s])`.
  `DecideAnalysis` ∈ {`FullAnalysis` (`mix`+6 stems), `MixOnly` (`mix` only, skips Demucs), `StemsOnly` (6 stems, preserves PG `predictions`), `Skip`}.

Feeder(`durable_queue.go`, `state/db.go`):
  SQLite WAL `orchestrator.db` (`opQueue` cap 10000, single writer goroutine).
  Status ∈ {`PENDING`, `QUEUED`, `RUNNING`, `COMPLETED`, `FAILED`, `FAILED_MAYBE_RETRY`}; PK=`(file_path, track_number)`.
  Startup: `QUEUED → PENDING`, `RUNNING → FAILED_MAYBE_RETRY` (payload preserved).
  Claim policy: `PendingTaskOrderSizeAscending` (`priority_score` = duration_sec or `FileSize/176400`), 1 task per ready worker.

TrackPipeline(`pipeline_step.go`):
  1. `HashCheck`: fast-path FLAC `STREAMINFO` MD5 (single non-sliced track) else `worker_hash.py` (`flac_decode.py` ↦ 32-hex MD5 of PCM) → PG check (`SkipDupByHash` when `AnalysisRowID==0`).
  2. `Demucs / DecodeMix` (`pipeline_demucs.go`, `demucs_daemon.py`):
     `MixOnly` ⇒ `decode_mix` (no Demucs).
     `FullAnalysis|StemsOnly` ⇒ allocate 6|7 SHM arenas (`CreateFileMappingW` + `VirtualLock` if enabled) or Disk `cacheDir` → acquire `AdaptiveDemucsScheduler` + `GPUArbiter` → `demucs_daemon.py` (`htdemucs-6s` ONNX, `UseDml=false`).
     ⚡🧠 **Per-Stem Wavefront (`StemLifecycle` + `stemWavefront`)**:
       As `demucs_daemon.py` writes+closes each stem SHM/npy and emits NDJSON `stem_ready`, Go freezes SHM (`VirtualProtect(PAGE_READONLY)`, WORM) and dispatches:
       - ⚡ `WorkerDaemonRoleCPU` (`worker_cpu_daemon.py`, pool=`NumWorkers`, prewarm=1, `CUDA_VISIBLE_DEVICES="-1"`, `maxRecycle=100`): `librosa_extractor` (∀stems) + `essentia` ONNX (`CPUExecutionProvider`, `mix` only, 64 mel patches).
         (`cpuConsumers = min(NumWorkers, len(stems))` iff `ShortSingleTrack(<30m)` else `1`).
       - 🧠 `WorkerDaemonRoleFeatureGPU` (`worker_gpu_daemon.py`, pool=`1`, requires `torch.cuda.is_available()`, `maxRecycle=100`): `extract_tensor_features` (CUDA, under `GPUArbiter`).
       Post-features: `CleanupGPU` (`gc.collect()` + `torch.cuda.empty_cache()`) + `gpuDaemonPool.TrimIdle(0)` ⇒ zero idle VRAM retention!
       ⚠️ Legacy note: `worker_daemon.py` exists for backwards compatibility, but `orchestrator` actually spawns `worker_cpu_daemon.py` ∧ `worker_gpu_daemon.py`.
  3. 🏷️ `Tagging` (`pipeline_tagger.go`, `flac_tagger.py`):
     Writes temp JSONs to `queue_dir` (RAII `defer cleanupQueueFiles`) → `flac_tagger.py` runs idempotent VorbisComment upsert (`missing_tags` unless `--force`), atomic write, restores Win32 `atime`/`mtime`/`ctime` (`SetFileTime`).
  4. 🗄️ `AsyncIngest` (`ingest_pgx.go`):
     `ingestQueue` (cap `1000`, decoupled from compute workers) merges `Librosa⊕Tensor` into `features` (`{"mix":..., "demucs":{...}}`), `Essentia` into `predictions`, injects `meta.analysis_schema_version=1`.
     - New track: `INSERT INTO raw.library_flac ... ON CONFLICT (audio_hash) DO NOTHING`.
     - Partial row (`AnalysisRowID>0`): optimistic-locked `UPDATE ... WHERE id=$12 AND xmin::text::bigint=$13 AND audio_hash=$14` (conflicts → `FAILED_MAYBE_RETRY`).
     - `-fix-record`: `repairEmptyJSONQuery` (`WHERE ... AND features='{}'`).
     - PG failure/timeout (`db_timeout_sec`): fallback `saveToSQLiteDLQ` (`send_failed.db` table `failed_payloads`) ⇒ `FAILED_MAYBE_RETRY`; auto-retried by `zig/retry_ingest.py` at startup & every `dlq_retry_interval_sec`.

§5 🔬 DSP & ML INVENTORY (`analyzer/`, `models.py`, `models/`)
`AudioContext` (`analyzer/core.py`): lazy CSE cache `{stft(n_fft=2048,hop=512), spectro, power, mel(128), chroma, chroma_cqt, onset_env, tempogram, tempobeat, centroid}`; `FIXED_SEQ_FRAMES=32`.
Active CPU Extractors (`analyzer/librosa_dsp.py`):
  `librosa_dynamics` (RMS, crest, energy, SNR, dynamics seq), `librosa_spectral` (centroid, rolloff, flatness, bandwidth, contrast, ZCR), `librosa_tonal` (chroma, key, tonnetz), `librosa_rhythm` (BPM, beat_regularity/stability, groove, onset, tempogram), `librosa_timbre` (MFCCs), `librosa_vocalpitch` (dominant pitch, vocal F0), `scipy_stats` (hilbert, peak, skew/kurtosis/entropy).
Active GPU Tensor DSP (`analyzer/tensor_dsp.py`):
  CUDA batch STFT, Wiener-Khinchin 2N zero-padded cuFFT HNR/NAP, Welch PSD, Hilbert envelope/phase, spectral/key/chord features.
Essentia ONNX (`analyzer/essentia_dsp.py`, `models.py`, `models/*.onnx`):
  16kHz `soxr` resample → `extract_mel_patches` (64 patches, `n_mels=96, patch_size=128, patch_hop=62`) → ONNX classifiers (`discogs-effnet` / `discogs-maest` heads: `approachability_3c`, `danceability`, `engagement_3c`, `gender`, `genre_discogs400`, `genre_dortmund`, `genre_electronic`, `genre_rosamerica`, `genre_tzanetakis`, `mood_*`, `voice_instrumental`, embeddings) + 24-chord Pearson seq + YIN vocal F0.
Optional Plugins (`analyzer.toml.example`, `execute=false` by default):
  `psychoacoustics_din45692.py`, `structure_ssm.py`, `voice_cpp.py`, `audio_cutoff_lufs.py`.

§6 🗄️ DATABASE_SCHEMA (`sql/schema.sql`, `sql/migration_v2.sql`)
PostgreSQL:
  `raw.library_flac`: `id(PK)`, `audio_hash(VARCHAR(32), UNIQUE)`, `filepath(IDX)`, `filename`, `track_number`, `album_artist(IDX)`, `album(IDX)`, `artist(IDX)`, `title(IDX)`, `meta(JSONB)`, `features(JSONB, GIN)`, `predictions(JSONB, GIN)`, `collected_at`, `analyzed_at`.
  `raw.library_flac_history`: populated automatically by `BEFORE UPDATE` trigger `trg_archive_library_flac` whenever `OLD.meta IS DISTINCT FROM NEW.meta ∨ OLD.features IS DISTINCT FROM NEW.features`.
  Roles: `etl_flac` (NOLOGIN), `analyzer` (NOLOGIN, SELECT), `ingester` (LOGIN, inherits `etl_flac`).
SQLite:
  `orchestrator.db`: `task_state(file_path, track_number, status, error_message, payload_json, priority_score, park_generation, next_evaluation_at, age_anchor_at, updated_at; PK(file_path, track_number))`.
  `send_failed.db`: `failed_payloads(audio_hash PK, ...)`.

§7 🔧 OPS_SCRIPTS, DASHBOARDS & TESTS
- `run_batch.ps1`: `pwsh` parallel HTTP `POST :8080/task` (default `Concurrency=8`), or `-SingleTask` (`single-orchestrator.exe`), `-Unreg`, `-UnanalyzedOnly` (`extract_unanalyzed_flacs.py`: diffs disk vs PG missing/empty `{}`), `-NoSync` (otherwise `sync_pg_completed_to_sqlite.py` syncs PG complete rows → SQLite `COMPLETED`), `-DryRun`, `-Test`.
- `fix.bat` → `fix_empty_json.py`: finds `features='{}'::jsonb`; `--apply` invokes `single-orchestrator.exe -single-file ... -fix-record <id>` + PG readback verify.
- `update.bat` → `update.ps1`: asserts orchestrators stopped → `unittest test_flac_tagger test_fix_empty_json` → `go test ./...` → `go build` → verifies `-fix-record` flag → SHA256-verified backup & atomic install to `{orchestrator.exe, single-orchestrator.exe, orchestrator\orchestrator.exe}` (`-CheckOnly` skips install).
- `zig/`: `dashboard.py` (TUI reading `:2112/metrics`), `retry_ingest.py` (DLQ replay), `check_tag_consistency.py`, `init_dl_model.py`, `migrate_features.py`, `migrate_hnr.py`, `repair_flac_tags.py`, `fix_empty_meta.py`, `inspect_track.py`, `verify_track4.py`, `update_hardware_specs.py`.
- `orchestrator/cmd/benchmark-acceptance`: offline regression evaluator comparing `MeasurementWindow` JSONs against `AcceptanceLimits`.
- `dashboards/`: Grafana YAML + generator scripts (`generate_*_dashboard.py`).
- Existing Tests: Go (`orchestrator/**/*_test.go`), Python (`tests/test_worker_gpu_daemon.py`, root `test_*.py`).
