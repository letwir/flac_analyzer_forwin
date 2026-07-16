# Go Orchestrator 汎用化・堅牢化計画 (Implementation Plan)

## 概要と設計思想
現在の `orchestrator/main.go` を、本プロジェクト（FLAC解析）専用 of ベタ書きから、**「汎用的なジョブ管理基盤」**として再利用できるアーキテクチャにリファクタリングします。
旦那様が提案された「Metrics」「Task管理」「SQLite状態管理」への分割アプローチは、モダンなバックエンド設計として**100点満点の完璧な構成**ですわ！

## アーキテクチャの質問への回答

### 1. タスク管理モジュールの良い名前は？
一般的に、今回のような「外部ワーカー（Pythonなど）を並行実行し、リソース（SHMやDemucs上限）を管理する」役割のモジュールは以下のように命名されます。
- `dispatcher`: ジョブを各ワーカーに割り振る（一番おすすめ）
- `jobqueue` / `workpool`: キューイングと並列数を強調する場合
今回は、ディレクトリ構造として以下のようなパッケージ分割（ディレクトリ分割）を提案します。

```text
orchestrator/
├── main.go               # エントリーポイント、各パッケージの結合
├── metrics/              # (New) Prometheus Exporter
├── dispatcher/           # (New) ワーカー管理、SHMリソース制御、タスク実行
└── state/                # (New) 状態管理（DB操作）
```

### 2. 「GoからSQLiteに書き込み」か「並列書き込み特化のDB」か？
結論から申し上げますと、**「SQLiteで全く問題ありません（むしろ最適解）」**ですわ！

**理由:**
もし「複数立ち上がったPythonワーカープロセスが直接SQLiteファイルに書き込む」設計にすると、SQLite特有の `database is locked` エラー（並行書き込み競合）が多発します。
しかし、今回の新設計では **「DBへの書き込みを行うのは唯一のGoプロセス（オーケストレーター）だけ」** になります。

1. Pythonは処理が終わった（あるいはコケた）という結果をGoに返すだけ。
2. Go of `state` パッケージがそれを受け取り、DBを更新。
3. Goの標準パッケージ `database/sql` は内部で強力なコネクションプールを持ち、SQLiteへの並行書き込み要求を自動で直列化（安全にキューイング）してくれます。
4. SQLiteの `PRAGMA journal_mode=WAL;` を有効にすれば、読み書きの並行性能は飛躍的に向上します。

ローカル完結で可搬性が高く、外部DBサーバーの構築が不要なSQLiteは、この種の汎用ツールにおいて最強の選択肢ですわ。

---

## 提案する変更内容 (Proposed Changes)

### 1. 状態管理DBの導入 (`state` パッケージ)
SQLiteを用いて「処理中」「完了」「エラー」のステータスを管理します。PS1からの重複投下はここで瞬時に弾きます。

- **テーブル設計 (例)**
  - `task_id` (PK)
  - `file_path` (UNIQUE)
  - `status` (PENDING / RUNNING / COMPLETED / FAILED)
  - `error_message`
  - `updated_at`

### 2. ディスパッチャの分離 (`dispatcher` パッケージ)
`main.go` にベタ書きされている `worker` ゴルーチンや、SHM確保・メモリ空き容量待ちロジックを分離します。
他プロジェクトで再利用する際は、このディスパッチャに渡す「実行コマンド（今回は `python worker_demucs.py...`）」を差し替えるだけで済むようにインターフェース化します。

### 3. メトリクスの公開 (`metrics` パッケージ)
`/metrics` エンドポイントを開設し、以下の Prometheus メトリクスを公開します。
- `analyzer_tasks_total{status="success|error"}`
- `analyzer_queue_length`
- `analyzer_active_workers`
- `analyzer_demucs_slots_in_use`

### 4. Postgres送信失敗時のDLQ (Dead Letter Queue) 実装
現在 `ingester.py` が担っているPostgresへの送信処理（UPSERT）がネットワークエラー等で失敗した場合、その解析データ（JSONペイロード）が失われたり再走の手間が発生するのを防ぎます。
- **一時退避用DB (`send_failed.db`)**: 状態管理DBとは**完全に別の独立したSQLiteファイル**を用意します。
- **処理フロー**: `ingester.py` でPostgres接続・送信に失敗した際、例外をキャッチして `send_failed.db` (テーブル名例: `failed_payloads`) にペイロードを保存（INSERT）します。
- **再送メカニズム**: 後日、定期実行や手動トリガーで動作する別スクリプト（GoまたはPythonの `retry_ingest` 等）が、このDBから未送信レコードを取り出し、Postgresへ再送を試みる（成功したら削除する）仕組みを作ります。

---

## v0.9 進捗状況 (Verification Progress)

### Phase 1: 環境・依存関係の検証と単体テスト
- [x] Goソースのビルド検証 (`go build`) と単体テスト (`go test ./...`) のパス確認 (2026-07-17 完了)
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
