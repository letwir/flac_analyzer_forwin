# Implementation Plan: 音響解析パイプラインの高速化 & 圏論的リファクタリング (Phase 1〜3)

- **Goal**: 命題1〜3（1: 分析精度の完全維持、2: Go OS管理 + Python GPU Tensor、3: 9年長期安定言語）を厳格に遵守し、事前リリース（v1.3.1）から Phase 1（Go直接DB Ingest & 常駐ワーカー化）、Phase 2（PyTorch GPU Tensor DSP & 2Nゼロパディング cuFFT HNR/NAP）、Phase 3（回帰テスト & CI Gate）を完遂する。
- **Target**: `orchestrator/dispatcher/ingest_pgx.go`, `orchestrator/dispatcher/dispatcher.go`, `worker_daemon.py`, `analyzer/tensor_dsp.py`, `analyzer/librosa_dsp.py`, `tests/test_gpu_dsp_equivalence.py`, `tests/test_worker_daemon.py`.
- **Feature**:
  - `gh release create v1.3.1`: ベースラインリリース作成。
  - `ingest_pgx.go` [NEW]: Go 内製 PostgreSQL Direct UPSERT および SQLite DLQ (`send_failed.db`) フォールバック。
  - `dispatcher.go`: `ingester.py` サブプロセス起動と中間 JSON ファイル生成の撤廃。
  - `worker_daemon.py` [NEW]: 常駐型ワーカーデーモン (NDJSON IPC / Advisory 2 遵守 `try...finally: shm.close()`)。
  - `analyzer/tensor_dsp.py`: $2N$ ゼロパディング Wiener-Khinchin cuFFT HNR/NAP (Advisory 1)、7ステム一括バッチ STFT、Spectral Centroid/Rolloff/Flatness/ZCR/Key 推定の GPU テンソル純粋射。
  - `analyzer/librosa_dsp.py`: `_calc_hnr_nap` の `tensor_dsp` 委譲。
  - `tests/test_gpu_dsp_equivalence.py` [NEW]: 数学的精度等価性回帰テストスイート。
  - `tests/test_worker_daemon.py` [NEW]: ワーカーデーモン単体テスト。
- **Status**: Completed

# Implementation Plan: 計測器 (analyzer/*) の圏論的完全分離および分岐器・射 (worker_*.py) 再配置と重複ファイル一掃

- **Goal**: 音響特徴量の数理計算・DSP演算（計測器）をすべて `analyzer/*` パッケージに完全分離・局所化し、各ワーカー（`worker_tensor.py`, `worker_essentia.py` 等）をオーケストレーターとの入出力を媒介する純粋な分岐器・射へと純化し、ルートの重複治具フォワーダー群（7ファイル）と旧 `load_wave.py` を一掃する。
- **Target**: `analyzer/tensor_dsp.py`, `analyzer/types.py`, `analyzer/essentia_dsp.py`, `analyzer/__init__.py`, `worker_tensor.py`, `worker_essentia.py`, `models.py`, `pipeline.py`, `tests/test_tensor_dsp.py`.
- **Feature**:
  - `analyzer/tensor_dsp.py` [NEW]: `hilbert_envelope_phase`, `welch_psd`, `fft_bandpass_envelope`, `extract_tensor_features`, `extract_tensor_obj`, `tensor_extractor` の実装。
  - `analyzer/types.py`: `TensorFeatures` データクラス新設。
  - `analyzer/essentia_dsp.py`: `extract_mel_patches`, `run_essentia_serialized` 集約。
  - `worker_tensor.py` & `worker_essentia.py`: DSP ロジックを排除し `analyzer` 呼び出しの純粋射化。
  - `models.py`: 計測ロジックを `analyzer.essentia_dsp` へ委譲。
  - ルートの不要ファイル群（`fix_empty_meta.py`, `init_dl_model.py`, `inspect_track.py`, `migrate_hnr.py`, `retry_ingest.py`, `update_hardware_specs.py`, `verify_track4.py`, `load_wave.py`）の削除。
  - `tests/test_tensor_dsp.py` [NEW]: PyTorch Tensor DSP 単体テスト。
- **Status**: Completed

# Implementation Plan: 残存 Issues (#7, #15, #16) の完全解決と Prometheus /metrics への所要時間・進捗メトリクス統合

- **Goal**: 未解決であった残存 Issues（#7 Blackwell GPU 動作検証、#15 DB ⇔ FLAC タグ双方向整合性チェッカー、#16 リアルタイム CLI 進捗ダッシュボード）をすべて解決し、1ファイルあたりおよび1曲（トラック）あたりの所要時間計測を Prometheus `:2112/metrics` に集約・可視化する。
- **Target**: `orchestrator/metrics/metrics.go`, `orchestrator/dispatcher/stats.go`, `orchestrator/dispatcher/dispatcher.go`, `orchestrator/main.go`, `zig/dashboard.py`, `zig/check_tag_consistency.py`, `tests/test_blackwell_onnx.py`, `tests/test_tag_consistency.py`, `tests/test_dashboard_stats.py`.
- **Feature**:
  - `metrics.go`: 1ファイル/1曲所要時間 (Histogram/Gauge)、スループット (Gauge)、ETA (Gauge)、リソース残量 (Gauge) の Prometheus メトリクス追加。
  - `stats.go`: `StatsTracker` による EMA 所要時間平滑化、60秒ウィンドウによる分あたりスループット算出、キュー残量による ETA 算出、RAM/Disk 定期サンプラー。
  - `dispatcher.go` & `main.go`: タスク/ファイル完了時の所要時間計測・報告、キュー長追跡の統合。
  - `zig/dashboard.py`: Rich TUI / ANSI によるリアルタイム進捗・所要時間ダッシュボード。
  - `zig/check_tag_consistency.py`: DB (`raw.library_flac`) と FLAC タグの双方向整合性検査・一括修復治具。
  - `tests/test_blackwell_onnx.py`: Blackwell GPU / ONNX Runtime / PyTorch 動作検証テスト。
- **Status**: Completed

# Implementation Plan: ストレージ防護機能（Gatekeeper ディスク監視・中間JSON/キャッシュ自動GC・Tagger空き容量事前検証）

- **Goal**: ストレージ不足（Disk Full）による解析クラッシュ、中間 JSON / 一時キャッシュの肥大化、および FLAC タグ書き込み時の空き容量枯渇によるファイル破壊を防止するため、Go Gatekeeper によるリアルタイムディスク監視・自動スロットリング、中間 JSON / 一時キャッシュの自動ガベージコレクション (Queue GC)、および FLAC Tagger の事前容量検証を実装。
- **Target**: `orchestrator/sysinfo/sysinfo.go`, `orchestrator/dispatcher/dispatcher.go`, `orchestrator/main.go`, `ingester.py`, `flac_tagger.py`, `config.toml`, `config.toml.example`, `tests/test_storage_defense.py`.
- **Feature**:
  - `sysinfo.go`: Win32 API `GetDiskFreeSpaceExW` ラッパー `GetDiskFreeSpace` の実装。
  - `dispatcher.go`: `EvaluateGoNoGoPure` にディスク空き容量判定を追加。`PurgeOrphanedQueueAndCacheFiles` および `cleanupQueueFiles` の実装。
  - `main.go`: `min_avail_disk_gb` 読み込みと起動時 GC 実行。
  - `ingester.py`: `predictions_json_path` (`*_essentia.json`) の削除漏れ修正。
  - `flac_tagger.py`: `tagger_disk_margin_ratio` による `shutil.disk_usage` 事前容量チェックと安全中断。
  - `tests/test_storage_defense.py`: ストレージ防護の単体テスト新設。
- **Status**: Completed

# Implementation Plan: flac_decode.py 範囲デコード堅牢化 (-F/--silent/communicate/リトライ)

- **Goal**: `worker_demucs.py` / `flac_decode.py` で発生していた `flac` CLI 呼び出し例外（`rc=1`）に対し、`-F` (`--decode-through-errors`)、`--silent`、`proc.communicate()`、指数バックオフリトライ（最大3回）を導入し、ストリームエラー耐性と堅牢性を向上させる。
- **Target**: `flac_decode.py`, `tests/test_flac_decode.py`.
- **Feature**:
  - `flac_decode.py`: `decode_flac_range` に `-F`, `--silent`, `proc.communicate()`, 指数バックオフリトライ、詳細エラーコンテキスト付き `RuntimeError` を実装。
  - `flac_decode.py`: `process_slice_with_seq_safety` の長尺ストリーミングデコードに `-F`, `--silent`, `proc.wait()` 戻り値検証を実装。
  - `tests/test_flac_decode.py`: 単体テスト（正常系スライスデコード・ハッシュ計算・異常系リトライ＆エラーハンドリング）を新設。
- **Status**: Completed

# Implementation Plan: run_batch.ps1 -Dir 引数バインド＆LiteralPath堅牢化

- **Goal**: `run_batch.ps1` において `-Dir` を指定した際にパラメータバインドされずデフォルト値で全件走査されてしまう問題を修正し、`[CmdletBinding()]`、エイリアス拡張、位置引数・パイプライン引数の対応、および特殊文字（角括弧）パスに対応するための `-LiteralPath` 解決処理を実装。
- **Target**: `run_batch.ps1`.
- **Feature**:
  - `run_batch.ps1`: `[CmdletBinding()]` の追加
  - `run_batch.ps1`: `$MusicRoot` に `-Dir`, `-Directory`, `-MusicDir`, `-TargetDir`, `-Target`, `-FilePath`, `-DirPath` エイリアスおよび `Position=0`, `ValueFromPipeline=$true` を付与
  - `run_batch.ps1`: `$Concurrency` に `-c`, `-Threads`, `-Parallel`, `-Jobs` エイリアスを付与
  - `run_batch.ps1`: `Test-Path` / `Resolve-Path` で `-LiteralPath` 優先フォールバックを実装
- **Status**: Completed

# Implementation Plan: Prometheus :2112/metrics ボトルネック観測・可観測性強化

- **Goal**: パイプラインのクリティカルパスおよびリソース競合（ボトルネック）を精密に特定できるよう、Prometheus `:2112/metrics` のメトリクス群を大幅に強化・拡張し、`net/http/pprof` によるライブプロファイリングを統合する。
- **Target**: `orchestrator/metrics/metrics.go`, `orchestrator/dispatcher/stats.go`, `orchestrator/dispatcher/dispatcher.go`, `worker_demucs.py`, `worker_librosa.py`, `worker_tensor.py`, `worker_essentia.py`, `flac_tagger.py`, `ingester.py`, `zig/dashboard.py`, `tests/test_dashboard_stats.py`, `orchestrator/dispatcher/stats_test.go`.
- **Feature**:
  - `metrics.go`: ステージ別レイテンシ分解 (`analyzer_stage_duration_seconds{stage}`), リソース競合・待機時間 (`analyzer_demucs_wait_seconds`, `analyzer_tensor_wait_seconds`, `analyzer_gatekeeper_wait_seconds`), セマフォ待ちワーカー数 (`analyzer_demucs_queue_waiters`, `analyzer_tensor_queue_waiters`), Python サブステップ内部プロファイル (`analyzer_python_stage_duration_seconds{component, step}`), `_ "net/http/pprof"` 有効化。
  - `stats.go` & `dispatcher.go`: `StatsTracker` にステージ別 EMA / 待機時間 / Python プロファイル記録メソッドを新設。パイプライン各工程の精密タイマーと JSON `profile` パースヘルパー `parseAndRecordPythonProfile` を実装。
  - Python ワーカー群: `time.perf_counter()` によるサブステップ時間計測と JSON `profile` 出力対応。
  - `zig/dashboard.py`: TUI 上での「ステージ別所要時間」および「リソース競合＆待機時間」のリアルタイム可視化テーブル新設。
- **Status**: Completed


