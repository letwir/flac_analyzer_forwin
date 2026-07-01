# 機能拡張と圏論的アーキテクチャの再構築 (Feature Expansion & Categorical Refactoring) - Rev.2

旦那様からのフィードバック（ETLでのクロス計算除外、CuPy/PyTorchの許可、ファイル命名規則の刷新）を反映した最終プランですわ！

## 解決した設計課題 (Resolved Design Decisions)

1. **圏論的破綻の完全回避 (No Categorical Breakdown)**:
   - ステム間相互作用 (Coherence 等) は **ETL (Python) 側では一切計算しない** ことが決定しました。
   - すべてのワーカーは引き続き「単一ステムに対する純粋な射 ($Stem \to Feature$)」として振る舞い、位相やPSDを抽出するにとどめます。
   - ステム間の相関は、抽出された特徴量を元に **後段の SQL (PostgreSQL) 側で直積を組んで比較計算** します。これにより、ETLアーキテクチャの純粋性は完全に守られました。

2. **余剰リソース (VRAM 10GB / CPU 26Threads) の活用**:
   - `cupy` または `torch` (PyTorch) を導入し、FFT などの重い周波数領域変換を並列化します。
   - `requirements.txt` に依存関係を追記し、`.venv` にインストールします。

3. **圏論的役割に応じたファイル命名 (Categorical Naming Convention)**:
   - ファイルの可視性を高めるため、接頭辞を `worker_` や `functor_` に統一し、アルファベット順ソート時に役割ごとにまとまるように変更します。

---

## Proposed Changes (実行予定の変更)

### 1. ファイルのリネームと整理
Gitの `mv` コマンドを用いて、既存のワーカー群を役割（計算コンテキスト）ごとにリネームします。
- `librosa_worker.py` ➔ `worker_librosa.py`
- `demucs_worker.py` ➔ `worker_demucs.py`
- `essentia_worker.py` ➔ `worker_essentia.py`
- `analyzer_worker.py` ➔ `worker_analyzer.py`

### 2. 新規ワーカーと関手の追加
- **`functor_precache.py`**: (旧案の `precache.py`) 波形を STFT/CQT 表現へ写像する関手。
- **`worker_tensor.py` (または `worker_cupy.py`)**: `cupy` / `torch` を用いて、CPU/GPUをハイブリッドに活用し、位相 (Phase) や PSD、Spectral Flux、Band-limited envelope 等を抽出する純粋な射。

### 3. `requirements.txt` の更新
- `cupy-cuda12x` (または環境に合わせたバージョン) もしくは `torch` を追記。
- 依存関係のインストール (`.venv\Scripts\pip.exe install -r requirements.txt`)

### 4. `pipeline.py` およびオーケストレーターの修正
- ワーカーの呼び出しパスを新しい `worker_*.py` に書き換えます。

## User Review Required

> [!IMPORTANT]
> **PyTorch (tensor) vs CuPy の選定**
> 10GBのVRAMを有効活用しつつ、CUDAコアが100%に張り付いている現状を鑑みると、CPUの余剰スレッド（26スレッド）も柔軟に活用できる **PyTorch (`torch`)** の方が、デバイスフォールバック（GPUが重い場合はCPUでFFTを回す等）がしやすく安全かと存じますが、いかがでしょうか？
> （純粋なNumPyのDrop-in replacementとしては CuPy が優秀ですが、CPU/GPUの動的負荷分散においては PyTorch に分があります）

> [!TIP]
> **ファイル名の一括変更について**
> よろしければ、このまま私が `git mv` を用いてリネームとコード内の参照修正（`pipeline.py` など）を自動実行いたしますわ。