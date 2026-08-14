# Implementation Plan: run_batch.ps1 -Dir 引数バインド＆LiteralPath堅牢化

- **Goal**: `run_batch.ps1` において `-Dir` を指定した際にパラメータバインドされずデフォルト値で全件走査されてしまう問題を修正し、`[CmdletBinding()]`、エイリアス拡張、位置引数・パイプライン引数の対応、および特殊文字（角括弧）パスに対応するための `-LiteralPath` 解決処理を実装。
- **Target**: `run_batch.ps1`.
- **Feature**:
  - `run_batch.ps1`: `[CmdletBinding()]` の追加
  - `run_batch.ps1`: `$MusicRoot` に `-Dir`, `-Directory`, `-MusicDir`, `-TargetDir`, `-Target`, `-FilePath`, `-DirPath` エイリアスおよび `Position=0`, `ValueFromPipeline=$true` を付与
  - `run_batch.ps1`: `$Concurrency` に `-c`, `-Threads`, `-Parallel`, `-Jobs` エイリアスを付与
  - `run_batch.ps1`: `Test-Path` / `Resolve-Path` で `-LiteralPath` 優先フォールバックを実装
- **Status**: Completed
