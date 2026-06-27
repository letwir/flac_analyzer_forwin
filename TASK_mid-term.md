# 次世代アーキテクチャ・マイルストーン (Next-Gen Architecture Milestones)

本ドキュメントは、次回以降のセッションで確実に引き継ぐための、Go+Python 共有メモリアーキテクチャへの移行におけるマイルストーンおよび目標分解（大・中・小）ですわ。

## 【大目標】
**関数型・圏論的制約に基づく Go + Python ハイブリッド共有メモリアーキテクチャの完成**
- Windows 11 の制約下において、`/dev/shm` に代わる `CreateFileMapping` を用いた強固な共有メモリ管理を確立する。
- Pythonプロセスを副作用（状態・IO）から解放し、純粋関数的な並列特徴抽出パイプラインへと昇華させる。
- PostgreSQLへの12分に及ぶ UPSERT ロックをGoの非同期Goroutineに委譲し、システムの Peak RAM および稼働時間効率を極限まで引き上げる。

## 【中目標】
1. **[Orchestration] Go オーケストレーターの土台構築**
   - PowerShellからのタスクをキューイングし、PythonサブプロセスをLSP風に管理・ディスパッチするGoレイヤの実装。
2. **[Memory Management] 共有メモリの WORM化 (Write-Once, Read-Many)**
   - Go側の `syscall` による共有メモリアロケーションと、Python側の `mmap` アタッチ。Demucsが書き込んだ波形データを Immutable とみなす Actor Model パターンの結合。
3. **[Purity] Python ワーカーからの DB 依存排除**
   - 既存の `db.py` を削除し、Pythonの出力結果を JSON Lines としてGoへ送り返すだけの純粋な状態へリファクタリング。
4. **[Testing & Validation] DB非依存のローカル検証環境確立**
   - テスト時には PostgreSQL へのアクセスを完全に禁じ（`--no-db` モード等）、結果をローカルJSONとしてダンプすることで、ネットワークやDBのロックに起因しない純粋なシステムテストとプロファイリングを実現する。

## 【タスク細分化 (Next Actions)】
具体的なタスクリストは `issues.md` に記載され、管理されています。
1. **ps1改修**: `Get-ChildItem` による検索結果をGoキューへ投下。
2. **Go共有メモリAPI**: `shm_windows.go` を用いた低レイヤWin32 APIのハンドリング。
3. **Goオーケストレーター**: DB UPSERT機能のモック化（ローカルテスト用）と非同期キューの作成。
4. **Python IPC実装**: `multiprocessing.shared_memory` と JSON stdout を用いたプロセス間通信の確立。
5. **結合テストの実行**:
   - `testFLAC` フォルダ配下のFLACを用いたテスト。
   - `psutil` や `time` モジュールを用いた**実行時間（Execution Time）の精密な計測**。
   - OOMの発生確認および例外（エラー）の捕捉と修正。
   - **※注意**: このテストフェーズにおいて、PostgreSQLへの実データ UPSERT は厳禁とし、全てローカルへのモック出力にて検証を行うこと。
