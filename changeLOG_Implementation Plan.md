# DB UPSERT Fix

`ingester.py` の `INSERT ... ON CONFLICT DO UPDATE` において、`predictions` カラムが意図せず除外されていた不具合を修正しますわ。

## Proposed Changes
### ingester.py
#### [MODIFY] [ingester.py](file:///a:/Users/letwir/repo/flac_analyzer_forwin/ingester.py)
- JSONからの `predictions` キー抽出処理を追加
- PostgreSQL への INSERT および DO UPDATE クエリに `predictions` カラムを追加し、EXCLUDED で上書きするように修正