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

