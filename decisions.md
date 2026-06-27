# 1ファイルインプロセス解析へのアーキテクチャ移行決定書

旦那様の 5950X ＆ 64GB RAM 環境において、FLAC解析の並列実行中に発生する RAM OOM (Out Of Memory) を完全に制圧するための設計決定ですわ。

---

## 課題の背景と根本原因

従来の Producer-Consumer 並列パイプラインでは、以下の要因により RAM 容量 (56/64GB) を圧迫し、OOM やリソース枯渇によるディスク書き込み失敗 (`OSError`) を引き起こしていましたわ。
1. **SharedMemory (SHM) の蓄積**: Windows における Python の `multiprocessing.shared_memory` は、参照解放後も非ページプールにメモリが残留しやすく、OSレベルでのリークを招きやすいですの。
2. **Pythonインタプリタのメモリ断片化**: NumPy / PyTorch 等の巨大バイナリを長時間並列で稼働し続けると、メモリの断片化（Fragmentation）が発生し、実消費量が肥大化し続けますわ。

---

## アーキテクチャ移行の方針

**「PowerShell によるファイル一次保存 ＆ Python 単一ファイルインプロセス解析」へ完全移行いたしますわ！**

### 変更の核心：
1. **プロセス生存期間の極小化**:
   - `run_batch.ps1` が対象ディレクトリ配下の FLAC ファイルを再帰的に列挙して一次保存しますの。
   - ループ処理により、1ファイルごとに `python main.py <flacfullpath>` を同期的に起動し、解析が完了したら即座に Python プロセスを終了（メモリ完全解放）させますわ。
2. **共有メモリ・ディスクキャッシュ転送の全撤廃**:
   - 単一プロセス内で「デコード → 波形分離 → 特徴量抽出 → DB保存」を自己完結させるため、`SharedMemory` や `.npy` キャッシュへ退避・復元するオーバーヘッドと状態管理が 100% 不要になりますの。
3. **インメモリ直接処理による高速化**:
   - 分離した波形（numpy 配列）を直接 Librosa / ONNX に流し込むため、I/O オーバーヘッドがなく、コードが劇的にシンプル（純粋関数的）になりますわ。

---

## 調整された実装アプローチ

- **`run_batch.ps1`**:
  - `Get-ChildItem` で指定ルートディレクトリ以下のすべての `.flac` ファイルを取得して配列に保持。
  - ループ内で `python main.py <flacfullpath> --resume --rough` などを順次呼び出し。
- **`main.py`**:
  - `directory` 引数を廃止し、`filepath` (単一のFLACファイルパス) を受け取るように変更。
  - P/C パイプライン（multiprocessing）の起動ロジックを廃止し、`pipeline.process_single_flac_file_directly` を呼び出すシンプルな構成に改修。
- **`pipeline.py`**:
  - `process_single_flac_file_directly` を新規追加。`run_producer` と `run_consumer` のハイブリッド直列版として動作。
  - `GLOBAL_DEMUCS` や `Essentia` のインプロセス初期化。
  - `analyze_stems` (Librosa スレッドプール並列) による特徴量抽出と `upsert_flac` による DB 書き込み。
