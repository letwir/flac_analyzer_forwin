# 状態遷移図 (State Diagram)

本ドキュメントは、Flac_Analyzer システムにおける Go オーケストレーターと Python ワーカープロセス群のタスク処理フローおよび状態遷移を示す図面ですわ。

## 日本語版 (Japanese Version)

```mermaid
stateDiagram-v2
    [*] --> StartupReset: オーケストレーター起動
    StartupReset --> Idle: orchestrator.db の RUNNING/PENDING タスクを FAILED へリセット
    
    Idle --> TaskReceived: /task APIへファイルパスがPOSTされる
    TaskReceived --> CueInspect: worker_cue.py 起動<br/>（CUE/タグ解析・トラック自動抽出。CUE不在時も自動フォールバック）
    CueInspect --> CheckState: orchestrator.db (SQLite) で各トラックの(file_path, track_number)確認
    
    CheckState --> Skipped: 全トラックが COMPLETED / RUNNING / PENDING (force:false 時)
    CheckState --> Queued: 未処理トラックを PENDING として登録
    
    Skipped --> [*]: レスポンス 200 OK (処理スキップ)
    Queued --> ResponseAccepted: レスポンス 202 Accepted (展開トラック数返却)
    ResponseAccepted --> Dispatcher_Loop
    
    state Dispatcher_Loop {
        CalcHash: worker_demucs.py --check-hash-only<br/>(トラック波形MD5算出)
        CalcHash --> CheckHashDB: ingester.py --check-hash<br/>(PostgreSQL重複照合)
        CheckHashDB --> SkippedByHash: PostgreSQLに同ハッシュが既に存在 (skip_dup_by_hash=true 時)
        CheckHashDB --> ResourceWait: 未登録楽曲
        
        ResourceWait --> AllocatingSHM: メモリ空き容量・並列上限セマフォ監視
        AllocatingSHM --> DemucsProcessing: worker_demucs.py 起動<br/>（波形スライスデコード・分離・SHM書き込み）
        DemucsProcessing --> FreezingSHM: Go側で共有メモリを PAGE_READONLY 化
        FreezingSHM --> Precache: functor_precache.py 起動<br/>（SHM read-only アタッチ・メタデータ整合性検証）
        Precache --> ParallelFeatureExtracting: ポストDemucs並列特徴量抽出起動<br/>（Librosa, Tensor, Essentia 3本同時並列実行）
        ParallelFeatureExtracting --> ReleaseSHM: Go側で共有メモリ (SHM) 解放
        ReleaseSHM --> WriteJSONFiles: 中間JSONファイル書き込み<br/>(queue/ ディレクトリへ一時出力)
        WriteJSONFiles --> Ingesting: ingester.py 起動（JSON集約・DB照合）
    }
    
    SkippedByHash --> TaskCompleted: スキップ完了 (status: COMPLETED)
    Ingesting --> PostgreSQL_Upsert: DB正常時 (PostgreSQLへUPSERT)
    Ingesting --> DLQ_Fallback: DB接続不可時 (send_failed.dbへ退避)
    
    PostgreSQL_Upsert --> TagWriteback: FLACタグ書き戻し &<br/>Windows タイムスタンプ保護 (SetFileTime)
    TagWriteback --> IngesterCleanup: ingester.py による中間JSON・キャッシュ削除
    DLQ_Fallback --> IngesterCleanup: 退避後に中間JSON・キャッシュ削除
    
    IngesterCleanup --> TaskCompleted: Go defer クリーンアップ実行後<br/>orchestrator.db の status を COMPLETED に更新
    TaskCompleted --> [*]
```

## English Version

```mermaid
stateDiagram-v2
    [*] --> StartupReset: Orchestrator Startup
    StartupReset --> Idle: Reset stale RUNNING/PENDING tasks in orchestrator.db to FAILED
    
    Idle --> TaskReceived: File path POSTed to /task API
    TaskReceived --> CueInspect: Execute worker_cue.py<br/>(Parse CUE/tags & extract tracks; graceful single-track fallback if no CUE)
    CueInspect --> CheckState: Check orchestrator.db (SQLite) for each (file_path, track_number)
    
    CheckState --> Skipped: All tracks already COMPLETED / RUNNING / PENDING (when force:false)
    CheckState --> Queued: Unprocessed tracks registered as PENDING
    
    Skipped --> [*]: 200 OK (Skipped)
    Queued --> ResponseAccepted: 202 Accepted (Enqueued tracks count)
    ResponseAccepted --> Dispatcher_Loop
    
    state Dispatcher_Loop {
        CalcHash: worker_demucs.py --check-hash-only<br/>(Calculate track waveform MD5)
        CalcHash --> CheckHashDB: ingester.py --check-hash<br/>(Check duplication in PostgreSQL)
        CheckHashDB --> SkippedByHash: Hash already exists in PostgreSQL (when skip_dup_by_hash=true)
        CheckHashDB --> ResourceWait: New track
        
        ResourceWait --> AllocatingSHM: Monitor RAM & concurrency limit semaphore
        AllocatingSHM --> DemucsProcessing: Execute worker_demucs.py<br/>(Slice decode/Separate/SHM Write)
        DemucsProcessing --> FreezingSHM: Go freezes SHM to PAGE_READONLY
        FreezingSHM --> Precache: Execute functor_precache.py<br/>(Validate SHM read-only attach & metadata)
        Precache --> FeatureExtracting: Execute extraction workers<br/>(Librosa → Tensor → Essentia)
        FeatureExtracting --> ReleaseSHM: Go closes & unmaps SHM handles
        ReleaseSHM --> WriteJSONFiles: Write intermediate JSON files<br/>(Save temporarily to queue/ directory)
        WriteJSONFiles --> Ingesting: Execute ingester.py (Aggregate JSON & DB sync)
    }
    
    SkippedByHash --> TaskCompleted: Mark completed (status: COMPLETED)
    Ingesting --> PostgreSQL_Upsert: DB available (PostgreSQL UPSERT)
    Ingesting --> DLQ_Fallback: DB unreachable (Save to send_failed.db)
    
    PostgreSQL_Upsert --> TagWriteback: Writeback FLAC tags &<br/>Protect Windows timestamp (SetFileTime)
    TagWriteback --> IngesterCleanup: ingester.py purges temp JSON & cache files
    DLQ_Fallback --> IngesterCleanup: Purge temp files after DLQ fallback
    
    IngesterCleanup --> TaskCompleted: Go defer cleanup & update status to COMPLETED in orchestrator.db
    TaskCompleted --> [*]
```
