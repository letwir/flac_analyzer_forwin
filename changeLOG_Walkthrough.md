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
