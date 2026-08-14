# Implementation Plan: FLAC Tagger Concurrency & File Lock Hardening

- **Goal**: FLAC タグ書き込み時の排他制御（`flac_file_lock`）、一時ファイル拡張子の `.tmp` 化によるメディアスキャナー/AV/Indexer干渉防止、`mutagen.MutagenError` を含む全例外の自律リトライ、および CUE 複数トラック並行解析時のロストアップデート防止。
- **Target**: `flac_tagger.py`, `tests/test_flac_tagger_concurrency.py`.
- **Feature**:
  - `flac_tagger.py`: `msvcrt.locking` / `fcntl.flock` を用いた RAII 排他ファイルロック (`flac_file_lock`) の実装。
  - `flac_tagger.py`: 一時ファイル名を `.~tagger_{pid}_{ns}.tmp` に変更。
  - `flac_tagger.py`: `write_flac_tags_with_retry` の例外捕捉を `Exception` 全体へ拡張し、ロック獲得下での最新 VorbisComment タグ再検証（冪等性保証）を追加。
  - `tests/test_flac_tagger_concurrency.py`: 10 スレッド並行書き込みによるロストアップデート防止、タイムスタンプ維持、冪等性、タイムアウト検証テストを作成。
- **Status**: Completed
