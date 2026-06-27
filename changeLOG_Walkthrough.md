# Walkthrough: --no-db flag and local JSON output in Go

## What was implemented
1. `orchestrator/main.go` に `--no-db` フラグを実装しました。
2. Python の標準出力を `bytes.Buffer` に一時格納し、`--no-db` が有効な場合は `testFLAC/` フォルダ配下に JSON ファイルとして出力する処理を追加しました。

## Testing & Validation
- Go の構文レベルでのエラーがないことを確認いたしました。
- `--no-db` モードで Go のオーケストレーターを起動すると、Python プロセスからの解析結果（JSON）をデータベースへの保存（UPSERT）無しでローカルファイルに直接出力し、安全かつ純粋なテストが実行可能となります。