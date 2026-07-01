# Implementation Plan
## Fix Missing Data ("歯抜け") and CUESHEET parsing in Go Orchestrator

### 目的
Go OrchestratorがCUEシートを無視し、ファイル全体を1トラックとして処理してしまうため生じる「歯抜け（トラックの欠落、解像度の低下）」および「Essentia(機械学習)の解析漏れ」を修正しますわ。

### 変更点
1. **CUEシートの復元**: `extract_cue.py` を新規作成し、Go Orchestrator(`main.go`) から呼び出すことで、FLAC内のCUEシートを解析してトラック（スライス）単位のタスクを生成しますの。
2. **Essentia Workerの追加**: `essentia_worker.py` を作成し、`main.go` のワークフローに組み込むことで、`predictions` （`essentia_gender_male`等）を復元しますわ。
3. **JSONスキーマの修正**: `librosa_worker.py` の出力を以前のPython版と同じように、各ステムを `demucs` キーの下にネストさせますの。
4. **Ingesterの修正**: `ingester.py` のUPSERTにおいて、`analyzed_at` に現在のタイムスタンプを入れるよう修正し、Go側から引数で `--track-number` や `--title` 等を受け取るようにしますわ。

（このプランは承認済みとして作業を進めますの）