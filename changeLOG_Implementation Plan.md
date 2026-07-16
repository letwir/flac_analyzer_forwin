# Go Orchestrator 汎用化・堅牢化計画 (Implementation Plan)

## 概要と設計思想
現在の `orchestrator/main.go` を、本プロジェクト（FLAC解析）専用 of ベタ書きから、**「汎用的なジョブ管理基盤」**として再利用できるアーキテクチャにリファクタリングします。
現在の進捗として、主要モジュールの分離と動作検証が完了しました。

## アーキテクチャの構成

```text
orchestrator/
├── main.go               # エントリーポイント、各パッケージ of 結合
├── metrics/              # Prometheus Exporter
├── dispatcher/           # ワーカー管理、SHMリソース制御、タスク実行
└── state/                # 状態管理（DB操作）
```

---

## 提案された変更内容

### 1. 状態管理DBの導入 (`state` パッケージ)
- **CGOフリー化**: GCC不要で動作する `modernc.org/sqlite` に移行し、Windows上でのビルド・実行互換性を 100% 確保しました。
- SQLite を用いて「処理中」「完了」「エラー」のステータスを管理します。

### 2. ディスパッチャの分離 (`dispatcher` パッケージ)
- ワーカー管理、SHMリソース制御、およびPythonスクリプト群の呼び出しとルーティングを分離しました。
- Python環境を `.venv` から優先的にロードするパス解決機構を導入し、依存パッケージのインポートエラーを解消しました。

### 3. メトリクスの公開 (`metrics` パッケージ)
- `/metrics` エンドポイントを開設し、Prometheus メトリクスを公開します。

### 4. Postgres送信失敗時のDLQ (Dead Letter Queue) 実装
- PostgreSQLへの接続・UPSERT失敗時に、例外をキャッチしてローカルの `send_failed.db` へペイロードを退避する機構を実装・検証しました。

---

## v0.9 進捗状況 (Verification Progress)

### Phase 1: 環境・依存関係の検証と単体テスト
- [x] Goソースのビルド検証 (`go build`) と単体テスト (`go test ./...`) のパス確認 (2026-07-17 完了)
- [x] Python仮想環境 (`.venv`) の依存パッケージ（`prometheus-client`, `sqlite3` 等）のインストール状況確認（※GoがPrometheus Exporter内蔵、Pythonは標準sqlite3使用のため追加不要）

### Phase 2: SQLite タスク状態管理 (`state` パッケージ) の機能検証
- [x] `/task` エンドポイントへのタスク投下による SQLite (`orchestrator.db`) への状態書き込み（PENDING -> RUNNING -> COMPLETED/FAILED） of 確認
- [x] 重複したタスクを投げた際に `CheckOrInsert` で正しく重複が弾かれ、`Skipped` (200 OK) になることの確認

### Phase 3: 共有メモリ（SHM）と Python ワーカーの連携・OOM 回避の検証
- [x] Windows環境における共有メモリ確保、`worker_demucs.py` の書き込み、`Freeze()`（PAGE_READONLY化）がエラーなく動作することの確認
- [x] 後続ワーカー（Librosa, Tensor, Essentia）が共有メモリを Read-Only で正しくアタッチし、並列動作することの確認

### Phase 4: DLQ (Dead Letter Queue) と再送スクリプトの動作検証
- [x] Postgres を一時停止させた状態で `ingester.py` を走らせ、`send_failed.db` にペイロードが正しく退避されることの確認
- [x] Postgres 復帰後に `retry_ingest.py` を実行し、データが Postgres に無事 UPSERT され、DLQ（SQLite）から削除されることの確認

### Phase 5: メトリクス監視と全体バッチ統合検証
- [x] `:2112/metrics` 経由での Prometheus メトリクスが出力されることの確認（キューの長さやアクティブワーカー数）
- [x] `run_batch.ps1` を用いた複数フォルダ・ファイルに対するエンドツーエンド of 自動実行検証
