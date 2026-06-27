# ウォークスルー: Python Zero-copy 共有メモリパイプラインの実装

旦那様、Zero-copy読み書きパイプラインのPython側実装を完了いたしましたわ！

## 完了した変更点
1. **完全疎結合なワーカーの分離**
   - 既存の巨大な `pipeline.py` に代わり、役割を厳格に分割した `demucs_worker.py` と `librosa_worker.py` を作成しました。
   - これにより、Go側がプロセスを順次起動し、ライフサイクル（メモリ解放など）を完全に制御できるようになりました。

2. **WORM (Write-Once, Read-Many) アーキテクチャの確立**
   - `demucs_worker.py` は、Go側から `--shm-tags` 引数で指定された共有メモリタグ名（例: `Local\FlacShm_mix`）を受け取り、そこに分離波形を Zero-copy で書き込みます。
   - 書き込み完了時、波形のメタデータ (shape, dtype) を標準出力に JSON として吐き出し、`sys.exit(0)` することで、Goへ「書き込み完了（Mutable状態の終了）」シグナルを送信します。
   - Goはその後 `Freeze()` (VirtualProtect) を適用し、不変データ (Immutable) となったメモリの情報を `librosa_worker.py` へ `--shm-metadata` として渡します。
   - `librosa_worker.py` は、その共有メモリを Read-Only で安全にアタッチして特徴量抽出を行います。

3. **圏論的制約の充足**
   - 副作用のある状態（波形の書き込み中）と、純粋な関数の実行（Librosaによる特徴量抽出）が、明確なプロセス分離と `exit 0` というシグナルによって完全に隔離されましたわ（Side-effect Isolation）。

## 次のステップ
今回構築したワーカー（`demucs_worker.py` / `librosa_worker.py`）をGo側（`orchestrator/main.go`）から `cmd.Wait()` を用いて順次起動し、その間に `Freeze()` 射を適用するロジックをGo側に組み込む作業が必要となりますの。