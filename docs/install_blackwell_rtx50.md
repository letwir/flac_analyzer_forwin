# NVIDIA RTX 50xx シリーズ (Blackwell アーキテクチャ) 専用セットアップガイド

本ドキュメントは、NVIDIA GeForce RTX 5070 Ti / 5080 / 5090 等の **Blackwell 世代 GPU** および **CUDA 13.2+** 環境において、Flac_Analyzer を高速かつ安定して動作させるためのセットアップ手順書です。

---

## 1. 概要と前提要件

RTX 3060 等の従来世代 (Ampere / Ada Lovelace) と異なり、Blackwell アーキテクチャ (RTX 50xx シリーズ) では CUDA 13.2 以上およびそれに適合する PyTorch / ONNX Runtime GPU ビルドが必須となります。

- **対象 GPU**: NVIDIA GeForce RTX 5070 Ti / RTX 5080 / RTX 5090
- **OS**: Windows 11 (64-bit) / Windows 10 (64-bit)
- **Python**: Python 3.12 または 3.13 (64-bit)
- **CUDA Toolkit / Driver**: NVIDIA Driver 570.xx 以上 (CUDA 13.2 サポート)

---

## 2. インストール手順

### ステップ 1: Python 仮想環境の作成

必ず専用の Python 仮想環境を作成して有効化します。

```powershell
# プロジェクトルートディレクトリにて実行
python.exe -m venv .venv
. .\.venv\Scripts\Activate.ps1
```

### ステップ 2: pip の最新化

```powershell
python.exe -m pip install --upgrade pip
```

### ステップ 3: Blackwell 専用パッケージの一括インストール

本リポジトリ同梱の `requirements-blackwell.txt` を指定して依存パッケージをインストールします。

```powershell
pip install -r requirements-blackwell.txt
```

> [!NOTE]
> `requirements-blackwell.txt` には PyTorch の CUDA 13.2 対応 Nightly インデックス URL (`https://download.pytorch.org/whl/nightly/cu132`) および `onnxruntime-gpu==1.23.2` があらかじめ定義されているため、自動的に最適パッケージが導入されます。

---

## 3. 手動で個別インストールする場合のパッケージコマンド

自動インストールではなく、手動で PyTorch および ONNX Runtime を個別にインストール・更新する場合は以下のコマンドを実行します。

```powershell
# 1. 既存の不要な ONNX Runtime を削除
pip uninstall -y onnxruntime onnxruntime-directml onnxruntime-gpu

# 2. Blackwell 対応 PyTorch (CUDA 13.2) のインストール
pip install torch torchaudio --extra-index-url https://download.pytorch.org/whl/nightly/cu132

# 3. ONNX Runtime GPU のインストール
pip install onnxruntime-gpu
```

---

## 4. 動作検証と GPU 認識チェック

仮想環境の PowerShell 上で以下のコマンドを実行し、PyTorch および ONNX Runtime が Blackwell GPU (RTX 50xx) を認識しているか確認します。

```powershell
python.exe -c "import torch, onnxruntime as ort; print('PyTorch CUDA:', torch.cuda.is_available(), torch.cuda.get_device_name(0) if torch.cuda.is_available() else ''); print('ORT Providers:', ort.get_available_providers())"
```

### 正常出力例
```text
PyTorch CUDA: True NVIDIA GeForce RTX 5070 Ti
ORT Providers: ['CUDAExecutionProvider', 'CPUExecutionProvider']
```

---

## 5. トラブルシューティング

### Q1. `CUDAExecutionProvider` が ORT Providers に表示されない
- **原因**: 既存の `onnxruntime` (CPU版) や `onnxruntime-directml` と競合している可能性があります。
- **対処法**:
  ```powershell
  pip uninstall -y onnxruntime onnxruntime-directml
  pip install --force-reinstall onnxruntime-gpu
  ```

### Q2. Demucs 推論時に CUDA Out of Memory (OOM) が発生する
- **原因**: 他のプロセスが VRAM を占有しているか、ワーカーの同時実行数が過大です。
- **対処法**: `config.toml` の `demucs_concurrent_limit = 1` に設定されていることを確認してください。また、オーケストレーターに新設された `tensorSemaphore` が自動的に VRAM の逐次クリーンアップ (`torch.cuda.empty_cache()`) を行います。
