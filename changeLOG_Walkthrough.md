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

