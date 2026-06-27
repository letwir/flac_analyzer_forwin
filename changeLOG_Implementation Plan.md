# Implementation Plan - 単一 flac.done 追記方式への移行

並列実行に伴う RAM OOM (Out Of Memory) や SharedMemory リークによるシステムリソース枯渇を根本解決するため、PowerShell が対象 FLAC ファイルパスを再帰的に列挙して一次保存（配列保持）し、同期呼び出しで `python main.py <flacfullpath>` を 1 ファイルずつ実行する構造へ移行しますわ。

また、スキップ判定の効率化のため、プロジェクトルートに `flac.done` を1つ作成し、成功したFLACファイルの絶対パスを改行区切りで追記していきますの。

## Proposed Changes

- **run_batch.ps1**: `Get-ChildItem` ですべての `.flac` ファイルを再帰列挙して一次配列に格納し、1つずつ python main.py を呼び出す。起動時に `flac.done` をロードし、`HashSet` を構築して高速スキップ判定。初回起動時には過去のログファイルから完了履歴を `flac.done` に自動的にマイグレーション。
- **main.py**: 変更は維持。
- **pipeline.py**: `process_single_flac_file_directly` にて成功時に `flac.done` に絶対パスを追記する処理を追加。
