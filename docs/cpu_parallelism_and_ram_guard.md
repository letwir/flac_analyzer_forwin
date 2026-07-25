# CPU並列処理最適化 ＆ リアルタイムRAM制御 アーキテクチャ解説書

## 概要

本ドキュメントは、**Flac_Analyzer** の Go オーケストレーターにおける CPU 並列処理スケーリング、ONNX Runtime SegFault 回避設計、およびリアルタイム空き RAM バックプレッシャー Guard の技術的仕様・設計思想について解説する補足資料です。

---

## 1. 全体アーキテクチャ ＆ 圏論的アプローチ

Flac_Analyzer は、各処理ステップを**厳格な射（Morphism）**および**純粋な読取射（Read-Only Reader）**としてモデル化し、パイプラインの安全な並列性を保証しています。

```
[FLAC File] ──► [Demucs (limit=1)] ──► [SHM Freeze (PAGE_READONLY)]
                                                │
                       ┌────────────────────────┼────────────────────────┐
                       ▼                        ▼                        ▼
              [worker_librosa.py]       [worker_tensor.py]      [worker_essentia.py]
                       │                        │                        │
                       └────────────────────────┼────────────────────────┘
                                                ▼
                                    [JSON Output & Ingester]
```

---

## 2. 並列処理・安定性設計の主要メカニズム

### ① ONNX Runtime SegFault 回避 (`demucs_concurrent_limit = 1`)
- **課題**: Demucs 音源分離で使用する ONNX Runtime (CUDA / DirectML / C++ バックエンド) は、複数プロセスから同時に並列アクセスされると DirectX / CUDA ドライバおよびメモリコンテキストの衝突により **SegFault (Access Violation)** を引き起こすリスクがあります。
- **対策**: Go オーケストレーター側で `demucs_concurrent_limit = 1` の排他制御セマフォを厳格に保持し、Demucs 推論がプロセス間で重複起動することを構造的に防止しています。

### ② ポスト Demucs ワーカーの `sync.WaitGroup` 並列同時実行
- **従来の問題**: Demucs 完了後、以前のバージョンでは `Librosa` $\to$ `Tensor` $\to$ `Essentia` を直列（`await`）で順次起動していたため、1タスクあたり 1 コアしか使えず、32 コア環境で CPU 稼働率が低下していました。
- **並列化の実現**: Demucs が書き込んだ共有メモリ (SHM) は Go 側で `PAGE_READONLY` 化（Freeze）されるため、後続ワーカーは完全な**参照透過的読取射 (Pure Reader)** となります。
- **実装**: `orchestrator/dispatcher/dispatcher.go` にて、`worker_librosa.py`, `worker_tensor.py`, `worker_essentia.py` を `sync.WaitGroup` を用いて**同時に3ワーカー平行起動**します。これにより 1 タスクあたり 3 コアを同時稼働させ、パイプライン全体で **CPU 全32コアを 100% 近くまでフル稼働**させます。

### ③ OS スレッド過剰競合の防止 (`OMP_NUM_THREADS = 1` 維持)
- 各 Python ワーカー内部で OpenMP / OpenBLAS / MKL のスレッド数を無闇に引き上げると（例: 22ワーカー × 4スレッド ＝ 88スレッド）、OS の CPU スケジューラキューで過剰なコンテキストスイッチ爆発が発生し、スループットが低下します。
- ワーカープロセス自体の並列度を高め、`OMP_NUM_THREADS = 1` を維持することで、無駄なスレッド競合と隠蔽された可変状態 (Hidden Shared State) を回避しています。

### ④ `MaxRamRatio` ベースのリアルタイム空き RAM バックプレッシャー Guard
- タスク投入直前、Windows API (`GetMemoryInfo`) を呼び出し、リアルタイムのシステム使用中メモリ量 (`TotalPhys - AvailPhys`) を監視します。
- 使用量が目標上限（例: `max_ram_ratio = 0.625` ≒ **40GB上限**）に達している場合、Worker は自動的に 2 秒間スリープ（バックプレッシャー）し、既存タスクのメモリ解放を安全に待ちます。

### ⑤ `0` 指定時の `runtime.NumCPU()` 自動解決 (`resolvePythonEnv`)
- `config.toml` の `num_workers` や `python_env` の各項目に `0` または `"0"` が指定された場合、純粋関数 `resolvePythonEnv` が現在のシステム CPU 論理コア数 (`runtime.NumCPU()`) を検出し、決定論的に最適なワーカー数およびパラメータを動的算出します。

---

## 3. 設定パラメータと推奨構成

| パラメータ | 設定値 | 説明 |
| :--- | :--- | :--- |
| `orchestrator.num_workers` | `0` (自動) または `22` | システム RAM / CPU コア数から最大安全並列ワーカー数を自動決定。 |
| `orchestrator.max_ram_ratio` | `0.625` | 全体物理メモリの 62.5%（64GB 環境で約 40GB）を上限としてリアルタイム制御。 |
| `orchestrator.demucs_concurrent_limit` | `1` | SegFault 防止のための Demucs 推論排他実行制限。 |
| `python_env.omp_num_threads` | `"1"` (または `"0"`) | スレッド競合を防ぎ、プロセス並列性を最大化。 |

---

## 結論

本アーキテクチャにより、ONNX の SegFault リスクをゼロに抑えた完全な安全性と、40GB RAM 領域内でのシステム全32コアを使い切る超高速な並列音響解析を両立しています。
