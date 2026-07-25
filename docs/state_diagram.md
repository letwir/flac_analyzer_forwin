# 状態遷移図 (State Diagram) & 圏論的構造 (Categorical Structure)

本ドキュメントは、Flac_Analyzer システムにおける Go オーケストレーターと Python ワーカープロセス群のタスク処理フローおよび状態遷移を示す図面ですわ。
圏論 (Category Theory) における対象 (Objects)・射 (Morphisms)・関手 (Functors)・モナド (Monads) の考え方に基づき 6 つの Subcategory に分類し、カラーパレットを定義しておりますの。

---

## 圏論的 Subcategory & カラー定義 (Categorical Subcategories)

- **Phase 1: Initialization Functor \(\mathcal{C}_{\text{init}}\)** (灰色 `#334155` / `#64748b`)
  - 起動・設定読込・SQLiteタスク状態リセット・CUE解析
- **Phase 2: Duplication & Identity Isomorphism \(f_{\text{hash}}\)** (暗い青 `#1e3a8a` / `#2563eb`)
  - Waveform MD5算出・PostgreSQL重複照合・Skipped同型判定
- **Phase 3: Monadic Heavy Resource Functor \(\mathcal{T}_{\text{demucs}}\)** (深みのあるグリーン `#065f46` / `#059669`)
  - RAM/SHMセマフォ確保・Demucs音源分離・PAGE_READONLY凍結
- **Phase 4: Parallel Product Morphisms \(F_1 \times F_2 \times F_3\)** (明るいゴールド・イエロー `#854d0e` / `#eab308`)
  - Librosa / Tensor / Essentia 並列特徴量抽出・JSON永続化
- **Phase 5: Terminal Persistence Monad \(\eta: F \Rightarrow G\)** (明るいピンク `#9d174d` / `#ec4899`)
  - JSON集約・PostgreSQL UPSERT・DLQ退避 (SendFailed)
- **Phase 6: Terminal Object & Side-Effect Completion \(\mathcal{T}_{\text{final}}\)** (明るい紫 `#5b21b6` / `#a855f7`)
  - FLACタグ書き戻し・Windows SetFileTime保護・キャッシュ消去

---

## 日本語版 (Japanese Version)

```mermaid
stateDiagram-v2
    classDef phase1 fill:#334155,stroke:#64748b,color:#f8fafc,stroke-width:2px;
    classDef phase2 fill:#1e3a8a,stroke:#2563eb,color:#eff6ff,stroke-width:2px;
    classDef phase3 fill:#065f46,stroke:#059669,color:#ecfdf5,stroke-width:2px;
    classDef phase4 fill:#854d0e,stroke:#eab308,color:#fef9c3,stroke-width:2px;
    classDef phase5 fill:#9d174d,stroke:#ec4899,color:#fce7f3,stroke-width:2px;
    classDef phase6 fill:#5b21b6,stroke:#a855f7,color:#f3e8ff,stroke-width:2px;

    [*] --> StartupReset
    
    subgraph Cat_Init ["Phase 1: Initialization Functor (基盤・初期化)"]
        StartupReset: オーケストレーター起動
        Idle: orchestrator.db の RUNNING/PENDING タスクを FAILED へリセット
        TaskReceived: /task APIへファイルパスがPOSTされる
        CueInspect: worker_cue.py 起動<br/>（CUE/タグ解析・トラック自動抽出。CUE不在時も自動フォールバック）
        CheckState: orchestrator.db (SQLite) で各トラックの(file_path, track_number)確認
    end

    subgraph Cat_Dedup ["Phase 2: Duplication & Identity Isomorphism (重複照合・同型)"]
        Skipped: 全トラックが COMPLETED / RUNNING / PENDING (force:false 時)
        Queued: 未処理トラックを PENDING として登録
        ResponseAccepted: レスポンス 202 Accepted (展開トラック数返却)
        CalcHash: worker_demucs.py --check-hash-only<br/>(トラック波形MD5算出)
        CheckHashDB: ingester.py --check-hash<br/>(PostgreSQL重複照合)
        SkippedByHash: PostgreSQLに同ハッシュが既に存在 (skip_dup_by_hash=true 時)
    end

    subgraph Cat_HeavyState ["Phase 3: Monadic Heavy Resource Functor (重畳分離・SHM資源)"]
        ResourceWait: 未登録楽曲 semaphore 監視
        AllocatingSHM: メモリ空き容量・並列上限セマフォ監視
        DemucsProcessing: worker_demucs.py 起動<br/>（波形スライスデコード・分離・SHM書き込み）
        FreezingSHM: Go側で共有メモリを PAGE_READONLY 化
        Precache: functor_precache.py 起動<br/>（SHM read-only アタッチ・メタデータ整合性検証）
    end

    subgraph Cat_ParallelProduct ["Phase 4: Parallel Product Morphisms (並列積射抽出)"]
        ParallelFeatureExtracting: ポストDemucs並列特徴量抽出起動<br/>（Librosa, Tensor, Essentia 3本同時並列実行）
        ReleaseSHM: Go側で共有メモリ (SHM) 解放
        WriteJSONFiles: 中間JSONファイル書き込み<br/>(queue/ ディレクトリへ一時出力)
    end

    subgraph Cat_PersistenceMonad ["Phase 5: Terminal Persistence Monad (Ingest永続化モナド)"]
        Ingesting: ingester.py 起動（JSON集約・DB照合）
        PostgreSQL_Upsert: DB正常時 (PostgreSQLへUPSERT)
        DLQ_Fallback: DB接続不可時 (send_failed.dbへ退避)
    end

    subgraph Cat_Finalize ["Phase 6: Terminal Object & Side-Effect Completion (終端射・完了)"]
        TagWriteback: FLACタグ書き戻し &<br/>Windows タイムスタンプ保護 (SetFileTime)
        IngesterCleanup: ingester.py による中間JSON・キャッシュ削除
        TaskCompleted: Go defer クリーンアップ実行後<br/>orchestrator.db の status を COMPLETED に更新
    end

    StartupReset --> Idle
    Idle --> TaskReceived
    TaskReceived --> CueInspect
    CueInspect --> CheckState

    CheckState --> Skipped
    CheckState --> Queued

    Skipped --> [*]: レスポンス 200 OK (処理スキップ)
    Queued --> ResponseAccepted
    ResponseAccepted --> CalcHash

    CalcHash --> CheckHashDB
    CheckHashDB --> SkippedByHash
    CheckHashDB --> ResourceWait

    ResourceWait --> AllocatingSHM
    AllocatingSHM --> DemucsProcessing
    DemucsProcessing --> FreezingSHM
    FreezingSHM --> Precache

    Precache --> ParallelFeatureExtracting
    ParallelFeatureExtracting --> ReleaseSHM
    ReleaseSHM --> WriteJSONFiles

    WriteJSONFiles --> Ingesting
    SkippedByHash --> TaskCompleted
    Ingesting --> PostgreSQL_Upsert
    Ingesting --> DLQ_Fallback

    PostgreSQL_Upsert --> TagWriteback
    DLQ_Fallback --> IngesterCleanup
    TagWriteback --> IngesterCleanup

    IngesterCleanup --> TaskCompleted
    TaskCompleted --> [*]

    class StartupReset,Idle,TaskReceived,CueInspect,CheckState phase1;
    class Skipped,Queued,ResponseAccepted,CalcHash,CheckHashDB,SkippedByHash phase2;
    class ResourceWait,AllocatingSHM,DemucsProcessing,FreezingSHM,Precache phase3;
    class ParallelFeatureExtracting,ReleaseSHM,WriteJSONFiles phase4;
    class Ingesting,PostgreSQL_Upsert,DLQ_Fallback phase5;
    class TagWriteback,IngesterCleanup,TaskCompleted phase6;
```

---

## English Version

```mermaid
stateDiagram-v2
    classDef phase1 fill:#334155,stroke:#64748b,color:#f8fafc,stroke-width:2px;
    classDef phase2 fill:#1e3a8a,stroke:#2563eb,color:#eff6ff,stroke-width:2px;
    classDef phase3 fill:#065f46,stroke:#059669,color:#ecfdf5,stroke-width:2px;
    classDef phase4 fill:#854d0e,stroke:#eab308,color:#fef9c3,stroke-width:2px;
    classDef phase5 fill:#9d174d,stroke:#ec4899,color:#fce7f3,stroke-width:2px;
    classDef phase6 fill:#5b21b6,stroke:#a855f7,color:#f3e8ff,stroke-width:2px;

    [*] --> StartupReset

    subgraph Cat_Init ["Phase 1: Initialization Functor"]
        StartupReset: Orchestrator Startup
        Idle: Reset stale tasks to FAILED
        TaskReceived: File path POSTed to /task
        CueInspect: Execute worker_cue.py
        CheckState: Check orchestrator.db state
    end

    subgraph Cat_Dedup ["Phase 2: Duplication & Identity Isomorphism"]
        Skipped: All tracks already done
        Queued: Register PENDING tracks
        ResponseAccepted: 202 Accepted
        CalcHash: worker_demucs.py --check-hash-only
        CheckHashDB: Check DB hash
        SkippedByHash: Hash exists in PostgreSQL
    end

    subgraph Cat_HeavyState ["Phase 3: Monadic Heavy Resource Functor"]
        ResourceWait: Semaphore waiting
        AllocatingSHM: Monitor RAM & concurrency limit
        DemucsProcessing: Execute worker_demucs.py
        FreezingSHM: Freeze SHM to PAGE_READONLY
        Precache: Execute functor_precache.py
    end

    subgraph Cat_ParallelProduct ["Phase 4: Parallel Product Morphisms"]
        ParallelFeatureExtracting: Parallel feature extraction
        ReleaseSHM: Unmap SHM handles
        WriteJSONFiles: Write intermediate JSON files
    end

    subgraph Cat_PersistenceMonad ["Phase 5: Terminal Persistence Monad"]
        Ingesting: Execute ingester.py
        PostgreSQL_Upsert: PostgreSQL UPSERT
        DLQ_Fallback: Save to send_failed.db
    end

    subgraph Cat_Finalize ["Phase 6: Terminal Object & Side-Effect Completion"]
        TagWriteback: FLAC tag writeback & SetFileTime
        IngesterCleanup: Purge temp files
        TaskCompleted: Task COMPLETED in DB
    end

    StartupReset --> Idle
    Idle --> TaskReceived
    TaskReceived --> CueInspect
    CueInspect --> CheckState

    CheckState --> Skipped
    CheckState --> Queued

    Skipped --> [*]
    Queued --> ResponseAccepted
    ResponseAccepted --> CalcHash

    CalcHash --> CheckHashDB
    CheckHashDB --> SkippedByHash
    CheckHashDB --> ResourceWait

    ResourceWait --> AllocatingSHM
    AllocatingSHM --> DemucsProcessing
    DemucsProcessing --> FreezingSHM
    FreezingSHM --> Precache

    Precache --> ParallelFeatureExtracting
    ParallelFeatureExtracting --> ReleaseSHM
    ReleaseSHM --> WriteJSONFiles

    WriteJSONFiles --> Ingesting
    SkippedByHash --> TaskCompleted
    Ingesting --> PostgreSQL_Upsert
    Ingesting --> DLQ_Fallback

    PostgreSQL_Upsert --> TagWriteback
    DLQ_Fallback --> IngesterCleanup
    TagWriteback --> IngesterCleanup

    IngesterCleanup --> TaskCompleted
    TaskCompleted --> [*]

    class StartupReset,Idle,TaskReceived,CueInspect,CheckState phase1;
    class Skipped,Queued,ResponseAccepted,CalcHash,CheckHashDB,SkippedByHash phase2;
    class ResourceWait,AllocatingSHM,DemucsProcessing,FreezingSHM,Precache phase3;
    class ParallelFeatureExtracting,ReleaseSHM,WriteJSONFiles phase4;
    class Ingesting,PostgreSQL_Upsert,DLQ_Fallback phase5;
    class TagWriteback,IngesterCleanup,TaskCompleted phase6;
```
