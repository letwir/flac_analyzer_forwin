# ISSUE

- [x] 【Go】テスト時の PostgreSQL UPSERT を無効化する `--no-db` フラグと、ローカルJSONファイル出力による検証機能の実装

## v0.9 中期目標：Go Orchestrator & DLQ 安定化と動作検証

### Phase 1: 環境・依存関係の検証と単体テスト
- [ ] Python仮想環境 (`.venv`) の依存パッケージ（`prometheus-client`, `sqlite3` 等）のインストール状況確認

### Phase 2: SQLite タスク状態管理 (`state` パッケージ) の機能検証
- [ ] `/task` エンドポイントへのタスク投下による SQLite (`orchestrator.db`) への状態書き込み（PENDING -> RUNNING -> COMPLETED/FAILED）の確認
- [ ] 重複したタスクを投げた際に `CheckOrInsert` で正しく重複が弾かれ、`Skipped` (200 OK) になることの確認

### Phase 3: 共有メモリ（SHM）と Python ワーカーの連携・OOM 回避の検証
- [ ] Windows環境における共有メモリ確保、`worker_demucs.py` の書き込み、`Freeze()`（PAGE_READONLY化）がエラーなく動作することの確認
- [ ] 後続ワーカー（Librosa, Tensor, Essentia）が共有メモリを Read-Only で正しくアタッチし、並列動作することの確認

### Phase 4: DLQ (Dead Letter Queue) と再送スクリプトの動作検証
- [ ] Postgres を一時停止させた状態で `ingester.py` を走らせ、`send_failed.db` にペイロードが正しく退避されることの確認
- [ ] Postgres 復帰後に `retry_ingest.py` を実行し、データが Postgres に無事 UPSERT され、DLQ（SQLite）から削除されることの確認

### Phase 5: メトリクス監視と全体バッチ統合検証
- [ ] `:2112/metrics` 経由での Prometheus メトリクスが出力されることの確認（キューの長さやアクティブワーカー数）
- [ ] `run_batch.ps1` を用いた複数フォルダ・ファイルに対するエンドツーエンド of 自動実行検証
