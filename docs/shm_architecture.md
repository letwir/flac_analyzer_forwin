# Windows 共有メモリ (SHM) 管理と WORM アーキテクチャ

本システムでは、Windows 環境において大量の音源ファイル（数十GB〜数TB）を一括処理する際のメモリ不足（OOM）や I/O ボトルネックを根絶するため、**Windows 共有メモリ (Shared Memory) による WORM (Write-Once Read-Many) アーキテクチャ** を採用しています。

## 1. WORM (Write-Once Read-Many) アーキテクチャ

1. **書き込みフェーズ (Write Phase)**:
   - `worker_demucs.py` が FLAC ファイルをスライスデコードし、Demucs による音源分離 (stems: `mix`, `drums`, `bass`, `other`, `vocals`) を実行します。
   - 分離された float32 多次元配列テンソルは、`shm_interop.py` (`write_to_shm` / `mmap.ACCESS_WRITE`) を介して Win32 API (`CreateFileMappingW`, `MapViewOfFile`) により `PAGE_READWRITE` モードでメモリ上に作成された命名共有メモリ領域に直接書き込まれます。
2. **フリーズフェーズ (Freeze Phase)**:
   - Go オーケストレーター (`shm_windows.go`) が Python プロセスからの書き込み完了を検知すると、`VirtualProtect` を呼び出して共有メモリのメモリ保護属性を `PAGE_READWRITE` から **`PAGE_READONLY`** へ変更（フリーズ）します。
3. **並行読み取りフェーズ (Read-Many Phase)**:
   - 後続の特徴量抽出ワーカー (`functor_precache.py`, `worker_librosa.py`, `worker_tensor.py`, `worker_essentia.py`) は、`PAGE_READONLY` で保護された共有メモリ領域に `shm_interop.attach_shm_read_only()` 経由でアタッチします。
   - `functor_precache.py` は、ディスクへの中間 `.npy` ファイル保存を完全に排除し、共有メモリのアタッチ性検証とメタデータ整合性の高速チェックのみを行います。
   - 各抽出ワーカーは、他のワーカーや自身の誤動作によって共有メモリ上の波形データが改変されるリスクから物理的に保護された状態で並行解析を実行します。

## 2. ShmArenaPool ＆ Producer-Consumer ゼロコピー IPC シーケンス (Mermaid)

毎曲ごとの共有メモリ作成・破棄ループを廃止し、ワーカーごとに 7 ステム分 (`mix`, `drums`, `bass`, `other`, `vocals`, `guitar`, `piano`) の共有メモリアリーナをプール管理する `ShmArenaPool` を採用しています。

```mermaid
sequenceDiagram
    autonumber
    participant Go as Go Orchestrator (shm_windows.go / ShmArenaPool)
    participant Producer as Producer (worker_demucs.py / shm_interop.py)
    participant SHM as Windows Shared Memory (ShmArenaPool)
    participant Precache as Precache (zig/functor_precache.py)
    participant Consumers as Consumers (worker_librosa / tensor / essentia)

    Note over Go,SHM: 起動時: ShmArenaPool がワーカー単位に 7 ステム永続アリーナを事前確保 (VirtualLock 物理RAM固着)
    Go->>SHM: EnsureCapacity(estimatedSize): 必要に応じてアリーナ容量を自律拡張
    Go->>Producer: 起動 (共有メモリタグ名渡す)
    Producer->>SHM: write_to_shm() via mmap(ACCESS_WRITE)<br/>(Zero-copy write to PAGE_READWRITE memory)
    Producer-->>Go: 書き込み完了シグナル
    Go->>SHM: FreezeAll(): 全ステムの Win32 API VirtualProtect(PAGE_READONLY)
    Note over SHM: 共有メモリ保護属性を PAGE_READONLY にロック (WORM化)
    
    Go->>Precache: zig/functor_precache.py 起動
    Precache->>SHM: attach_shm_read_only()<br/>(アタッチ性・メタデータ整合性チェック)
    Precache-->>Go: 検証成功

    par 3本同時並列実行 (Parallel Read-Many)
        Go->>Consumers: worker_librosa.py 起動
        Consumers->>SHM: attach_shm_read_only() (Zero-copy np.ndarray ビュー)
        Go->>Consumers: worker_tensor.py 起動
        Consumers->>SHM: attach_shm_read_only() (Zero-copy np.ndarray ビュー)
        Go->>Consumers: worker_essentia.py 起動
        Consumers->>SHM: attach_shm_read_only() (Zero-copy np.ndarray ビュー)
    end

    Consumers-->>Go: 特徴量抽出完了
    Consumers->>SHM: mmap.close() (Consumer デタッチ)

    Go->>SHM: UnfreezeAll(): Win32 API VirtualProtect(PAGE_READWRITE)
    Note over SHM: アリーナを再利用可能状態（Unfreeze）へ復帰。プロセス終了時のみ一括 Close
```

## 3. Win32 API 呼出一覧と役割

Windows C API (`kernel32.dll`) を Go言語の `syscall.NewLazyDLL` から直接呼び出して制御しています。

| API 名 | 主なパラメーター / 定数 | 役割と詳細 |
| :--- | :--- | :--- |
| `CreateFileMappingW` | `INVALID_HANDLE_VALUE`, `PAGE_READWRITE`, `size`, `name` | Windows ページングファイルバックの命名共有メモリハンドルの作成 |
| `MapViewOfFile` | `handle`, `FILE_MAP_WRITE \| FILE_MAP_READ`, `0`, `0`, `size` | 共有メモリハンドルをプロセスの仮想アドレス空間へマッピング |
| `GetProcessWorkingSetSizeEx` | `hProc`, `&minSize`, `&maxSize`, `&flags` | プロセスの現在のワーキングセットサイズ（Min/Max）およびクォータフラグを取得 |
| `SetProcessWorkingSetSizeEx` | `hProc`, `minSize`, `maxSize`, `flags` | プロセスの物理 RAM ワーキングセットの上限・下限を動的に拡大し、OS のメモリトリムを強力に抑制 |
| `VirtualLock` | `addr`, `size` | 共有メモリ空間のページを物理 RAM 上にピン留め（スワップアウト防止・RAM直載せ）。ワーキングセットクォータ不足 (1453) 発生時は自動でワーキングセットを動的拡大してリトライ |
| `VirtualUnlock` | `addr`, `size` | 共有メモリ空間の物理 RAM ピン留め解除 |
| `VirtualProtect` | `addr`, `size`, `PAGE_READONLY`, `&oldProtect` | 共有メモリ空間のアクセス保護属性を `PAGE_READONLY` に変更（WORMフリーズ処理） |
| `UnmapViewOfFile` | `addr` | プロセスの仮想アドレス空間から共有メモリのマッピングを解除 |
| `CloseHandle` | `handle` | Windows カーネルオブジェクト（共有メモリハンドル）のクローズと解放 |


## 4. ライフサイクル管理とリーク防止メカニズム

- **Win32 API による精密制御**:
  - Go 側では `syscall` または Win32 DLL 経由で `CreateFileMappingW`, `MapViewOfFile`, `VirtualProtect`, `VirtualLock`, `SetProcessWorkingSetSizeEx`, `UnmapViewOfFile`, `CloseHandle` を直接呼び出して管理します。
- **Working Set 動的オートスケール ＆ `VirtualLock` リトライ**:
  - 共有メモリの割り当て時（`NewSharedMemoryWithLock` / `EnsureCapacity`）、`VirtualLock` 実行時に `ERROR_WORKING_SET_QUOTA` (1453: Insufficient quota) が検知された場合、`ExpandWorkingSetForSize` が自動発動してプロセスのワーキングセットサイズを必要量に合わせて動的に引き上げ、即座にリトライします。
  - これにより、大容量トラックや複数ワーカーの並列実行時でも物理 RAM へのピン留め（固着化）が 100% 成功します。
- **同期遅延 (`shm_allocation_delay_sec`)**:
  - 各ワーカーが共有メモリハンドルを閉じる際、OS 側のハンドルフラグクリア待ちによる競合を防ぐため、`shm_allocation_delay_sec` で指定された安全セマフォ遅延が挿入されます。
- **`defer` ステートメントによる確実な解放**:
  - 全ての特徴量抽出タスク完了時、または途中でエラー（例外やワーカー異常終了）が発生した場合でも、Go の `defer` クリーンアップ関数が確実に発動し、`VirtualUnlock`、`UnmapViewOfFile` および `CloseHandle` を実行して共有メモリ領域を即座に OS へ返還します。
- **タスク単位の精密メモリサイズ計算 (`EstimateShmSizeForTask`)**:
  - CUE シート配下のサブトラック解析時、親 FLAC 全体の巨大なファイルサイズではなく、**切り出し区間のサンプル数 (`(EndSample - StartSample) * channels * 4bytes * 1.5`)** に基づいて必要最小限の共有メモリサイズを動的に計算します。
  - これにより、大容量 CD アルバム等のコンピレーション音源解析時における過剰なコミットチャージ要求（WinError 1455: ページングファイル不足）を完全防止します。

