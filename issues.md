# ISSUE

## RAM / VRAM 配置・CPU/GPU 並行解析・DB 分岐ロードマップ (2026-09-12)

### Phase 1 — 資源モデルと配置判断の確立 (`goal:resource-model`)

中目標: 専用 VRAM、Windows 共有 GPU メモリ、物理 RAM、Disk mmap を別資源として計測し、暗黙の WDDM 共有メモリ退避を起こさない決定論的な波形配置計画を作る。

- [x] **P1-I1 【Spec】波形配置契約 `DedicatedVRAM | HostRAM | DiskMmap` を定義する**
  - 単一タスク時は GPU 処理用 working set のみ専用 VRAM を優先し、Librosa / Essentia が必要とする CPU 側波形の所有権を明示する。
  - 並列タスク存在時はマスター波形を HostRAM または DiskMmap に置き、GPU へは有界チャンクだけを転送する。
  - 「低負荷」「並列タスクあり」「安全余裕」の判定値とヒステリシスを設定可能にする。
  - Acceptance: 同一入力・同一資源スナップショットから常に同じ配置計画が得られる純粋関数テストを作る。
- [x] **P1-I2 【Fix/Observability】専用 VRAM と共有 GPU メモリの計測を分離・検証する**
  - `DedicatedUsedBytes` / `DedicatedTotalBytes` / `SharedUsedBytes` / `AvailableVramBytes` の取得元、単位、集約規則を実機 RTX 5070 Ti と照合する。
  - WMI `Win32_VideoController.AdapterRAM` の誤報・32-bit 上限を前提にせず、取得不能時は未知値として fail-safe に扱う。
  - 信頼できる取得経路が確立するまで、明示設定した専用VRAM総量を検証付きで使える暫定overrideを用意する。
  - Acceptance: Task Manager または一次取得元との許容差を定め、専用 16 GB と共有領域を混同しない実機スモークテストを通す。
- [x] **P1-I3 【Fix】Gatekeeper の二重 RAM 控除と判定・予約間競合を除去する**
  - `AvailPhys` に既反映の消費と `activeInFlightRamBytes` の二重控除を整理し、観測値と予約値の意味を分離する。
  - Gatekeeper 判定と資源予約を単一の原子的操作にし、実行直前の StorageMode 再計算を廃止する。
  - Acceptance: 複数 goroutine の同時 admission で予約上限を超えず、解放後に予約量が必ずゼロへ戻る race / failure-path テストを通す。

**Phase 1 Done:** 配置判断・RAM予約・VRAM予約が純粋計画と原子的予約に分かれ、共有 GPU メモリ増加を専用 VRAM 空きとして誤認しない。

### Phase 2 — NOGO 停滞と資源食い合いの解消 (`goal:admission-control`)

中目標: Worker がタスクを抱えてsleepする現行方式を中央 Admission Controller へ置き換え、資源待ちがCPU/GPUスロットを占有しないようにする。

- [x] **P2-I1 【Arch】中央 Admission Controller と資源Leaseを実装する**
  - キュー上で `Plan -> AtomicReserve -> Dispatch -> Release` を実行し、通らないタスクは Worker を占有せず待機させる。
  - Lease に HostRAM、DedicatedVRAM、CPU lane、GPU lane、Demucs、Disk一時容量を含める。
  - キャンセル、timeout、panic相当エラー、部分失敗の全経路で冪等に解放する。
  - Acceptance: 4〜8並列投入時に全WorkerがNOGO sleepへ入らず、小タスクが進行できる統合テストを通す。
- [x] **P2-I2 【Fix】RAM待機より前に取得しているDemucs/GPUスロットを解放する**
  - SHM容量確認・資源Lease確定後にのみ Demucs 実行スロットを取得する。
  - 未使用の `tensorSemaphore` と `demucsSemaphore` は対応するLeaseへ統合するか削除し、文書と実装を一致させる。
  - Acceptance: RAM不足中の Demucs in-use と GPU lane in-use が0であり、資源回復後に処理が自動再開する。
- [x] **P2-I3 【Fix】`MemoryLoad >= 90%` の永久NOGOを回復可能な状態遷移へ変更する**
  - 一律停止ではなく、緊急回収、DiskMmapへの降格、常駐キャッシュ縮退、再開ヒステリシスを定義する。
  - 同一理由のログを集約し、park/requeueの世代と次回評価時刻を記録する。
  - Acceptance: 90%到達後に82%等の再開閾値まで戻ると人手なしで再開し、同一タスクの重複claimが発生しない。
- [x] **P2-I4 【Fairness】大タスク飢餓を防ぐサイズ別キューとagingを導入する**
  - 小タスク先行を許しつつ、大タスクが永久に選ばれない状態を防ぐ。
  - Acceptance: 混合サイズ負荷でsmall queueが進行し、大タスクも規定時間またはaging閾値内にadmitされる決定論的テストを通す。

**Phase 2 Done:** RAM圧迫中にも実行可能な仕事が進み、待機タスクがCPU/GPU/Demucsスロットを保持せず、資源回復後に自動復帰する。

### Phase 3 — CPU / GPU を同時活用する有界パイプライン (`goal:cpu-gpu-pipeline`)

中目標: 現行のステム単位 `Librosa -> Tensor -> Essentia` 直列処理を、ピークメモリを制御した CPU lane / GPU lane の並行処理へ変更する。

- [x] **P3-I1 【Feasibility/Contract】Demucs出力の公開粒度とreadyプロトコルを確定する**
  - 現行Demucs APIは全source推論完了後に `StemContext` を返すため、「推論中に完成stemを順次公開」と「推論完了後にSHMへ転記済みstemから公開」を区別する。
  - per-stem `Ready -> Frozen -> Consuming -> Released` 状態、generation ID、参照カウント、エラー時の全体cancelを定義する。
  - 真の推論中streamingがモデル出力契約上できない場合は、転記・分析のwavefront並行だけを採用し、推論器の改造をPhase要件にしない。
  - Acceptance: fake producerでstemの順不同ready、重複ready、途中失敗、cancelを再現し、未完成波形をreaderが開けないことを検証する。
- [x] **P3-I2 【Arch】特徴抽出をCPU laneとGPU laneへ分離しJoinする**
  - CPU lane は Librosa / Essentia、GPU lane は Tensor 特徴を担当し、結果をトラック単位で結合する。
  - laneごとにcontext、timeout、最大並列数、所有する波形参照を明示する。
  - Acceptance: 片laneの失敗で他laneをcancelし、SHM/mmap/CUDA参照とLeaseが残らないfailure-pathテストを通す。
- [x] **P3-I3 【Memory】ready済みステムへ分析器が取り付くbounded wavefront pipelineを実装する**
  - producerがstem単位の書込みとfreezeを完了した時点でready queueへ公開し、全stem完了を待たずCPU/GPU laneが処理を開始する。
  - 無制限に分析器をspawnせず、Admission ControllerのLease、lane semaphore、参照カウントで同時処理数を制限する。
  - 全7ステムを同時に専用VRAMへ常駐させず、設定可能な上限内で転送・解析・解放する。
  - CUDA OOMは共有GPUメモリへの暗黙退避に依存せず、チャンク縮小またはHostRAM/DiskMmapへ明示降格する。
  - Acceptance: stem 1本ready後、残りstem未公開のまま対応laneが開始すること、および長尺トラックでRAM/VRAM安全域を守ることを検証する。
- [x] **P3-I4 【Pool】CPU WorkerとGPU Workerの常駐数を分離する**
  - CUDA context / GPU model を持つdaemonは原則1系統とし、CPU daemon数はCPU・RAM予算から独立決定する。
  - プロセスrecycle前後のHostRAM、専用VRAM、共有GPUメモリを計測する。
  - Acceptance: idle時に複数daemonが専用VRAM・共有GPUメモリを重複確保せず、100タスク等の連続実行後も使用量が単調増加しない。
- [x] **P3-I5 【Perf】CPU/GPU稼働率の受入基準とベンチマークを定義する**
  - 「フルロード」を瞬間100%ではなく、十分なキューがある測定窓の平均利用率・throughput・p95待機時間で定義する。
  - Acceptance: 代表的な単曲、CUEアルバム、長尺、4〜8並列のベンチ結果を変更前後で比較し、OOMゼロとthroughput非劣化を確認する。

**Phase 3 Done:** 十分なキューがある間、CPU laneとGPU laneが重なって進行し、設定したRAM/VRAM上限とクリーンアップ契約を守る。

### Phase 4 — PostgreSQL `(filepath, track_number)` とmix成果による解析分岐 (`goal:analysis-routing`)

中目標: CUE展開後の各トラックを波形生成前にPostgreSQLと照合し、DBだけで決められない対象のみraw mixを一度デコードして、保存済み成果の完全性に応じて `Full | MixOnly | StemsOnly | Skip` を決定する。

- [x] **P4-I1 【Spec】`AnalysisDecision` とmix有効性契約を確定する**
  - 候補を `FullAnalysis | MixOnly | StemsOnly | Skip` とし、必要な `features` / `predictions` / schema-version / analyzed_at 条件を定義する。
  - 第1段は `(filepath, track_number)` と保存済みmix/stem成果だけで判断し、この段階で `Skip` できる対象はデコードしない。
  - 第2段が必要な対象だけraw mixを一度デコードする。mixは分離結果ではなく元の混合波形であり、軽量mix解析結果からDemucs要否を判定する場合は特徴量・閾値・偽陰性時の扱いを明文化する。
  - Acceptance: DB行の不存在、mix欠落、stem欠落、完全、旧schema、不正JSONの期待判断を表形式テストにする。
- [x] **P4-I2 【DB】正規化 `(filepath, track_number)` のバッチ照合を汎用化する**
  - 既存 `-Unreg` の正規化規則を再利用し、ドライブ文字、大小文字、区切り、UNC、Track 1 / NULL方針を一箇所に集約する。
  - CUE展開後の入力キーをバッチ照会し、N+1 queryを避ける。
  - Acceptance: SQLiteのみ、PostgreSQLのみ、双方、双方なしの4象限と、CUE一部登録ケースを通す。
- [x] **P4-I3 【Routing】DB判断を実行計画へ接続し、mix/stem解析を選択実行する**
  - DB preflightは重いdecode/Demucs/SHM確保より前に行う。
  - 新規mix判定が必要な場合は `DB preflight -> raw mix decode -> bounded mix analysis -> Demucs decision` の順序を固定する。
  - `Skip` は資源Leaseを取得せず、`MixOnly` / `StemsOnly` は不要なモデル・波形領域を確保しない。
  - 既存の `audio_hash` 重複判定は移動・改名・別名コピー向け第二段安全網として保持する。
  - Acceptance: 各decisionで起動したstageと確保資源をspy/fakeで検証し、不要stageが0回であることを確認する。
- [x] **P4-I4 【Failure】DB障害・競合更新・再実行をfail-closedかつ冪等にする**
  - DB接続失敗を未登録扱いにせず、解析開始前に明示失敗またはretryへ送る。
  - preflight後の同時更新に備え、ingest時の再検証またはversion条件を定義する。
  - Acceptance: timeout、接続断、同時UPSERT、キャンセル後再実行で重複解析・成果欠落・誤Skipが発生しない。

**Phase 4 Done:** PostgreSQLの正規化パス＋トラック番号とmix/stem成果の完全性から、各トラックの最小解析経路をfail-closedに選べる。

### 全Phase完了の受入境界

- [ ] 代表負荷でCPU/GPUが並行して進み、処理件数が継続的に増加する。
- [ ] 専用VRAM、共有GPUメモリ、物理RAM、Disk使用量が別々に観測・制限される。
- [ ] 90% RAMのNOGO状態から資源回復後に自動再開し、park/requeueループや重複claimがない。
- [ ] タスク成功・失敗・timeout・cancelの全経路で資源LeaseとOSハンドルが解放される。
- [ ] PostgreSQL照合により不要なmix/Demucs/stem解析を起動しない。
- [ ] 単体・race・統合テストに加え、RTX 5070 Ti実機で境界スモークテストと変更前後ベンチを完了する。

---

- [x]DONE 【Fix/Architecture】 長尺トラック・高負荷時の特徴量抽出タイムアウト解決および適応的タイムアウト（Adaptive Timeout）導入（5分以上の楽曲および55分トークトラック等の 90秒デッドライン超過 FAILED を完全根絶）
- [x]DONE 【Feature】 物理 RAM 安全圏超過時の SSD/TMP ディスク退避モード (Disk Mode Fallback) 実装（ASMR / 長尺トラック 104GB 見積もり時の Gatekeeper NOGO 永久ブロック根絶 ＆ 7ステム維持）
- [x]DONE 【Fix】 flac_decode.py の flac CLI 範囲デコード例外 (rc=1) 修正（-F / --silent / proc.communicate / 指数バックオフリトライ導入）
- [x]DONE 【Fix】 ingester.py の stdout ログ混入による Orchestrator の Pre-Hash Duplicate Check (mixハッシュチェック) スキップ失敗バグの修正
- [x]DONE 【Fix】 PostgreSQL 直接書き込みのタイムアウト欠如による無限ハング修正 ＆ DB Ingestion の独立非同期ワーカー化 (db_timeout_sec = 20)
- [x]DONE 【Tuning】 PostgreSQL 側の GIN インデックスおよび UPSERT チューニング（優先度：低）
- [x]DONE 【Verify】 実機 CUDA / GPU 実行環境における ONNX 推論および PyTorch の動作確認とパフォーマンス検証
- [x]DONE 【Docs】 requirements.txt に記載された依存バージョンの整合性解消（PyTorchのONNX統一のドキュメント不一致修正）

- [-]WIP 【Investigate/Orchestrator】 タスク停滞の律速箇所を特定（2026-08-22）
  - 観測: pprof/metrics/HTTP は応答するが、`PENDING=5499`、`RUNNING=1` が長時間不変。CPU/GPU は低負荷、メモリ使用率は約83〜86%、ディスクI/Oは継続。
  - 観測: 設定上のワーカー数は1、Demucs同時実行数は1。タスク/ファイル処理時間、GPU待ち、Demucs/Tensor待ちメトリクスは実質記録なし。
  - pprof確認: `FetchGpuMetricsComplex`（PowerShell/CIM経由のGPU監視）が10秒CPU profileの約100%を占有し、`runtime.cgocall`・プロセスI/O・`WaitForSingleObject` が支配的。GPU監視デーモンの観測負荷は確認済み。
  - 追加ログ観測: `DemucsDaemon-1` が 14:00:38 に HTDemucs ONNX 推論を開始し、14:12:39 に分離完了。`ONNX_LOCK` 同期区間を含む約12分の長時間処理で、単一のDemucsスロット（`DemucsConcurrentLimit=1`）を占有していた可能性が高い。
  - 追加ログ観測: DLQ retry は 14:09:45 以降10分周期で毎回正常起動し、「DLQは空」と終了。DLQ scheduler / retry_ingest は律速ではない。
  - 追加ログ観測: 15:33:47 に Track 9 が `daemon ExtractAll context cancelled: context deadline exceeded` でFAILED。15:33:48 には `GPU Utilization too high (100.0% >= 85.0%)` によりGatekeeperが60秒 dispatch delayへ移行。
  - 判断更新: 完全ハングではなく、長時間のHTDemucs/ExtractAll処理が5分系コンテキスト期限を超過し、その直後にGPU飽和防御が次タスクを抑制する流れを確認。主律速は `worker daemon extraction` とGPU飽和（`ONNX_LOCK`/Demucs単一スロット）で、GPU監視PowerShell/CIM負荷は副次的な観測オーバーヘッド。
  - 次の確認: `ExtractAll` の実処理時間・入力トラック長・GPU飽和時間を突合し、`FeatureExtractTimeoutSec=300` とGPU Gatekeeper 60秒遅延の妥当性を評価する。

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
