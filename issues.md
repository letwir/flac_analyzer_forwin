# ISSUE

- [x]DONE 【Fix】 flac_decode.py の flac CLI 範囲デコード例外 (rc=1) 修正（-F / --silent / proc.communicate / 指数バックオフリトライ導入）
- [x]DONE 【Fix】 ingester.py の stdout ログ混入による Orchestrator の Pre-Hash Duplicate Check (mixハッシュチェック) スキップ失敗バグの修正
- [x]DONE 【Tuning】 PostgreSQL 側の GIN インデックスおよび UPSERT チューニング（優先度：低）
- [x]DONE 【Verify】 実機 CUDA / GPU 実行環境における ONNX 推論および PyTorch の動作確認とパフォーマンス検証
- [x]DONE 【Docs】 requirements.txt に記載された依存バージョンの整合性解消（PyTorchのONNX統一のドキュメント不一致修正）

## 状態遷移・README修正 (4会話ロードマップ)
- [x]DONE #1 【Feature】 DLQ退避時(exit code 2): 10分後 retry_ingest.py 自動実行、再失敗時はDLQ保持のまま FAILED 設定するロジック実装
- [x]DONE #2 【Docs】 README.md: functor_precache.py の実態（npy保存廃止、SHMアタッチ検証のみ）を反映
- [x]DONE #3 【Docs】 README.md: Mermaid図に中間JSONファイル書き込みステップ (WriteJSONFiles) を追加
- [x]DONE #4 【Docs】 README.md: Mermaid図のハッシュ確認を2段階 (worker_demucs -> ingester DB照合) に修正
- [x]DONE #5 【Fix】 Go: CUEインスペクト失敗/0トラック時、単一トラックフォールバックせず即 FAILED で終了するよう修正
- [x]DONE #6 【Docs】 README.md: Mermaid図に起動時ゾンビタスク (RUNNING/PENDING -> FAILED) リセットを追加
- [x]DONE #7 【Docs】 README.md: Mermaid図のクリーンアップ処理の位置・分散構造を修正
- [x]DONE #8 【Docs】 README.md: USAGE直下に config.toml 解説 (skip_dup_by_hash / force:true 挙動含む) を追加
- [x]DONE #9 【Docs】 README.md: 末尾に Windows 共有メモリ (SHM) の詳細仕様・割り当てセクションを追加
- [x]DONE #10 【Docs】 README.md: Mermaid図に FLAC タグ書き戻し + Windows タイムスタンプ保護のステップを追加

## 課題・仕様検討 (完了済み)
- [x]DONE 【Feature】 Win32 Job Object 導入による Chrome 風プロセスグループ化 ＆ 自動一括クリーンアップ
- [x]DONE 【Fix/Memory】 テンソル形状保持 ＆ config.toml可変キュー絞り・バックオフリトライによるメモリ保護

---

## 🎯 中期目標・小目標 Issues (GitHub Issues)

### 1. メモリ安定化・コミットチャージ最適化 (`goal:memory`)
- [x]DONE [#2 spectral_bandwidth float64 抹殺 & FLACデコードインプレース化 ＋ config.toml 反映](https://github.com/letwir/flac_analyzer_forwin/issues/2)
- [x]DONE [#3 [Feat] Go SHM Arena Pool による事前確保・再利用でメモリ断片化を根絶](https://github.com/letwir/flac_analyzer_forwin/issues/3)
- [x]DONE [#4 [Feat] VirtualLock / SetProcessWorkingSetSizeEx 完全実装（物理RAM固着化）](https://github.com/letwir/flac_analyzer_forwin/issues/4)

### 2. 音響特徴量の品質・正確性向上 (`goal:features`)
- [x]DONE [#7 [Verify] Blackwell GPU (requirements-blackwell.txt) での Essentia/ONNX 動作検証](https://github.com/letwir/flac_analyzer_forwin/issues/7)
- [x]DONE [#5 [Spec] HNR を dB スケールへ変換・LIBROSA_NAP / LIBROSA_HNR_DB タグ分離](https://github.com/letwir/flac_analyzer_forwin/issues/5)
- [x]CLOSED [#6 [Spec] Guitar / Piano ステムの特徴量抽出対応方針を決定 (予定なしのためクローズ)](https://github.com/letwir/flac_analyzer_forwin/issues/6)

### 3. パイプライン堅牢化・ETL改善 (`goal:pipeline`)
- [x]DONE [#8 [Test] repair_flac_tags / flac_tagger: CUE付き複数トラックの重複書き込みリグレッションテスト整備](https://github.com/letwir/flac_analyzer_forwin/issues/8)
- [x]DONE [#9 [Test] Gatekeeper EffectiveAvail 判定の自動化テスト整備 ＆ 20秒リトライ制御](https://github.com/letwir/flac_analyzer_forwin/issues/9)
- [x]DONE [#10 [Feat] DLQ retry_ingest.py の orchestrator 起動時自動実行・定期実行化](https://github.com/letwir/flac_analyzer_forwin/issues/10)

### 4. コード品質・テスト整備 (`goal:quality`)
- [x]DONE [#12 [Quality] pytest カバレッジ計測とレポート出力設定](https://github.com/letwir/flac_analyzer_forwin/issues/12)
- [x]DONE [#22 [Quality] ADV-A1: Ping() における json.Marshal エラーハンドリング (¬SilentSwallow 準拠)](https://github.com/letwir/flac_analyzer_forwin/issues/22)
- [x]CLOSED [#11 [CI] test_integration.py および単体テストを GitHub Actions に組み込む (対象外のためクローズ)](https://github.com/letwir/flac_analyzer_forwin/issues/11)

### 5. ドキュメント整備 (`goal:docs`)
- [x]DONE [#13 [Docs] 治具スクリプト (zig/*.py) の独立集約 ＆ ドキュメント化 (docs/utility_tools.md)](https://github.com/letwir/flac_analyzer_forwin/issues/13)
- [x]DONE [#14 [Docs] docs/ フォルダを flac_tagger / Gatekeeper / JobObject / ShmArenaPool 修正に合わせて最新化](https://github.com/letwir/flac_analyzer_forwin/issues/14)

### 6. ストレージ防護・リソース管理 (`goal:storage`)
- [x]DONE [#17 [Feat] ディスク容量防護（Gatekeeper min_avail_disk_gb ＆ 中間JSON/キャッシュ自動GC ＆ Tagger空き容量事前検証）](https://github.com/letwir/flac_analyzer_forwin/issues/17)

### 7. データ整合性・運用保守 (`goal:consistency`)
- [x]DONE [#15 [Feat] DB ⇔ FLAC タグの双方向整合性チェッカー＆一括修復スクリプト](https://github.com/letwir/flac_analyzer_forwin/issues/15)

### 8. 可視化・モニタリング (`goal:observability`)
- [x]DONE [#16 [Feat] CLI リアルタイム進捗ダッシュボード（処理速度/残り時間/ディスク残量/ワーカー稼働状況）](https://github.com/letwir/flac_analyzer_forwin/issues/16)

### 9. 音響解析パイプラインのプラグインアーキテクチャ化 & 密結合分離 (`goal:pipeline`)
- [ ] [#18 [Arch] 音響解析パイプラインのプラグインアーキテクチャ化 & analyze-pre/ 分離 & 不要スクリプト整理](https://github.com/letwir/flac_analyzer_forwin/issues/18)
  - [x] 1-1. 旧世代スクリプト (`worker_analyzer.py`, `functor_precache.py` 等) の整理・廃止とGo連携ワーカーへの一本化
  - [ ] 1-2. `analyze-pre/` ディレクトリ新設（Demucs分離後SHM・Pre-warm密結合レイヤーの隔離）
  - [ ] 1-3. `analyzer/` の純粋関数プラグイン化（`librosa_[なにやるの].py` / `scipy_stats.py` 等への分割・命名統一）
  - [ ] 1-4. プラグイン自己登録と動的ディスパッチ基盤（`registry_plugins.py` 「あるものを回す」機構）の実装
  - [ ] 1-5. 既存テストおよび Go オーケストレーター連携の動作確認

### 10. 新規音響解析モジュールの導入 (`goal:features`)
- [ ] [#19 [Feat] 新規音響解析モジュールの導入 (心理音響・楽曲構造・ボーカル声質・偽ハイレゾ/TruePeak)](https://github.com/letwir/flac_analyzer_forwin/issues/19)
  - [ ] 2-1. 心理音響指標プラグイン `analyzer/psychoacoustics_din45692.py` (Sharpness DIN 45692, Roughness, Tonality) の実装
  - [ ] 2-2. 楽曲構造・サビ/ドロップ検出プラグイン `analyzer/structure_ssm.py` (SSM, Chorus, Drop, Complexity) の実装
  - [ ] 2-3. ボーカル声質指標プラグイン `analyzer/voice_cpp.py` (CPP / CPPS ケプストラム突出度, Breathiness) の実装
  - [ ] 2-4. オーディオ品質検証プラグイン `analyzer/audio_cutoff_lufs.py` (偽ハイレゾ遮断周波数, 4x True Peak, EBU R128 LUFS/LRA) の実装
  - [ ] 2-5. 将来の音楽基盤モデル (CLAP/MERT) 連携用プラグインスロットの設計
  - [ ] 2-6. 新モジュール群の単体テスト（`tests/test_*.py`）整備

### 11. 外だし設定 analyzer.toml 自動生成・エディタ連携・安全弁 (`goal:pipeline`)
- [ ] [#20 [Feat] 外だし設定 analyzer.toml 自動生成・エディタ連携・安全弁 (execute=false)](https://github.com/letwir/flac_analyzer_forwin/issues/20)
  - [ ] 3-1. プラグイン定義からの `analyzer.toml` 自動生成機構の実装
  - [ ] 3-2. 誤実行防止のための安全弁 (`execute = false` 初期値ガード) の導入
  - [ ] 3-3. 設定ファイル（`config.toml` の `editor = "notepad"` / `"sakura"`）に基づくエディタ自動起動機能の実装
  - [ ] 3-4. `execute = true` 確認後のパイプライン実行テスト

### 12. Go非介在・治具マイグレーションスクリプトの実装 (`goal:pipeline`)
- [ ] [#21 [Tool] Go非介在・治具マイグレーションスクリプト (zig/migrate_features.py) の実装](https://github.com/letwir/flac_analyzer_forwin/issues/21)
  - [ ] 4-1. `--migrate-features` による既存 DB (`raw.library_flac.features`) への JSONB 差分マージ (`||`) 機構の実装
  - [ ] 4-2. `--from-audio` による FLAC 直接デコード・新特徴量高速バッチ抽出モードの実装（Demucs再実行バイパス）
  - [ ] 4-3. `--dry-run`, `--fix-tags`, `--batch-size` 等の運用管理オプションおよび単体テスト整備
  - [ ] 4-4. 実DBおよびテスト音源に対するマイグレーション動作検証


