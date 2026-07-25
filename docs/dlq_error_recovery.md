# DLQ (Dead Letter Queue) パターン＆エラーリカバリフロー

本ドキュメントは、`ingester.py` および `retry_ingest.py` による PostgreSQL 永続化・DLQ フォールバック・再送処理の全体フロー、ならびにオーケストレーター起動時のゾンビタスク自動リセットの仕組みを解説します。

---

## 1. 全体フロー図

```mermaid
flowchart TD
    subgraph Ingester ["ingester.py"]
        Start["ingester.py 起動<br/>(Go オーケストレーターから呼出)"] --> LoadJSON["中間 JSON 読込<br/>(librosa / tensor / predictions)"]
        LoadJSON --> MergeFeatures["Tensor 特徴量を<br/>Librosa features にマージ"]
        MergeFeatures --> ReadTags["FLAC タグ再読込<br/>(mutagen.FLAC)<br/>→ meta JSONB 構築"]
        ReadTags --> TryPG{"PostgreSQL 接続<br/>＆ UPSERT 試行"}

        TryPG -- "成功" --> PG_OK["UPSERT 完了<br/>(ON CONFLICT → UPDATE)"]
        PG_OK --> Cleanup_OK["中間 JSON 削除<br/>キャッシュディレクトリ削除<br/>exit(0)"]

        TryPG -- "例外発生<br/>(接続不可 / タイムアウト等)" --> DLQ_Write["DLQ フォールバック<br/>send_failed.db (SQLite) へ<br/>INSERT OR REPLACE"]
        DLQ_Write --> Cleanup_DLQ["中間 JSON 削除<br/>キャッシュディレクトリ削除<br/>exit(2)"]
    end

    subgraph Orchestrator ["Go オーケストレーター"]
        Exit0["exit(0) 受信"] --> MarkComplete["orchestrator.db:<br/>status → COMPLETED"]
        Exit2["exit(2) 受信"] --> MarkCompleteDLQ["orchestrator.db:<br/>status → COMPLETED<br/>(DLQ 退避済み)"]
        Exit1["exit(1) 受信"] --> MarkFailed["orchestrator.db:<br/>status → FAILED"]
    end

    Cleanup_OK --> Exit0
    Cleanup_DLQ --> Exit2
```

### 終了コードの意味

| Exit Code | 意味 | オーケストレーター側の処理 |
|:---:|:---|:---|
| `0` | PostgreSQL UPSERT 成功 | タスクを `COMPLETED` に更新 |
| `2` | DB 障害 → DLQ 退避成功 | タスクを `COMPLETED` に更新（データは DLQ に安全保管） |
| `1` | 致命的エラー（DLQ 書込すら失敗） | タスクを `FAILED` に更新 |

---

## 2. DLQ 再送フロー (`retry_ingest.py`)

```mermaid
flowchart TD
    RetryStart["retry_ingest.py 起動"] --> CheckDB{"send_failed.db<br/>が存在？"}
    CheckDB -- "No" --> NothingToDo["Nothing to retry.<br/>exit(0)"]
    CheckDB -- "Yes" --> CheckTable{"failed_payloads<br/>テーブルが存在？"}
    CheckTable -- "No" --> NothingToDo
    CheckTable -- "Yes" --> FetchRows["全レコード取得<br/>(SELECT *)"]
    FetchRows --> CheckEmpty{"レコード数 = 0？"}
    CheckEmpty -- "Yes" --> NothingToDo
    CheckEmpty -- "No" --> ConnectPG["PostgreSQL 接続"]
    ConnectPG --> LoopStart["各レコードをループ処理"]

    LoopStart --> TryUpsert{"PostgreSQL<br/>UPSERT 試行"}
    TryUpsert -- "成功" --> DeleteDLQ["send_failed.db から<br/>該当レコード DELETE"]
    DeleteDLQ --> IncrSuccess["success_count++"]
    IncrSuccess --> NextRow{"次のレコード<br/>あり？"}

    TryUpsert -- "失敗" --> Rollback["PostgreSQL ROLLBACK"]
    Rollback --> IncrFail["fail_count++"]
    IncrFail --> NextRow

    NextRow -- "Yes" --> LoopStart
    NextRow -- "No" --> Summary["結果サマリー出力<br/>Success: N, Failed: M"]
```

### 再送の設計ポイント

- **レコード単位のトランザクション**: 1レコードの UPSERT 失敗が他のレコードに影響しないよう、各レコードを独立してコミット/ロールバック
- **冪等性**: `ON CONFLICT (audio_hash) DO UPDATE` により、何度再送しても安全
- **成功したレコードは即時 DLQ から DELETE**: 再送ループ中に `send_failed.db` から削除されるため、中断しても再実行時に二重送信されない

---

## 3. ゾンビタスク自動リセット

オーケストレーター起動時に、前回クラッシュ等でステータスが中途半端な状態で残ったタスクを自動検知・リセットします。

```mermaid
sequenceDiagram
    participant O as Go Orchestrator
    participant DB as orchestrator.db (SQLite)

    Note over O: プロセス起動
    O->>DB: SELECT * FROM task_state<br/>WHERE status IN ('RUNNING', 'PENDING')
    DB-->>O: 残存タスク一覧

    alt 残存タスクあり
        O->>DB: UPDATE task_state<br/>SET status = 'FAILED'<br/>WHERE status IN ('RUNNING', 'PENDING')
        Note over O: ゾンビタスクを FAILED にリセット<br/>→ 再投入可能状態に復帰
    else 残存タスクなし
        Note over O: クリーンスタート
    end

    O->>O: ワーカーディスパッチャ起動<br/>→ 通常運転開始
```

---

## 4. データストア役割分担

```mermaid
flowchart LR
    subgraph SQLite_Orch ["orchestrator.db (SQLite)"]
        TS["task_state テーブル<br/>─────────────<br/>file_path (PK)<br/>status<br/>error_message<br/>updated_at"]
    end

    subgraph SQLite_DLQ ["send_failed.db (SQLite)"]
        FP["failed_payloads テーブル<br/>─────────────<br/>audio_hash (PK)<br/>filepath, filename<br/>meta, features, predictions<br/>failed_at"]
    end

    subgraph PostgreSQL ["PostgreSQL"]
        LF["raw.library_flac<br/>─────────────<br/>本番データ永続化"]
        LFH["raw.library_flac_history<br/>─────────────<br/>更新前スナップショット"]
    end

    TS -- "タスク進捗管理<br/>(PENDING→RUNNING→COMPLETED/FAILED)" --> TS
    FP -- "DB障害時の<br/>一時退避バッファ" --> FP
    FP -. "retry_ingest.py<br/>による再送" .-> LF
    LF -- "BEFORE UPDATE<br/>トリガー" --> LFH
```

| データストア | 目的 | ライフサイクル |
|:---|:---|:---|
| `orchestrator.db` | タスク状態管理（重複防止・進捗追跡） | オーケストレーター常時使用 |
| `send_failed.db` | DB 障害時の解析結果一時退避 | 正常時は空、障害時に蓄積→再送で消化 |
| PostgreSQL | 解析結果の永続化（本番データ） | 最終到達地点 |
