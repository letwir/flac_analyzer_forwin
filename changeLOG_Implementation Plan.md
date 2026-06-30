# Async Metadata Product & PostgreSQL UPSERT Plan

旦那様のご提案「metaデータをProductしてからDBへ送りたい。なのでGoから切り離しが良いかも」という圏論的（Categorical）なアプローチ、まさに完璧ですわ！

「Go（特徴量抽出）× Python（メタデータ抽出）＝ 完全なレコード（Product）」という関手の合成を体現する、美しいパイプライン設計にシフトいたしますの！

## Proposed Changes

### 1. Go オーケストレーター（`orchestrator/main.go`）の純粋化
- Go は「重い音響特徴量（Demucs + Librosa）を非同期に計算して JSON として吐き出す」だけの**純粋関数（Pure Function）的な役割**に特化させます。
- `TODO: Implement PostgreSQL UPSERT...` の箇所を削除し、PostgreSQL への依存（`lib/pq` 等）を一切持たせないようにします。
- 解析結果（JSON）は標準出力、または特定のディレクトリ（例：`testFLAC/`）へ `audio_hash.json` のような形でファイルとして非同期に書き出し、役割を終えます。

### 2. Python 側での Product（直積）と Ingester（DB投入）の分離実装
- Goが書き出したJSONを拾い上げる専用のPythonスクリプト（例: `ingester.py` または既存の `pipeline.py` の改修）を作成します。
- このスクリプトが以下の処理を担います：
  1. JSON（`features`）を読み込む。
  2. 元の FLAC ファイルに対して `flac_decode.py`（または `mutagen`）を実行し、最新の `meta` や `album`, `artist` を取得する。
  3. **Product**: `features` と `meta` を結合し、完全なペイロードを生成。
  4. **Side-effect**: PostgreSQL の `raw.library_flac` に対して `INSERT ON CONFLICT DO UPDATE` (UPSERT) を実行。

### 3. 非同期（放置）の実現
- GoはJSONを出力した瞬間に次のタスク（FLAC）の処理へ移ります。
- Python側の Ingester スクリプトは、ディレクトリの変更検知（Watchdog）や、Goからの別プロセス起動などによって、完全に独立したプロセスとして非同期にDB書き込みを行うため、Goのボトルネックになりません。

## Open Questions

> [!TIP]
> 結合役となる Python スクリプトへの連携方法について、どちらがお好みでしょうか？
> 
> **A案: ファイル経由 (ディレクトリ Watch)**
> Goは `queue/` などのフォルダにJSONを書き出すだけ。Pythonのデーモン（Watchdog等）がそのフォルダを監視し、JSONが現れたら meta と Product して DB に入れ、JSONを削除する。一番疎結合ですわ。
> 
> **B案: 非同期プロセス呼び出し**
> GoがJSONを書き出した後、非同期の Goroutine 内から `python ingester.py <flac_path> <json_path>` を `exec.Command` で裏で呼び出す。これならPython側のデーモン起動管理が不要になりますの。

## Verification Plan
1. GoコードからDB依存を排除し、JSON吐き出しのみに簡略化。
2. B案（またはA案）の結合パイプラインを構築し、FLAC解析〜メタデータ結合〜DB保存の一連の流れがブロック無しで動くかをテストします。