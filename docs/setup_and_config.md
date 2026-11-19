# 初回セッティング・設定ファイル詳細ガイド

[README に戻る](../README.md)

## 1. GPU / Python 推論環境の構築

ベース仮想環境を作成後、お使いの GPU に合わせて推論バックエンドを選択してください。

```powershell
python.exe -m venv .venv
. .\.venv\Scripts\Activate.ps1
python.exe -m pip install --upgrade pip
pip install -r requirements.txt
```

- **NVIDIA GPU (RTX 30xx / 40xx, CUDA 12.x)**:
  ```powershell
  pip uninstall -y onnxruntime onnxruntime-directml
  pip install onnxruntime-gpu
  pip install torch torchaudio --index-url https://download.pytorch.org/whl/cu124
  ```
- **NVIDIA RTX 50xx (Blackwell / CUDA 13.2+)**:
  ```powershell
  pip install -r requirements-blackwell.txt
  ```
  詳細は [NVIDIA RTX 50xx 専用セットアップガイド](install_blackwell_rtx50.md) を参照してください。
- **AMD / Intel iGPU など DirectX 12 (DirectML)**:
  ```powershell
  pip uninstall -y onnxruntime onnxruntime-gpu
  pip install onnxruntime-directml
  ```

## 2. `config.toml` パラメータ仕様

プロジェクトルートの `config.toml.example` をコピーして `config.toml` を作成します。

```toml
[database]
url = "postgres://username:password@hostname:5432/flac_analyzer_db"
db_timeout_sec = 20

[orchestrator]
num_workers = 0
max_ram_ratio = 0.625
demucs_concurrent_limit = 1
queue_dir = "./queue"
skip_dup_by_hash = true
```

| パラメータ | 型 | 説明 |
| :--- | :--- | :--- |
| `database.url` | String | PostgreSQL 接続 URI（`postgres://user:pass@host:port/dbname`）。 |
| `database.db_timeout_sec` | Integer | PostgreSQL 接続・クエリタイムアウト秒数（デフォルト: `20`）。 |
| `orchestrator.num_workers` | Integer | 最大並列ワーカー数。`0` でホスト物理 RAM・CPU コア数から自動算出。 |
| `orchestrator.max_ram_ratio` | Float | システム物理 RAM に対する使用許可割合（デフォルト: `0.625`）。 |
| `orchestrator.demucs_concurrent_limit` | Integer | Demucs 音源分離の同時実行上限（ONNX SegFault 防止のため `1` 推奨）。 |
| `orchestrator.dedicated_vram_total_gb` | Float | 専用 VRAM 総容量 (GB) の明示指定（WMI 誤報回避用）。 |
| `orchestrator.shm_allocation_delay_sec` | Integer | SHM 確保・解放時の安全遅延（秒）。 |
| `orchestrator.queue_dir` | String | 解析中間 JSON 出力キューディレクトリ。 |
| `orchestrator.skip_dup_by_hash` | Boolean | `true` で波形 MD5 ハッシュが PG に既存なら Demucs・特徴抽出をスキップ。 |
| `orchestrator.gatekeeper_retry_delay_sec` | Integer | Gatekeeper NOGO 時の再試行待機秒数（デフォルト: `20`）。 |
| `orchestrator.config_watch_interval_sec` | Integer | `config.toml` ホットリロード監視間隔（デフォルト: `600` 秒）。 |
| `orchestrator.enable_dlq_retry` | Boolean | `send_failed.db` の自動再送を有効化（デフォルト: `true`）。 |
| `orchestrator.dlq_retry_interval_sec` | Integer | DLQ 定期自動再送間隔（秒、デフォルト: `600`、`0` で定期実行無効）。 |

その他の詳細パラメータは [config.toml.example](../config.toml.example) を参照してください。

## 3. ホットリロードと管理 API

常駐 `orchestrator.exe` は `config_watch_interval_sec` ごとに `config.toml` の変更を自動検知して反映します。手動確認・即時反映は以下の HTTP エンドポイントで行えます。

- **手動リロード**: `POST http://localhost:8080/reload`
- **現在設定の参照**: `GET http://localhost:8080/config`

## 4. `update.bat` と `fix.bat` の詳細仕様

### `update.bat`（バイナリ更新）
PowerShell 7、Go、既存の `.venv` がある環境で Python/Go のテストとビルドを実行し、`orchestrator.exe`・`single-orchestrator.exe`・`orchestrator/orchestrator.exe` を更新します。モデルや依存パッケージの再インストールは行いません。稼働中のプログラムを停止してから実行してください（旧バイナリは日時付き `.bak` に退避されます）。

```powershell
.\update.bat -CheckOnly  # テスト・ビルドのみ（配置しない）
.\update.bat             # バックアップ後に配置
```

### `fix.bat`（空 JSON レコード修復）
`config.toml` の DB を参照し、`features = '{}'` の既存行だけを対象に安全に再解析・修復します（`meta` や `predictions` のみが空の行は対象外）。保存時に ID・パス・曲番号・音源ハッシュ・空状態を二重確認し、汎用 DLQ へは流さず失敗時は停止します。

```powershell
.\fix.bat                                       # 対象を一覧表示（読取り専用）
.\fix.bat --id 89476                            # ID で絞り込み（複数指定可）
.\fix.bat --id 89476 --apply                    # 対象曲だけ再解析・FLACタグ更新・DB修復
.\fix.bat --file "M:\Music\album.flac" --apply  # 指定ファイル内の空レコードを修復
```
