# Walkthrough: Demucs Resident Daemon & Adaptive GPU Single/Dual Scheduler

- **Summary**: Demucs 波形分離処理において、常駐型ワーカーデーモン (`DemucsDaemonPool` & `demucs_daemon.py`) によるモデルロード時間・プロセス起動オーバーヘッドの完全撤廃、および GPU 負荷・VRAM 空き容量に応じたアダプティブ Single/Dual スロット制御 (`AdaptiveDemucsScheduler`) を実装・検証完了。
- **Changes**:
  - `demucs_daemon.py` [NEW]: `HTDemucsSeparator`（ONNX）を起動時に 1 回だけ GPU VRAM にロード。NDJSON IPC で `check_hash` および `separate` をオンメモリ高速実行。Advisory 1 遵守により書き込み後即座に `shm.close()` でハンドル解放。
  - `orchestrator/dispatcher/demucs_daemon.go` [NEW]: `DemucsDaemonClient` & `DemucsDaemonPool` (Windows JobObject 管理, 自動リカバリ, 50タスクリサイクル)。
  - `orchestrator/dispatcher/demucs_scheduler.go` [NEW]: 決定論的純粋射 `DetermineDemucsSlotLimitPure` と、2回連続判定ヒステリシス制御を備えた `AdaptiveDemucsScheduler`。
  - `orchestrator/dispatcher/dispatcher.go`: Step 2.1 (HashCheck) および Step 3 (Demucs) を常駐デーモンプール経由に切り替え。
  - `orchestrator/metrics/metrics.go`: `analyzer_demucs_dynamic_limit`, `analyzer_demucs_daemon_active_slots`, `analyzer_demucs_daemon_pool_size` を追加。
  - `orchestrator/main.go`, `config.toml`, `config_test.toml`, `reload_test.go`: `demucs_daemon_capacity` (2), `demucs_dual_gpu_util_threshold` (0.50), `demucs_dual_min_vram_gb` (4.0) を追加。
- **Verification**:
  - `go test -v ./...` (orchestrator): 全単体テスト PASS (`ok flac_analyzer/orchestrator/dispatcher 34.221s`)
  - `python -m unittest tests/test_demucs_daemon.py`: OK (19.767s)
  - `proof-checker.exe`: PASS (0 Errors)
  - Auditor & Verifier サブエージェント審査: 満場一致の PASS

# Walkthrough: GPU Resource Observation & Dynamic Allocation

- **Summary**: Windows Native API (PDH / CIM) による GPU 使用率 (%) および Dedicated / Shared VRAM のリアルタイム観測モジュール、Prometheus 可観測性メトリクス拡張、Gatekeeper による GPU 過負荷防止動的リソース配分・スロットリングを実装・検証完了。
- **Changes**:
  - `orchestrator/sysinfo/gpu_windows.go` [NEW]: Windows Performance Counters (CIM/WMI) による GPU 負荷率・Dedicated/Shared VRAM のバックグラウンド定期収集ループ (`GpuCollectorDaemon`) および Lock-free キャッシュ (`atomic.Pointer[GpuMetrics]`)。
  - `orchestrator/sysinfo/gpu_test.go` [NEW]: 初期値安全性・VRAM 計算境界値テスト。
  - `orchestrator/metrics/metrics.go`: `analyzer_gpu_utilization_percent`, `analyzer_gpu_dedicated_used_bytes`, `analyzer_gpu_wait_seconds`, `analyzer_gpu_throttle_events_total` などの Prometheus 可観測性メトリクス追加。
  - `orchestrator/dispatcher/dispatcher.go` & `stats.go`: `GatekeeperInput` 構造体による純粋判定関数 `EvaluateGoNoGoPure` を拡張し、GPU 負荷率（閾値: 85%）や VRAM 不足時の安全スロットリング・待機時間記録を実装。
  - `orchestrator/main.go` & `config.toml`: `max_gpu_utilization_ratio`, `min_avail_vram_gb`, `estimated_demucs_vram_gb`, `enable_gpu_throttle` を追加し、無停止動的ホットリロードに対応。
  - `gatekeeper_test.go` & `reload_test.go`: GPU 過負荷・VRAM 枯渇・スロットル無効化・動的リロードの単体テストを整備。
- **Verification**:
  - `go test -v ./...` (orchestrator): 全単体テスト PASS (`ok flac_analyzer/orchestrator/dispatcher 32.276s`, `ok flac_analyzer/orchestrator/sysinfo 0.998s`)
  - `proof-checker.exe`: PASS (0 Errors)
  - Auditor & Verifier サブエージェント審査: 満場一致の PASS

# Walkthrough: WorkerDaemon 常駐プロセス化 & Warmup 49秒削減

- **Summary**: `worker_daemon.py` の常駐プロセス化および `WorkerDaemonPool` の Go オーケストレーター統合により、従来の 3 プロセス起動オーバーヘッド（~2秒）と 60 回の CPU Warmup ループ（49.2秒）を完全撤廃。1曲あたり約 50 秒のレイテンシを削減し、2〜3秒/曲での Zero-copy 一括特徴量抽出を達成。
- **Changes**:
  - `worker_daemon.py`: ADV-01 (CPU Warmup ループ完全撤廃), ADV-02 (`torch.cuda.empty_cache()` の純粋関数からの副作用分離), ADV-03 (`ctx.clear()` の順序修正による use-after-free 根絶), 詳細プロファイル返却。
  - `orchestrator/dispatcher/daemon.go` [NEW]: `WorkerDaemonClient` (NDJSON IPC, Windows JobObject 管理, ハンドシェイク検知, `context.WithTimeout`, RAII)。
  - `orchestrator/dispatcher/daemon_pool.go` [NEW]: `WorkerDaemonPool` (スレッドセーフ接続プール, 100タスクセルフリサイクル, 自動リカバリ)。
  - `orchestrator/dispatcher/dispatcher.go`: `Dispatcher` に `daemonPool` を組み込み、Step 5 を `WorkerDaemonPool.ExtractAll` に切り替え。
  - `orchestrator/dispatcher/daemon_test.go` [NEW]: `TestDaemonPingPong`, `TestDaemonPoolAcquireRelease`。
  - `decisions.md`: §1 に常駐ワーカーデーモンプール構成を追記。
- **Verification**:
  - `go test -v ./dispatcher/...`: 全テスト PASS (`ok flac_analyzer/orchestrator/dispatcher 29.741s`)
  - `python -m unittest tests/test_worker_daemon.py`: OK (32s)
  - `proof-checker.exe`: PASS (0 Errors, 0 Warnings)
  - Verifier Gate (`claude-sonnet-4-6`): PASS_WITH_ADVISORIES 獲得 → ADV-A1 修正完了

# Walkthrough: 音響解析パイプラインの高速化 & 圏論的リファクタリング (Phase 1〜3)

- **Summary**: 命題1〜3に基づき、1) Go オーケストレーターからの PostgreSQL 直接 UPSERT (`ingest_pgx.go`) による中間ファイル I/O と `ingester.py` 起動オーバーヘッドの完全撤廃、2) 常駐型ワーカーデーモン (`worker_daemon.py`)、3) Wiener-Khinchin $2N$ パディング cuFFT HNR/NAP & 7ステム一括 STFT / スペクトル特徴量 GPU テンソル DSP (`analyzer/tensor_dsp.py`)、4) 数学的等価性回帰テストスイート (`test_gpu_dsp_equivalence.py`) を実装・検証完了。
- **Changes**:
  - `gh release create v1.3.1`: ベースラインリリースを作成。
  - `orchestrator/dispatcher/ingest_pgx.go` [NEW]:
    - Go 内製 PostgreSQL Direct UPSERT および SQLite DLQ (`send_failed.db`) フォールバックを実装。
  - `orchestrator/dispatcher/dispatcher.go`:
    - `ingester.py` 呼び出しと中間 JSON ファイル生成を廃止し、Go Direct Ingest に切り替え。
  - `worker_daemon.py` [NEW]:
    - Go と NDJSON で通信する常駐型ワーカーデーモン。Advisory 2 に従い `try...finally: shm.close()` でハンドルリークを完全防止。
  - `analyzer/tensor_dsp.py`:
    - $2N$ ゼロパディング付き cuFFT による Wiener-Khinchin HNR/NAP (Advisory 1)、7ステム一括バッチ STFT、Spectral Centroid/Rolloff/Flatness/ZCR/Key 推定の GPU テンソル純粋射を実装。
  - `analyzer/librosa_dsp.py`:
    - `_calc_hnr_nap` を `tensor_dsp.calc_hnr_nap_tensor` へ委譲し、自己相関を $O(N^2) \to O(N \log N)$ へ高速化。
  - `tests/test_gpu_dsp_equivalence.py` [NEW]:
    - 全 6 特徴量に対する相対誤差 $< 10^{-4}$ の数学的等価性回帰テストを新設。
  - `tests/test_worker_daemon.py` [NEW]:
    - ワーカーデーモンの起動・ping-pong IPC テストを新設。
- **Verification**:
  - `python -m unittest tests/test_gpu_dsp_equivalence.py`: 全 6 テスト PASS
  - `python -m unittest tests/test_worker_daemon.py`: PASS
  - `go test -v ./...` (orchestrator): 全 Go テスト PASS
  - `proof-checker.exe -path .`: Verdict: PASS (0 errors, 0 warnings)
  - Auditor & Verifier サブエージェント審査: 満場一致の PASS

# Walkthrough: 計測器 (analyzer/*) の圏論的完全分離および分岐器・射 (worker_*.py) 再配置と重複ファイル一掃

- **Summary**: 音響特徴量の数理計算・DSP演算（計測器）をすべて `analyzer/*` パッケージに完全分離・局所化し、各ワーカー（`worker_tensor.py`, `worker_essentia.py` 等）をオーケストレーターとの入出力を媒介する純粋な分岐器・射へと純化。ルートの重複治具フォワーダー群（7ファイル）と旧 `load_wave.py` を一掃し、圏論的健全性を達成。
- **Changes**:
  - `analyzer/tensor_dsp.py` [NEW]:
    - PyTorch テンソル DSP 演算（`hilbert_envelope_phase`, `welch_psd`, `fft_bandpass_envelope`, `extract_tensor_features`, `extract_tensor_obj`, `tensor_extractor`）を純粋関数・Applicative 射として新設。
  - `analyzer/types.py`:
    - `TensorFeatures` データクラスを新設し、シリアライズ（`to_dict`）および FLAC タグ変換（`to_flac_tags`）を実装。
  - `analyzer/essentia_dsp.py`:
    - `extract_mel_patches` および `run_essentia_serialized` を集約・一元化。
  - `worker_tensor.py` & `worker_essentia.py`:
    - DSP 計算ロジックを排除し、`analyzer` パッケージの計測器を呼び出す純粋な射（SHM アタッチ → 抽出 → JSON 出力）へと純化。
  - `models.py`:
    - 計測ロジックを `analyzer.essentia_dsp` へ委譲し、ONNX セッション管理および `HTDemucsSeparator`（波形分離器 / 分岐器）に専念。
  - `pipeline.py`:
    - 旧マルチプロセス SHM モジュール `load_wave` への依存およびレガシー P/C コードを全廃。
  - ルートの不要・重複ファイル群（`fix_empty_meta.py`, `init_dl_model.py`, `inspect_track.py`, `migrate_hnr.py`, `retry_ingest.py`, `update_hardware_specs.py`, `verify_track4.py`, `load_wave.py`）を削除。
  - `tests/test_tensor_dsp.py` [NEW]:
    - PyTorch Tensor DSP の周波数ピーク検出・Hilbert 変換・Applicative 射の単体テストを新設。
- **Verification**:
  - `pytest tests/`: 全 33 テスト PASS (15.31s)
  - `proof-checker.exe -path . -strict`: Verdict PASS (0 errors, 0 warnings)
  - `go test -v ./...`: 全 Go テスト PASS
  - Auditor & Verifier サブエージェントによる検証: 満場一致の Verdict PASS

# Walkthrough: Ingester `NameError: name 'time' is not defined` Bug Fix

- **Summary**: PostgreSQL への UPSERT 処理時間計測のために追加された `time.perf_counter()` 呼び出しにおいて、`ingester.py` に `import time` が欠落していた NameError バグを修正いたしました。
- **Changes**:
  - `ingester.py`:
    - ファイル先頭の import セクションに `import time` を追加。
- **Verification**:
  - `pytest tests/`: 全 28 テスト PASS (15.72s)

# Walkthrough: Blackwell ONNX CPU Fallback Bug Fix & Metrics URL Identification

- **Summary**: Blackwell GPU 上で ONNX Runtime が CPU にフォールバックしていたバグを cuDNN `EXHAUSTIVE` 設定の強制適用および `gpu_mem_limit` 排除によって修正し、CUDA での高速推論を復旧。また、前回会話のログから VictoriaMetrics のメトリクス URL（`http://100.84.48.65:8428`）を特定・疎通確認した。
- **Changes**:
  - `models.py`:
    - `cuda_opts` 内の `"cudnn_conv_algo_search"` を `"EXHAUSTIVE"` に変更。
    - `gpu_mem_limit` 制限を削除して VRAM アロケーションのクラッシュを防止。
  - `tests/test_blackwell_onnx.py`:
    - 実 ONNX セッションを作成し、`CUDAExecutionProvider` が実際にバインドされることを確認するテストケースを追加。
- **Verification**:
  - `python -m unittest tests/test_blackwell_onnx.py`: PASS (20.3s)
  - `read_url_content` で `http://100.84.48.65:8428/api/v1/query` にアクセスし、MemAvailable や load1 が正常に取得できることを確認。
  - 暴走していた Python プロセスの cleanup を完了。

# Walkthrough: 残存 Issues (#7, #15, #16) の完全解決と Prometheus :2112/metrics 所要時間・進捗可視化

- **Summary**: 未解決であった残存 Issues（#7 Blackwell GPU 動作検証、#15 DB ⇔ FLAC タグ双方向整合性チェッカー、#16 リアルタイム CLI 進捗ダッシュボード）をすべて解決し、1ファイルあたりおよび1曲（トラック）あたりの所要時間計測を Prometheus `:2112/metrics` に集約・可視化。
- **Changes**:
  - `orchestrator/metrics/metrics.go`:
    - `analyzer_task_duration_seconds` (Histogram/Gauge), `analyzer_avg_task_duration_seconds` (Gauge: 1曲所要時間)
    - `analyzer_file_duration_seconds` (Histogram/Gauge), `analyzer_avg_file_duration_seconds` (Gauge: 1ファイル所要時間)
    - `analyzer_tasks_per_minute`, `analyzer_files_per_minute`, `analyzer_eta_seconds` (Gauge)
    - `analyzer_disk_free_bytes`, `analyzer_ram_available_bytes`, `analyzer_files_total` (Gauge/Counter)
  - `orchestrator/dispatcher/stats.go`:
    - `StatsTracker`: 1トラック/1ファイルの完了所要時間を EMA（$\alpha = 0.15$）で集約。
    - 60秒スライディングウィンドウによるスループット算出とキュー残量による ETA 算出。
    - バックグラウンドでシステム空き RAM・空きディスクを定期ポーリングする `StartSystemResourceCollector`。
  - `orchestrator/dispatcher/dispatcher.go` & `orchestrator/main.go`:
    - タスク開始・完了時の所要時間計測と `StatsTracker` への報告、キュー長追跡、ファイルトラック数登録。
  - `zig/dashboard.py`:
    - Rich TUI / ANSI によるリアルタイム進捗・所要時間ダッシュボード。
  - `zig/check_tag_consistency.py`:
    - DB (`raw.library_flac`) と FLAC タグの双方向整合性検査・一括修復治具。
  - `tests/test_blackwell_onnx.py`, `tests/test_tag_consistency.py`, `tests/test_dashboard_stats.py`, `orchestrator/dispatcher/stats_test.go`:
    - 自動単体テスト新設。
- **Verification**:
  - `go test -v ./...`: 全件 PASS
  - `pytest tests/ -v`: 全 28 テスト PASS
  - `proof-checker.exe -path orchestrator`: Verdict PASS (0 Errors)
  - Verifier Subagent: **Verdict: PASS**

# Walkthrough: ストレージ防護機能（Gatekeeper ディスク監視・中間JSON/キャッシュ自動GC・Tagger空き容量事前検証）

- **Summary**: ストレージ不足（Disk Full）による解析クラッシュ、中間 JSON / 一時キャッシュの肥大化、および FLAC タグ書き込み時の空き容量枯渇によるファイル破壊を防止するため、Go Gatekeeper によるリアルタイムディスク監視・自動スロットリング、中間 JSON / 一時キャッシュの自動ガベージコレクション (Queue GC)、および FLAC Tagger の事前容量検証を実装。
- **Changes**:
  - `orchestrator/sysinfo/sysinfo.go`:
    - Win32 API `GetDiskFreeSpaceExW` をラップした `GetDiskFreeSpace(dirPath string) (*DiskInfo, error)` を実装。
  - `orchestrator/dispatcher/dispatcher.go`:
    - `EvaluateGoNoGoPure`: RAM 判定の前にディスク空き容量（`availDisk < minAvailDisk`）を検査し、容量不足時は安全にスロットリング待機する純粋関数に拡張。
    - `EvaluateGoNoGo`: キュー領域 (`QueueDir`)、テンポラリ (`os.TempDir()`)、音源ディレクトリの最小空き容量を取得して事前判定。
    - `PurgeOrphanedQueueAndCacheFiles`: 起動時に 1時間以上経過した一時キャッシュ (`%TEMP%/flac_analyzer_cache`) および孤立 `.json` を自動パージ。
    - `cleanupQueueFiles`: タスク失敗時に中間 JSON ファイル群（Librosa, Essentia, Tensor）を自動削除。
  - `orchestrator/main.go`:
    - `config.toml` から `min_avail_disk_gb` (デフォルト: 5.0 GB) を読み込み、起動時に `PurgeOrphanedQueueAndCacheFiles` を実行。
  - `ingester.py`:
    - 正常コミット時および DLQ 退避時の両方で `args.predictions_json_path` (`*_essentia.json`) を確実に `os.remove`。
  - `flac_tagger.py`:
    - `config.toml` から `tagger_disk_margin_ratio` (デフォルト 1.5) を読み込み、ファイル書き込み前に `shutil.disk_usage` で対象ディレクトリの空き容量を検証。不足時は `OSError` で安全中断。
  - `config.toml` / `config.toml.example`:
    - `min_avail_disk_gb = 5.0`, `tagger_disk_margin_ratio = 1.5` を追加。
  - `tests/test_storage_defense.py`:
    - `test_flac_tagger_disk_space_defense`, `test_ingester_cleanup_all_json_files` を新規追加。
- **Verification**:
  - `cd orchestrator; go test -v ./dispatcher/...`: 全 18 テスト PASS
  - `pytest tests/ -v`: 全 21 テスト PASS
  - `proof-checker.exe -path "orchestrator"`: Verdict PASS
  - Verifier Subagent Review: **Verdict: PASS**

# Walkthrough: Gatekeeper 20秒リトライ・10分設定監視・DLQ自動再送・全治具 zig/ 集約 ＆ Docs最新化

- **Summary**: ユーザー要求（GO/NOGO 20秒リトライ制御、10分設定検知、全治具スクリプトの `zig/` 集約）および Issues #9, #10, #12, #13, #14 を解決。Gatekeeper の純粋射化 ＆ テスト整備、DLQ 起動時/定期自動再送スケジューラ、pytest カバレッジ計測設定、治具集約 (`zig/`) ＆ ドキュメント全面最新化を完了。
- **Changes**:
  - `orchestrator/dispatcher/dispatcher.go`:
    - `EvaluateGoNoGoPure`: 副作用のない純粋判定関数（EffectiveAvail 算出・アンダーフロー防御・90%負荷チェック）の実装。
    - `gatekeeper_retry_delay_sec` (デフォルト: 20秒), `enable_dlq_retry` (true), `dlq_retry_interval_sec` (600秒) の動的適用。
    - `StartDlqRetryScheduler`: 起動時即時および10分ごとの `zig/retry_ingest.py` バックグラウンド自動実行。
    - `runPythonScript`: `zig/` 配下のスクリプトパス自動解決。
  - `orchestrator/main.go`:
    - `config_watch_interval_sec` (600秒) 対応、DLQ 再送スケジューラ起動。
  - `config.toml` & `config.toml.example`:
    - 新設定キー追加。
  - `zig/`:
    - `repair_flac_tags.py`, `migrate_hnr.py`, `retry_ingest.py`, `fix_empty_meta.py`, `inspect_track.py`, `functor_precache.py`, `init_dl_model.py`, `update_hardware_specs.py`, `verify_track4.py` の全9治具を集約・UTF-8保護。
  - `pyproject.toml` & `requirements.txt`:
    - pytest カバレッジ計測設定 (`pytest-cov`) の追加。
  - `docs/` & `README.md` / `README_en.md`:
    - `docs/utility_tools.md` 新規作成。
    - `state_diagram.md`, `shm_architecture.md`, `dlq_error_recovery.md`, `cpu_parallelism_and_ram_guard.md` を最新化。
- **Verification**:
  - `cd orchestrator; go test -v ./...`: 100% PASS
  - `.venv\Scripts\python.exe -m pytest --cov`: 19 passed in 34.36s
  - `proof-checker.exe -path "orchestrator"`: Verdict PASS
  - Verifier Subagent Review: **Verdict: PASS**

# Walkthrough: flac_decode.py 範囲デコード堅牢化 (-F/--silent/communicate/リトライ)

- **Summary**: `worker_demucs.py` / `flac_decode.py` で発生していた `flac` CLI 呼び出し例外（`rc=1`）に対し、`-F` (`--decode-through-errors`)、`--silent`、`proc.communicate()`、指数バックオフリトライ（最大3回）を導入し、ストリームエラー耐性と堅牢性を向上。
- **Changes**:
  - `flac_decode.py`:
    - `decode_flac_range`: `-F`, `--silent`, `proc.communicate()`, 指数バックオフリトライ（最大3回）、詳細エラーコンテキスト付き `RuntimeError` を実装。
    - `process_slice_with_seq_safety`: 10分以上の長尺ストリーミングデコードにおける `-F`, `--silent`, `proc.wait()` 戻り値検証とエラー送出の実装。
  - `tests/test_flac_decode.py`:
    - `test_decode_flac_range_basic`: 正常系範囲デコードの検証。
    - `test_process_slice_with_seq_safety_basic`: スライス抽出・44.1kHzリサンプリング・MD5ハッシュ計算の検証。
    - `test_decode_flac_range_retry_and_error`: 存在しないファイルに対するリトライ動作および例外送出の検証。
- **Verification**:
  - 実FLAC（エラー対象曲: Dire Straits Track 5）デコード成功 (15,630,805 samples, Hash: `048daea8384f537545277230790e7237`)
  - `pytest tests`: 19/19 PASS (10.23s)
  - `go test -v ./...` (in `orchestrator`): PASS
  - Verifier Subagent Review: **Verdict: PASS**

# Walkthrough: run_batch.ps1 -Dir 引数バインド＆LiteralPath堅牢化

- **Summary**: `run_batch.ps1` において `-Dir` を指定した際にパラメータバインドされずデフォルト値で全件走査されてしまう問題を修正。`[CmdletBinding()]` の追加、エイリアスの拡充、位置引数・パイプライン引数の対応、および特殊文字（角括弧）パスに対応するための `-LiteralPath` 解決処理を実装。
- **Changes**:
  - `run_batch.ps1`:
    - `[CmdletBinding()]` を追加
    - `$MusicRoot` に `-Dir`, `-Directory`, `-MusicDir`, `-TargetDir`, `-Target`, `-FilePath`, `-DirPath` エイリアスおよび `Position=0`, `ValueFromPipeline=$true` を付与
    - `$Concurrency` に `-c`, `-Threads`, `-Parallel`, `-Jobs` エイリアスを付与
    - `Test-Path` / `Resolve-Path` で `-LiteralPath` 優先フォールバックを実装
- **Verification**:
  - `.\run_batch.ps1 -Dir testFLAC -DryRun`: 4件検知・キュー投下正常動作
  - `.\run_batch.ps1 -Directory testFLAC -DryRun`: 正常動作
  - `.\run_batch.ps1 testFLAC -DryRun`: 位置引数バインド正常動作
  - `.\run_batch.ps1 -File 'testFLAC\01_08_Reply.flac' -DryRun`: 単一ファイル指定正常動作
  - `.\run_batch.ps1 -File 'testFLAC\The Art Of Nikita Magaloff[Disc 02][Chopin][chamber, ..].flac' -DryRun`: 角括弧ファイル名正常動作
  - `.\run_batch.ps1 -Dir 'flac_special_[2026] #1 (test)' -DryRun`: 特殊文字ディレクトリ正常動作
  - Verifier Subagent Review: **Verdict PASS**

# Walkthrough: Prometheus :2112/metrics ボトルネック観測・可観測性強化

- **Summary**: パイプラインのクリティカルパスおよびリソース競合（ボトルネック）を精密に特定できるよう、Prometheus `:2112/metrics` のメトリクス群を大幅に強化・拡張し、`net/http/pprof` によるライブプロファイリングを統合。
- **Changes**:
  - `orchestrator/metrics/metrics.go`:
    - `_ "net/http/pprof"` インポートによる `/debug/pprof/` 自動公開。
    - `analyzer_stage_duration_seconds{stage}` (HistogramVec), `analyzer_last_stage_duration_seconds{stage}`, `analyzer_avg_stage_duration_seconds{stage}`。
    - `analyzer_demucs_wait_seconds` (Histogram/Gauge), `analyzer_tensor_wait_seconds` (Histogram/Gauge), `analyzer_gatekeeper_wait_seconds` (Histogram/Gauge), `analyzer_shm_alloc_duration_seconds` (Histogram)。
    - `analyzer_demucs_queue_waiters`, `analyzer_tensor_queue_waiters` (Gauge)。
    - `analyzer_python_stage_duration_seconds{component, step}`, `analyzer_python_last_stage_duration_seconds{component, step}`。
  - `orchestrator/dispatcher/stats.go` & `dispatcher.go`:
    - `StatsTracker` にステージ別 EMA（alpha=0.15）および待機時間記録メソッドを追加。各ステージ（hash_check, shm_alloc, demucs, librosa, tensor, essentia, flac_tagger, db_ingest）の前後にタイマーを配置し、Python JSON からの `profile` パースヘルパー `parseAndRecordPythonProfile` を実装。
  - Python ワーカー群 (`worker_demucs.py`, `worker_librosa.py`, `worker_tensor.py`, `worker_essentia.py`, `flac_tagger.py`, `ingester.py`):
    - `time.perf_counter()` によるサブステップ（デコード、推論、SHM書き込み、タグ保存、DB Upsert）の時間を測定し、JSON レスポンスの `profile` フィールドに格納して標準出力に出力。
  - `zig/dashboard.py`:
    - TUI 上に「ボトルネック・ステージ別所要時間（平均/直近）」および「リソース競合＆待機時間（Demucs待ち/Tensor待ち/Gatekeeper待ち）」の専用テーブルパネルを追加。
  - `tests/test_dashboard_stats.py` & `orchestrator/dispatcher/stats_test.go`:
    - 新設メトリクスの単体テストを追加。
  - `C:\Users\letwir\.gemini\CODE_RULE.md`, `method.md`, `knowledge.md`:
    - Go ETL 観測規約 (`etl_observability`)、pprof ライブプロファイリング手順、ボトルネック観測知見を記録・永続化。
- **Verification**:
  - `go test ./... -v` (in `orchestrator`): 20/20 PASS (100%)
  - `.\.venv\Scripts\python.exe -m pytest tests/ -v`: 28/28 PASS (100%)
  - `proof-checker.exe`: Verdict: PASS (0 Errors)
# Walkthrough: CUE範囲外デコードエラー & WorkerDaemonPool Thundering Herd 修正

- **Summary**: 1) マルチディスクCUEシート配下の範囲外トラックによる `FLAC__STREAM_DECODER_SEEK_ERROR` / `LibsndfileError` の根絶、2) `WorkerDaemonPool` のスロット事前予約（`spawningCount`）、起動時 `Prewarm`、および Step 5 での Acquire/Extract タイムアウト完全分離による Thundering Herd（多重起動競合）と `context deadline exceeded` タイムアウトの根絶。
- **Changes**:
  - `flac_decode.py`:
    - `parse_cue_text_to_slices` および `build_flac_handle` に `start >= total_samples` および `clamped_end <= start` の境界ガードを追加し、警告ログを出力して範囲外トラックを安全にスキップ。
    - `decode_flac_range` に `start >= end` の早期引数検証を追加。
    - `decode_flac_range_fallback` に `frames <= 0` / `actual_start >= total` のガードを追加。
  - `tests/test_flac_decode.py`:
    - `test_parse_cue_text_out_of_bounds_filtering` および `test_decode_flac_range_invalid_bounds` を新設。
  - `orchestrator/dispatcher/daemon_pool.go`:
    - `spawningCount int` によるスロット事前予約を導入し、同時多重起動を `maxDaemons` 以内に完全に抑止。
    - `spawningCount--` を RAII `defer` ブロック内で実行し、コンテキストキャンセル時にも確実にデクリメントされることを保証（ADV-1 準拠）。
    - `Prewarm(ctx context.Context, count int)` を実装。
  - `orchestrator/dispatcher/dispatcher.go`:
    - `daemonCap` を最大 8 基まで動的に拡大可能に改善。
    - `Dispatcher.Start()` 時にバックグラウンドで `daemonPool.Prewarm(2)` を実行。
    - Step 5 において、Acquire 用タイムアウト（120s）と Extract 用タイムアウト（90s）を完全に分離。
  - `orchestrator/dispatcher/daemon_test.go`:
    - `TestDaemonPoolThunderingHerd`（8 goroutine 同時 Acquire 並行ストレステスト）を新設。
- **Verification**:
  - `pytest tests/test_flac_decode.py`: 5/5 PASSED (3.33s)
  - `go test -v -timeout 180s ./dispatcher/...`: ALL PASSED (58.089s)
  - `TestDaemonPoolThunderingHerd`: PASS (13.11s, `totalSpawned <= 2 ∧ spawningCount == 0`)
  - `proof-checker.exe`: Verdict: PASS (0 Errors, 0 Warnings)
  - `go build -o orchestrator.exe .`: SUCCESS (Exit 0)
  - Verifier Gate (Claude Sonnet 4.6): **Verdict: PASS**
