# 🛠️ 治具スクリプト集 (`zig/` ユーティリティガイド)

本プロジェクト (`flac_analyzer_forwin`) では、パイプライン本体の実行・運用・データマイグレーション・検証・タグ修復を支援する各種独立治具スクリプトを `zig/` フォルダに集約・整備しておりますわ。

全治具はプロジェクトルートからの実行（`python zig/<script>.py`）および `zig/` フォルダ内からの直接実行（`python <script>.py`）の双方に完全対応しておりますの。

---

## 治具スクリプト一覧

| スクリプト | 概要・用途 | 主な引数・実行例 |
| :--- | :--- | :--- |
| [`repair_flac_tags.py`](file:///a:/Users/letwir/repo/flac_analyzer_forwin/zig/repair_flac_tags.py) | PostgreSQL DB 内の解析結果から FLAC 本体タグへの不足分一括焼き戻し・修復（重複排除＆Windowsタイムスタンプ保護） | `python zig/repair_flac_tags.py --dir M:\Music --batch-size 100` |
| [`migrate_hnr.py`](file:///a:/Users/letwir/repo/flac_analyzer_forwin/zig/migrate_hnr.py) | HNR (Harmonic-to-Noise Ratio) の旧 NAP 値 (0〜1) から dB スケール (`LIBROSA_HNR_DB` / `LIBROSA_NAP`) への一括変換・DBマイグレーション＆単体双方向計算 CLI | `python zig/migrate_hnr.py --calc-db 0.85`<br>`python zig/migrate_hnr.py --dry-run`<br>`python zig/migrate_hnr.py --fix-tags --batch-size 500` |
| [`retry_ingest.py`](file:///a:/Users/letwir/repo/flac_analyzer_forwin/zig/retry_ingest.py) | PostgreSQL 送信エラー時にローカル DLQ (`send_failed.db`) に退避された未送信レコードの自動再送・リカバリ | `python zig/retry_ingest.py --dlq-db send_failed.db` |
| [`fix_empty_meta.py`](file:///a:/Users/letwir/repo/flac_analyzer_forwin/zig/fix_empty_meta.py) | PostgreSQL 側の `meta` カラムが空/NULL のレコードを検出し、FLAC ファイル本体から VorbisComment を再抽出して更新 | `python zig/fix_empty_meta.py --dry-run`<br>`python zig/fix_empty_meta.py --batch-size 500` |
| [`inspect_track.py`](file:///a:/Users/letwir/repo/flac_analyzer_forwin/zig/inspect_track.py) | 単一 FLAC ファイルまたは CUE 付きアルバムのサンプル数・CUE スライス分割情報・VorbisComment タグを即座にインスペクト表示 | `python zig/inspect_track.py "testFLAC/01_08_Reply.flac"` |
| [`functor_precache.py`](file:///a:/Users/letwir/repo/flac_analyzer_forwin/zig/functor_precache.py) | Demucs が共有メモリ (SHM) に書き込んだ PCM 波形のアタッチ性・形状整合性を診断・高速検証する Functor ワーカー | `python zig/functor_precache.py --shm-metadata "{...}" --track-hash <hash>` |
| [`init_dl_model.py`](file:///a:/Users/letwir/repo/flac_analyzer_forwin/zig/init_dl_model.py) | Essentia ONNX/PB モデルの一括ダウンロード、TensorFlow `.pb` モデルから ONNX への自己変換、および Python 仮想環境のセットアップ | `python zig/init_dl_model.py` |
| [`update_hardware_specs.py`](file:///a:/Users/letwir/repo/flac_analyzer_forwin/zig/update_hardware_specs.py) | PowerShell/CIM を通じてマシンの CPU/RAM/GPU/OS/Pagefile を検知し、`HARDWARE_SPECS.md` を自動更新 | `python zig/update_hardware_specs.py` |
| [`verify_track4.py`](file:///a:/Users/letwir/repo/flac_analyzer_forwin/zig/verify_track4.py) | 単一 FLAC ファイル（特定トラック）のデコード、Demucs 分離、Librosa 特徴量抽出を単一プロセスで一気通貫実行して検証 | `python zig/verify_track4.py --track 4` |

---

## 各治具の詳細仕様

### 1. `repair_flac_tags.py`
- **目的**: データベース (`raw.library_flac`) に既に保存されている特徴量（Essentia 453モデル確率1000倍整数値、Librosa スカラー統計量、HNR_DB、NAP）を FLAC 本体の VorbisComment タグに安全に補完・書き戻します。
- **特徴**:
  - ファイル先行走査 (File-First Fast Scan) により、巨大な楽曲ライブラリでも高速にスキャン。
  - Mutagen タグ重複書き込み防止 ＆ Windows タイムスタンプ復元機能。

### 2. `migrate_hnr.py`
- **目的**: Issue #5 に基づく HNR の dB スケール化マイグレーション。
- **特徴**:
  - NAP (0.0〜1.0) と HNR (-40dB〜+40dB) の双方向完全可逆変換（Logit / Sigmoid 変換、誤差 \(< 10^{-6}\)）。
  - `--calc-db` / `--calc-nap` による単体計算確認機能。
  - `--dry-run` による事前影響範囲プレビュー。

### 3. `retry_ingest.py`
- **目的**: DB 接続障害などで `raw.library_flac` への UPSERT が失敗したペイロードを退避した SQLite (`send_failed.db`) から PostgreSQL へ安全に再送・リカバリ。
- **特徴**:
  - Go オーケストレーターの起動時および定期実行（`dlq_retry_interval_sec`、デフォルト10分）から自動呼び出し。
  - 再送成功したレコードのみを DLQ から自動削除。

### 4. `fix_empty_meta.py`
- **目的**: 過去にメタデータ抽出に失敗して DB 上で `meta` が空になっているレコードを検出し、FLAC ファイル本体のタグから再抽出して DB を更新。

### 5. `inspect_track.py`
- **目的**: CUE シート分割位置やサンプル数、主要タグの埋め込み状況をコマンドラインから一瞬で確認・デバッグ。
