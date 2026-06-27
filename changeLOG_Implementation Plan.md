# Implementation Plan: --no-db flag and local JSON output in Go

## Goal
テスト時の PostgreSQL UPSERT を無効化する `--no-db` フラグと、ローカルJSONファイル出力による検証機能の実装

## Background
Pythonプロセスの純粋性を保つため、PostgreSQL依存を排除し JSON Lines を標準出力から返すようにリファクタリングが完了しました。これを Go オーケストレーター側で受け取り、DB に依存せずローカルでテスト検証できるようにします。

## Changes
- `orchestrator/main.go`
  - `flag` パッケージを用いて `--no-db` コマンドライン引数をパースします。
  - Python プロセスの標準出力を `bytes.Buffer` に捕捉します。
  - `--no-db` フラグが有効な場合、捕捉した出力（JSON）を `testFLAC/<flac_filename>.json` に書き出します。
  - `--no-db` が無効な場合は、現状は「未実装」としてログ出力のみを行います（将来的に UPSERT 処理を実装します）。

## Verification
- コマンドライン引数 `--no-db` を与えて起動し、POST されたタスクが完了した際、`testFLAC` フォルダ配下に `.json` ファイルが生成されていることを確認します。
