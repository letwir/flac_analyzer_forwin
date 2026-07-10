# Go Orchestrator と DLQ 実装の完了報告

旦那様、ご要望の通り、解析パイプラインのアーキテクチャ刷新と、Postgres送信失敗時のDLQ（Dead Letter Queue）フォールバック機能を実装いたしましたわ！

## 実装内容

### 1. Go Orchestrator パッケージの分割・整理
`orchestrator/main.go` に集中していた処理を、再利用性と保守性の高いパッケージ構成へとリファクタリングいたしましたの。
*   **`dispatcher`**: ワーカープールの管理、メモリ容量に応じた共有メモリ（SHM）の動的割り当て、およびPythonスクリプト群の呼び出しとルーティングを担当します。
*   **`state`**: SQLite (`orchestrator.db`) を用いたタスク状態の管理（Pending, Running, Completed, Failed）を行います。WALモードを有効化し、並列処理時の排他制御をGo側で一元管理しています。
*   **`metrics`**: Prometheusクライアント（`/metrics` エンドポイント）を提供し、キューの長さやワーカー稼働状況などを監視できるようにいたしました。

### 2. DLQ (Dead Letter Queue) フォールバック機構
*   **`ingester.py` の改修**: PostgreSQLへのUPSERTで例外が発生した場合、強制終了せずに `send_failed.db` (SQLite) へペイロード（JSON形式のメタデータや特徴量）を退避させるようにしました。
*   **`retry_ingest.py` の新規作成**: 定期実行または手動で呼び出すことで、`send_failed.db` に溜まった未送信レコードをPostgresへ再送し、成功したものをDLQから削除するスクリプトをご用意いたしましたわ。

### 3. バッチスクリプトの単純化
*   **`run_batch.ps1` の改修**: ローカルの `flac.done` や複雑なスキップ判定ロジックをすべて廃止し、ディレクトリを走査して発見したFLACを無条件でGoの `/task` APIへPOSTするだけのシンプルな構造に変更いたしました。
*   スキップの判定自体は、Go側の `state` パッケージがSQLiteを参照して `[200 OK (Skipped)]` または `[202 Accepted]` を返すことで一元的に処理されますの。

## 確認方法

1.  **Orchestratorの起動**: `run_batch.ps1` を実行すると自動で起動しますが、手動でテストする場合は `orchestrator` ディレクトリ内で `./orchestrator.exe` を実行してくださいませ。
2.  **メトリクスの確認**: Orchestrator稼働中にブラウザで `http://127.0.0.1:2112/metrics` にアクセスすると、Prometheus形式のメトリクスをご確認いただけます。
3.  **DLQのテスト**: わざとPostgresを落とした状態で `run_batch.ps1 -Test` などを実行して `send_failed.db` にデータが保存されるか確認し、その後Postgresを復帰させてから `python retry_ingest.py` を実行してみてくださいませ。

不具合や、さらに追加したいメトリクス項目（例えば処理時間のヒストグラムなど）がございましたら、いつでもお申し付けくださいませ！
