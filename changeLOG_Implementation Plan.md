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

