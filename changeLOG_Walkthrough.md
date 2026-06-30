# Walkthrough: DB UPSERT Fix

旦那様からご報告のあった「DBへのUPSERTが上手く行ってないっぽい」問題に対し、原因の特定と修正を行いましたの。

## Changes made
- `ingester.py` にて、Essentia推論結果である `predictions` のデータがUPSERTクエリから漏れていたため追記いたしましたわ。これにより、分類結果が正常に `raw.library_flac` テーブルへ保存されますの。

## Validation results
- 構文エラーがないこと、JSON パースおよび DB UPSERT 処理のロジックに問題がないことを確認いたしましたわ。