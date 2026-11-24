# v2 Phase A–D 完了報告書 (PD-S2 統合監査・実測ゲート)

- ブランチ: `v2` (`origin/v2` 同期)
- 期間: 2026-11-23 〜 2026-11-24
- 対象計画: [`issues.md`](../../issues.md) §15 (`PA-S1` 〜 `PD-S2`、4 中目標 / 11 小目標)

---

## 1. Phase A–D アーキテクチャおよび実装サマリー

### Phase A: 並行基盤・ライフタイム・Windows ハンドル安全化
- **PA-S1** (`9eee00f`, Sol Verifier PASS): `ShmArenaPool` (`orchestrator/dispatcher/shm_windows.go`) の並列マップ競合修正、singleflight `ArenaLease`、`Close`/`CloseIdle`、並行ストレステスト (`shm_windows_test.go`)。
- **PA-S2** (`2c7d2a1`, 条件付き): `newStemWavefront` 単一 `sync.Once` ティアダウン、非保持 GPU デーモンの `DeferRetire` 経路、`CleanupGPU` クライアント固定、有界非同期プールロガー、`DiskTempBytes` 伝播と Disk 容量最終検証。
- **PA-S3** (`c4052e4`, 条件付き): リクエスト単位キャッシュディレクトリ (`creating-`/`req-`/`removing-` 名前空間、`owner.json` 原子書込、Windows リパースポイント/PID生存検証 `cache_dir_windows.go`)。

### Phase B: DownstreamTracks 拡大・3層 Admission 台帳・有界ターン GPUArbiter
- **PB-S1** (`71e7f7b`, Sol Verifier PASS): `MaxDownstreamTracks`, `RetainGpuDaemonIdle`, `DiskCacheDir`, `SpectroCacheDir`, `SpectroCacheMinFreeGB` 追加、`MinAvailVramGB` 1 GiB 下限統一、`main.go` の `-single-file` 強制 cap=1、ホットリロード差分検証。
- **PB-S2** (`7f044f2` → `PD-S2` で残件 1–3 解消): `AdmissionController` の 3 層台帳分離 (`AdmissionTierDownstream`, `AdmissionTierGPUExecution`, `AdmissionTierRetainedResidency`)、`ResolveDownstreamTracksCapPure`、キュー受理と起動時 GPU 空き判定分離。
  - `PD-S2` 解消分: `gatekeeper.go` (`EvaluateGoNoGoPure` / `EvaluateGPULaunchPure`) で `EvaluateQueueAdmissionPure` の明示的バイパスを維持しつつ共有 VRAM 容量判定へ統一、`admission.go` で有効な GPU 専用 VRAM 計測時のみ容量更新＋未初期化 `0` 容量の起動時補完、`pipeline_features.go` で GPU arbiter 取得後・`AcquireTracked` 前に `retainGPUIdle` を 1 回スナップショット化して全分岐（取得失敗時含む）で一貫利用。
- **PB-S3** (`3e09b3f`, 条件付き): 有界ターン `GPUArbiter` (`gpu_arbiter.go`: `AcquireDemucs`, `AcquireFeatureBatch`, `RegisterPostDemucsFeatureTurn`) と `WaitFirstGPUFeatureQueued` による最古 Demucs 前の最大 1 ターン優先調停。

### Phase C: CPU 専用チャンク GPU STFT キャッシュ・Per-Volume 容量台帳・2段 Wavefront
- **PC-S1** (`09f7f7b`, 条件付き): `analyzer/tensor_dsp.py` に `compute_and_save_spectro_chunked_Large` (`T = 1 + N // 512`, `center=False`, `periodic=True` Hann, `open_memmap` flush+close→`os.replace`, OOM 時 2048→1024 リトライ) を追加し、`extract_tensor_features` の 1024-reflect 不変性を維持。`analyzer/core.py` は形状/dtype 検証付きロードと即座のコピー＆確定的 `mmap.close()` を実装。
- **PC-S2** (`3096622`, 条件付き): `volume_budget.go` / `volume_budget_windows.go` による OS ボリューム単位バイト予約台帳と `admission.go` のキャッシュ残量連動。
- **PC-S3** (`3da927d`, Sol Verifier R3 PASS): `SpectroCacheOutcome` (`written`/`fallback`/`skipped`)、`pipeline_demucs.go` の `newTwoStageStemWavefront` (`Demucs -> gpuJobs -> cpuJobs`, 単一 `cpuJobs` クローズ、`mix -> vocals` 順序保証、CPU STFT 自動フォールバック、`Wait()` 後キャッシュ削除)。

### Phase D: 実測ロールアウトゲート＆統合監査
- **PD-S1** (`4c4f251`, 条件付き): `worker_gpu_daemon.py` の `spectro_cache` 結果通知、`tests/test_phase_d_rollout_gate.py`、`orchestrator/dispatcher/rollout_gate_test.go` (`TestPhaseDRolloutGateDownstream1Vs4`) を構築。
- **PD-S2** (本ドキュメント・最終統合コミット): `PB-S2` 残件 1–3 および `PD-S1` の `run_batch` スパイ競合 / `os.replace` 一時ファイル検証を修正し、Go・Python 全テストと統合 Verifier を実施。

---

## 2. 実機実測ベンチマーク・テスト検証結果 (PD-S2 最終修正後)

### 2.1 Go 全パッケージ (`cd orchestrator; go vet ./...; go test -count=1 ./...`)
- `flac_analyzer/orchestrator`: PASS (1.693s)
- `flac_analyzer/orchestrator/benchmark`: PASS (0.532s)
- `flac_analyzer/orchestrator/cmd/benchmark-acceptance`: PASS (0.614s)
- `flac_analyzer/orchestrator/config`: PASS (0.469s)
- `flac_analyzer/orchestrator/dispatcher`: PASS (15.191s)
- `flac_analyzer/orchestrator/planner`: PASS (0.374s)
- `flac_analyzer/orchestrator/state`: PASS (1.374s)
- `flac_analyzer/orchestrator/sysinfo`: PASS (2.169s)
- 注記 (`-race`): 本 Windows 環境には `gcc` が導入されていないため (`Get-Command gcc` 空出力)、`go test -race` は未実施。

### 2.2 Python 全スイート (`.venv\Scripts\python.exe -m unittest discover -s tests -p "test_*.py"`)
- **全 37 テスト PASS (41.215s)**
- 実 FLAC CUDA ロールアウトゲート対象ファイル: `testFLAC/01_01_シンカンセンスゴイカタイアイス.flac`
- PyTorch CUDA ピーク割り当て (`torch.cuda.max_memory_allocated` / 1024²) / テストプロセス事後 Windows Working Set (`GetProcessMemoryInfo.WorkingSetSize` / 1024²) 実測 (MiB):
  - `Downstream 1 vs 4` (Python CUDA スイート):
    - cap=1: PyTorch peak CUDA allocation = `24.00 MiB`, テストプロセス RSS = `1196.00 MiB`
    - cap=4: PyTorch peak CUDA allocation = `66.01 MiB`, テストプロセス RSS = `1233.16 MiB`
  - `SpectroCache OFF vs ON`:
    - OFF: PyTorch peak CUDA allocation = `46.20 MiB`, テストプロセス RSS = `1244.70 MiB`
    - ON: PyTorch peak CUDA allocation = `46.20 MiB`, テストプロセス RSS = `1254.56 MiB`
- Go オーケストレータ実デーモン (`TestPhaseDRolloutGateDownstream1Vs4` via `go test -json`):
  - `AdmissionTierDownstream` 同時リース最大観測数: `cap1_concurrent = 1`, `cap4_concurrent = 4`（実デーモン処理区間の時間重複計測および Go 子プロセス毎の RSS/VRAM 計測は `PD-S1` #4–5 持ち越し）
  - 4 トラックすべての `librosa` / `tensor` / `essentia` JSON について `_assert_json_parity(..., exact=True)` による値一致（同一スカラー型の厳密一致、および異なる数値型間の `==` 比較；数値型同士の厳密型チェックは `PD-S1` #3 として持ち越し）、`orphan_count = 0`

---

## 3. Verifier 残件の対処台帳（解消済 vs 持ち越し）

### 本フェーズで解消・コード反映済
- `PB-S2` #1 (`gatekeeper.go:227`): unknown-telemetry ガード通過後の共有 VRAM 容量検査条件を `in.GPURequired || (in.EnableGpuThrottle && in.MinAvailVram > 0)` とし、GPU 必須時は `MinAvailVram == 0` やスロットル設定に関わらず常に VRAM 予算 (`EstimatedTaskVram + MinAvailVram`) を検査し、GPU 非必須時は `EnableGpuThrottle && MinAvailVram > 0` のときのみ検査（`EvaluateQueueAdmissionPure` は GPU 必須・スロットルを無効化してバイパス維持、`EvaluateGPULaunchPure` は重複判定を廃して委譲）。
- `PB-S2` #2 (`admission.go`): `dedicatedKnown` 時のみ `DedicatedVRAMBytes` 更新＋起動時 `0` 容量補完。
- `PB-S2` #3 (`pipeline_features.go`): GPU arbiter 取得後・`AcquireTracked` 前に `retainGPUIdle` を 1 回スナップショット化し、取得エラー経路と完了/defer 経路で一貫利用。
- `PD-S1` #1, #6 (`tests/test_phase_d_rollout_gate.py`): バッチ共有のロック付き `np.load` スパイ化、および `os.replace` 失敗注入時に `_REAL_NP_LOAD(src, mmap_mode=None)` で `(1025, expected_frames)` / `float32` 実在を検証したうえで例外送出・一時ファイル削除を確認。

### 既知の持ち越し事項（[`issues.md`](../../issues.md) 記載）
- `PA-S2`: R6 指摘 3 件はコード修正済・全 Go テスト PASS だが **修正後の Verifier 再検証は未実施**（実機長時間運転のハンドル/mmap 検証・`gcc` 不在による `-race` 未実施を含む）。
- `PA-S3`: ディレクトリ外永続レシート、プロセス間ロック、ディレクトリハンドル走査 TOCTOU 等 6+3 件。
- `PB-S2` #4 (`pipeline_features.go`): watcher 単位クライアント紐付け (`TrimIdleWithWatchers` の対象特定解放)。
- `PB-S3` #1, `PC-S1` #1–4, `PC-S2` #1–4: R3 指摘はすべてコード修正済かつ全 Go/Python テスト PASS だが、[`issues.md`](../../issues.md) に記録のとおり **各チケットの修正後 Verifier 再検証は未完了（pending）**。
- `PD-S1` #2–5: CPU デーモン側 mmap ヒット明示テレメトリ、型厳密統一、`extract` オーバーラップ秒区間計測、Go 起動デーモン子プロセス毎の RSS/VRAM 計測。

---

## 4. コミット履歴 (`v2`)

| コミット | 内容 |
| --- | --- |
| `2cb5aed` | `issues.md` §15 Phase A–D 計画追加 |
| `9eee00f` | `PA-S1` `ShmArenaPool` スレッドセーフ化 |
| `2c7d2a1` | `PA-S2` Wavefront 単一 teardown・非同期ロガー等 (条件付き) |
| `c4052e4` | `PA-S3` リクエストキャッシュディレクトリ隔離 (条件付き) |
| `71e7f7b` | `PB-S1` 設定拡張・1 GiB 下限・single-file cap=1 |
| `7f044f2` | `PB-S2` 3 層 Admission 台帳・キュー/起動分離 (条件付き) |
| `3e09b3f` | `PB-S3` 有界ターン `GPUArbiter` (条件付き) |
| `09f7f7b` | `PC-S1` チャンク化 GPU STFT キャッシュ・`mmap` 解放 (条件付き) |
| `3096622` | `PC-S2` OS ボリューム単位バイト予約台帳 (条件付き) |
| `3da927d` | `PC-S3` 2 段 Wavefront・`SpectroCacheOutcome` 統合 (PASS) |
| `4c4f251` | `PD-S1` 実 FLAC・Go 実デーモン統合ロールアウトゲート (条件付き) |
| (pending) | `PD-S2` 統合監査・`PB-S2`/`PD-S1` 残件解消・完了メモ（コミット＆`issues.md`確定予定） |
