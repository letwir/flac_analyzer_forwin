# Go+Python 共有メモリアーキテクチャ 結合テストと最終調整 計画書

旦那様、少し期間が空きましたので、現状の整理とこれからの実装計画（最終フェーズ）をまとめましたわ！

## 現状の進捗 (Status Check)
これまでのセッションで、以下のタスクが完了しておりますの。
- **Go オーケストレーター**: HTTPサーバーとGoroutineワーカプール基盤の実装。`--no-db` テストフラグの追加。(`orchestrator/main.go`)
- **Python側のDB依存排除**: `db.py` の削除と psycopg2 依存の排除。結果を JSON Lines として吐き出す処理へのリファクタリング完了。(`pipeline.py`)
- **Zero-copy 共有メモリラッパー**: `shm_windows.go` および Python側の `shm_interop.py` (WORM Shared Memory) の基盤作成完了。

## 残課題 (Remaining Tasks from issues.md)
1. **[ ] 【Python】Demucs分離波形をGoが確保した共有メモリに書き込み、Librosa が Read-Only で参照する Zero-copy 読み書きパイプラインの実装**
   - 既存の `multiprocessing.shared_memory` 依存を剥がし、`shm_interop.py` を用いたインターフェースへ完全移行させますわ。
2. **[ ] 【Testing】`testFLAC` ディレクトリのファイルを用いた単体・結合テストの実行。OOM・エラー監視と、経過時間（Execution Time）の厳密な計測。**
   - `--no-db` モードを用いて、実環境（PostgreSQL）にアクセスせずローカルJSONでの結合テストを実施します。Peak RAMの監視も行いますの。

---

## 提案する変更内容 (Proposed Changes)

### 1. Zero-copy パイプラインの結合 (Orchestration & IPC)
#### [MODIFY] analyzer_worker.py
- `shm_interop.read_from_shm` を利用して共有メモリから Read-Only で配列をアタッチする処理を組み込み、Goからのシグナルを安全に受け取る形に修正いたします。
#### [MODIFY] pipeline.py
- `analyze_segment_pipeline` 内の旧 `SharedMemory` 作成ロジックを削除し、Go 経由（または `shm_interop` の Windows 名前付き共有メモリ経由）での連携処理を完成させますわ。
#### [MODIFY] demucs_worker.py
- Demucsの出力を `shm_interop.write_to_shm` 経由で書き込む処理を追加いたします。

### 2. 結合テストと計測の実施
#### [MODIFY] run_batch.ps1 / main.go
- ローカルの `testFLAC/` フォルダを対象に、End-to-End でテストが通るかを確認するためのドライランを自動化します。

## Open Questions (旦那様への確認事項)
> [!IMPORTANT]
> - Go側からの Python プロセス (`demucs_worker.py` と `librosa_worker.py`) 呼び出し時の引数インターフェース（共有メモリのタグ名 `Local\FlacShm_...` の渡し方）は、コマンドライン引数で渡す形でよろしいでしょうか？
> - テスト時、OOMの監視スクリプト等はPowerShell側で別途回すか、それともGoのオーケストレーター内で `psutil` に相当するモニタリングを行う方針で進めますか？

## Verification Plan (検証計画)
1. `testFLAC/` 以下のファイルに対し、`go run ./orchestrator/main.go --no-db` と `run_batch.ps1` を併用してキューイング。
2. Python のエラーが出ずに各曲のJSON Linesが `testFLAC/` フォルダに出力されることを確認。
3. リソースモニタリングにより、以前発生していた 64GB RAM 枯渇（OOM）が解消されているかを証明。
