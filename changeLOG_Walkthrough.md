# Walkthrough
## Fix Missing Data ("歯抜け") and CUESHEET parsing in Go Orchestrator

### 作業概要
Go Orchestrator（新パイプライン）で発生していた、CUEシートが無視されてトラックが1つしか登録されない問題、およびEssentia(ML解析)が欠落する問題を修正いたしましたわ。

### 変更点
1. **`extract_cue.py` の追加**:
   * `flac_decode.py` のロジックを用いてCUEシートを解析し、JSONとしてトラック一覧を出力するラッパースクリプトを作成しました。
2. **`orchestrator/main.go` のリファクタリング**:
   * APIの`/task`エンドポイントで `extract_cue.py` を呼び出し、得られたトラックごとにTaskPayloadを分割キューイングするように変更しました。
   * Librosaの後に `essentia_worker.py` を呼び出すように変更しました。
   * `ingester.py` へ `--track-number`, `--title`, `--artist` を渡すように変更しました。
   * 修正後、正常に `go build` が通ることを確認いたしました。
3. **`demucs_worker.py` の修正**:
   * `--start-sample` と `--end-sample` オプションを追加し、`flac_decode.process_slice_with_seq_safety` を用いて当該トラックの範囲のみを読み込むように修正しました。
4. **`essentia_worker.py` の追加**:
   * 共有メモリから `mix` 音源を読み込み、`GLOBAL_ESSENTIA.run_all` を実行して `predictions` を出力する専用ワーカーを追加しました。
5. **JSONスキーマ (`librosa_worker.py`) の修正**:
   * `demucs` 由来のステム特徴量（bass, drumsなど）を、旧パイプライン同様に `"demucs"` キーの直下にネストするように修正しました。
6. **`ingester.py` の修正**:
   * Orchestratorから渡された `--track-number`, `--title`, `--artist` を優先して用いるよう修正しました。
   * `--predictions-json-path` を追加し、Essentiaの出力を読み込んでマージするようにしました。
   * UPSERT SQL文に `analyzed_at = CURRENT_TIMESTAMP` を追加しました。

### 確認事項
- `test2.py` / `test3.py` で、DB側にすべての特徴量（`features` に `demucs`）と `predictions` が正しく格納されるためのスキーマ準備が整っていることを確認しました。
- Goビルド (`go build`) は正常に完了しておりますわ。