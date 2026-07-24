# ISSUE

- [x]DONE 【Fix】 ingester.py の stdout ログ混入による Orchestrator の Pre-Hash Duplicate Check (mixハッシュチェック) スキップ失敗バグの修正
- [ ] 【Tuning】 PostgreSQL 側の GIN インデックスおよび UPSERT チューニング（優先度：低）
- [ ] 【Verify】 実機 CUDA / GPU 実行環境における ONNX 推論および PyTorch の動作確認とパフォーマンス検証
- [ ] 【Docs】 requirements.txt に記載された依存バージョンの整合性解消（PyTorchのONNX統一のドキュメント不一致修正）

## 状態遷移・README修正 (4会話ロードマップ)
- [x]DONE #1 【Feature】 DLQ退避時(exit code 2): 10分後 retry_ingest.py 自動実行、再失敗時はDLQ保持のまま FAILED 設定するロジック実装
- [ ] #2 【Docs】 README.md: functor_precache.py の実態（npy保存廃止、SHMアタッチ検証のみ）を反映
- [x]DONE #3 【Docs】 README.md: Mermaid図に中間JSONファイル書き込みステップ (WriteJSONFiles) を追加
- [x]DONE #4 【Docs】 README.md: Mermaid図のハッシュ確認を2段階 (worker_demucs -> ingester DB照合) に修正
- [x]DONE #5 【Fix】 Go: CUEインスペクト失敗/0トラック時、単一トラックフォールバックせず即 FAILED で終了するよう修正
- [x]DONE #6 【Docs】 README.md: Mermaid図に起動時ゾンビタスク (RUNNING/PENDING -> FAILED) リセットを追加
- [x]DONE #7 【Docs】 README.md: Mermaid図のクリーンアップ処理の位置・分散構造を修正
- [ ] #8 【Docs】 README.md: USAGE直下に config.toml 解説 (skip_dup_by_hash / force:true 挙動含む) を追加
- [ ] #9 【Docs】 README.md: 末尾に Windows 共有メモリ (SHM) の詳細仕様・割り当てセクションを追加
- [x]DONE #10 【Docs】 README.md: Mermaid図に FLAC タグ書き戻し + Windows タイムスタンプ保護のステップを追加


