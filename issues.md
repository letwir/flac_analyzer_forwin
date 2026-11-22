# ISSUE

- [x]DONE 【Fix/VRAM】 ローカライズされた `Microsoft 基本ディスプレイ アダプター` も言語非依存で除外する（2026-09-14）

- [x]DONE 【Observability/VRAM】 Gatekeeper の dedicated VRAM unknown ログへGPU採取キャッシュの具体的な `StatusDetail` を伝播する（2026-09-14）

- [x]DONE 【Fix/VRAM】 `Microsoft Basic Display Adapter` を物理GPU候補から除外し、単一GPU環境での誤った multi-adapter ambiguity を防ぐ（2026-09-14）

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

- [ ] Demucs RAM admission の実ホスト受入と条件付き Single レーン設計は [引き継ぎ](docs/demucs_ram_admission_handoff.md) を参照する。ticket 実装自体は `906990c` に含まれる。

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

### 13. ワークロード適応型SQLiteキュー・スケジューラー (`goal:workload-scheduler`)
- [*] **【Arch/SQLite】CUEバッチ・長尺単曲・短尺単曲を区別し、資源に合わせてタスク順序とCPU枝数を調整する**
  - SQLiteへCUE由来の全トラックを一括登録し、Feederが部分バッチを先取りしないようにする。
  - 推定処理時間の短いタスクを優先し、ageingで長尺タスクも飢餓させない。等順位は受付FIFO。
  - CUE複数曲は曲単位の有界並列、長尺単曲はCPU枝を絞り、短尺単曲はDemucs後のCPUステム枝を有界並列化する。
  - Demucs/Tensor GPU共有排他を維持し、全特徴抽出枝のJoin後にPyTorch未使用キャッシュを解放する。
  - SQLite旧スキーマ・旧payloadを保ち、scheduler metadataがない既存行は保守的な単一枝で処理する。
  - [x] SQLite一括登録・claim順序・ageing、3種ワークロードの純粋ポリシー、Join後cleanup、ビルドをローカルで確認する。
  - [ ] 実ホストでCUE複数曲・長尺単曲・短尺単曲を流し、待ち行列の進行、CPU枝数、VRAM回復を確認する（push後に継続）。
  - 要件: `docs/workload_scheduler_requirements.lrf`

---

### 14. Demucs 6ステム高度解析 & 圏論的疎結合共通モジュール化 (`goal:demucs-stem-mir`)

> **設計原則（Category-Theoretic Decoupling / `CODE_RULE.md` 準拠）**
> - $\mathrm{Sep}(\mathrm{IO\_Effect}, \mathrm{PureDomain})$: ワーカー (`demucs_daemon.py`, `worker_cpu_daemon.py`, `worker_gpu_daemon.py`) は SHM/IPC/ONNX セッション管理（効果系）のみを担い、音響変換は `analyzer/common_*.py` の**純粋射 $\mathrm{Mor}(\mathrm{Domain}\to\mathrm{Codomain})$** として完全分離する。
> - **共通 `.py` 分離枠（5モジュール）**:
>   1. `analyzer/common_signal.py`: 帯域分割 (`subband_energy_morphism`)、7統計量包絡 (`envelope_stats7_morphism`: min/median 0潰れ解消)、自己相関/相互相関ラグ (`xcorr_lag_morphism`)、包絡減衰傾き (`decay_slope_morphism`) の純粋射。
>   2. `analyzer/common_stereo.py`: ステレオ波形 $\mathbb{R}^{2\times N}$ に対する Mid/Side 分解、Stereo Width $W=\mathrm{RMS}(S)/(\mathrm{RMS}(M)+\mathrm{RMS}(S)+\epsilon)$、Panning ILD (L/R dB・32フレーム推移)、L/R 位相相関係数 $\rho_{LR}$、低域 (<120Hz) 位相整合度の純粋射。
>   3. `analyzer/common_cross_stem.py`: 積対象 $\prod_{s\in\mathrm{Stems}}\mathbb{R}^N \times \mathbb{R}^N_{\text{mix}}$ 上のエネルギー構成比・Active Ratio・分離残差 SNR ($\mathrm{mix}-\sum y_s$)・ボーカル帯域マスキング・32フレーム編曲アーク行列・Drums×Bass ポケット＆サイドチェインダッキング純粋射。
>   4. `analyzer/common_pitch_harmony.py`: パラメータ化 $(f_{\min},f_{\max})$ 有声ゲート付き F0 輪郭・音域・跳躍度・安定度・ビブラート (4–8Hz) 純粋射（`vocals`/`bass` 共通関手）、およびクロマ→コード列・クロマエントロピー・純和声クロマ合成純粋射。
>   5. `analyzer/common_dispatch.py`: `StemName` から「必要 Warmup 集合」と「適用 `FeatureExtractor` / `BasePlugin` の直積 ($\prod$)」への純粋ルーティング関手（画一コピペ `STEM_CONFIGS` の廃止と無駄計算の除去）。

---

#### 【大目標 Phase 1】共通純粋射基盤の確立・既存バグ/未接続プラグイン解消・モノラル化直前のステレオ＆クロスステム基礎抽出 (`goal:phase1-foundation`)

- [x] **中目標 P1-M1: 共通信号処理・統計純粋射 (`analyzer/common_signal.py`) の分離と既存欠落値の修復**
  - [x] `P1-M1-S1`: `analyzer/common_signal.py` を新設し、状態非依存の7統計量純粋射 `envelope_stats7_morphism` (mean, std, peak, max, min, median, entropy)、FFT 帯域分割射 `subband_energy_morphism`、相互相関ラグ射 `xcorr_lag_morphism`、減衰傾き射 `decay_slope_morphism` を実装する。
  - [x] `P1-M1-S2`: `analyzer/librosa_dsp.py` の `_build_raw_features` で `0.0` ハードコードされている `rms_min`, `rms_median`, `centroid_min`, `centroid_median` を `envelope_stats7_morphism` 経由の実測値へ接続する。
  - [x] `P1-M1-S3`: `analyzer/essentia_dsp.py:21` の `_calc_chord_sequence` における `"vocal"`（単数形）タイポを `"vocals"` に修正し、`vocals` ステムでコード列が `["C"]*32` 固定になるバグを解消する。
  - [x] `P1-M1-S4`: `tests/test_common_signal.py` を作成し、純粋射の参照透過性・境界入力（空配列・無音・NaN/Inf 防御）および `"vocals"` コード列回帰テストを検証する。

- [ ] **中目標 P1-M2: ステレオ空間純粋射 (`analyzer/common_stereo.py`) とクロスステム基礎純粋射 (`analyzer/common_cross_stem.py`) の分離＆ `demucs_daemon` モノラル化直前フック**
  - [ ] `P1-M2-S1`: `analyzer/common_stereo.py` を新設し、`(2, N)` 波形から `pan_mean`, `pan_std`, `stereo_width`, `lr_correlation`, `low_band_phase_coherence`, `pan_seq_32`, `width_seq_32` を算出する純粋射 `stereo_field_morphism` とモノラル化射 `mono_downmix_morphism` を実装する。
  - [ ] `P1-M2-S2`: `analyzer/common_cross_stem.py` を新設し、`mix` と6ステム波形群から各ステムの `energy_share`, `active_ratio`, `v_to_accomp_db`, `vocal_band_masking` (300Hz–4kHz)、分離品質 `residual_snr_db` ($\mathrm{mix}-\sum y_s$)、および32フレーム編曲稼働マトリクス `arrangement_arc_32` を算出する純粋射 `cross_stem_foundation_morphism` を実装する。
  - [ ] `P1-M2-S3`: `models.py` (`HTDemucsSeparator.separate`) および `demucs_daemon.py` にて、ステレオ波形をモノラル化して SHM へ書き出す直前に `stereo_field_morphism` と `cross_stem_foundation_morphism` を適用し、**SHM 容量を1バイトも増やさず**メタデータ/特徴量へ渡すパイプラインを接続する（各ステムの `snr` にもステム対残差比または有効 SNR を供給）。
  - [ ] `P1-M2-S4`: `tests/test_common_stereo_and_cross_stem.py` を作成し、センターモノラル信号・ハードパン信号・逆位相信号・既知ステム合成信号に対する不変条件（$W\in[0,1]$, $\rho_{LR}\in[-1,1]$, $\sum \text{energy\_share}\approx 1$）を検証する。

- [ ] **中目標 P1-M3: ステム別ルーティング関手 (`analyzer/common_dispatch.py`) と未接続4プラグインの本番パイプライン統合**
  - [ ] `P1-M3-S1`: `analyzer/common_dispatch.py` を新設し、`analyzer.toml` の有効設定と `ctx.source`（ステム名）を受け取って適用対象プラグインを合成する純粋ディスパッチ関手 `build_stem_extractor_morphism` を実装する。
  - [ ] `P1-M3-S2`: 未接続だった4プラグインを適材適所で本番接続する：
    - `voice_cpp.py` (CPP / Breathiness) $\to$ `vocals`（および `mix`）
    - `audio_cutoff_lufs.py` $\to$ `mix`（カットオフ・偽ハイレゾ・TruePeak・LUFS/LRA）＋全6ステム（LUFS/LRA のみ高速算出）
    - `psychoacoustics_din45692.py` (Sharpness / Roughness / Tonality) $\to$ `mix`, `vocals`, `guitar`, `drums`
    - `structure_ssm.py` (Chorus / Drop / Novelty) $\to$ `mix`
  - [ ] `P1-M3-S3`: `worker_cpu_daemon.py` / `analyzer/librosa_dsp.py` から `common_dispatch.py` を呼び出し、`RawFeatures.to_postgres_dict()` で新規フィールドが欠落なく出力されることを `tests/test_plugin_dispatch_integration.py` で検証する。

---

#### 【大目標 Phase 2】ステム別「楽器特化 DSP」純粋射の導入と不要計算の刈り込み (`goal:phase2-instrument-dsp`)

- [ ] **中目標 P2-M1: 不要 Warmup / 不適合特徴量の刈り込みによる計算予算の創出**
  - [ ] `P2-M1-S1`: `analyzer/common_dispatch.py` と `STEM_CONFIGS` を改修し、`drums` からの `chroma` / `chroma_cqt` / `tonnetz` / `key` / `chord_sequence` 除外、および非低域ステム（`vocals`, `piano`, `guitar`）からの `subbass_env_mean` (20–60Hz) 除外を宣言的に定義する。
  - [ ] `P2-M1-S2`: スキーマ互換維持のため、除外されたキーには `None` または型安全な単位元（Neutral Element）を返し、`orchestrator/dispatcher/analysis_decision.go` の `completeStemFeatures` 契約を壊さないことをテストで保証する。

- [ ] **中目標 P2-M2: パラメータ化 F0・輪郭純粋射 (`analyzer/common_pitch_harmony.py`) と `vocals` / `bass` 特化解析**
  - [ ] `P2-M2-S1`: `analyzer/common_pitch_harmony.py` を新設し、帯域 $(f_{\min}, f_{\max})$ と RMS+HNR 有声ゲートを受け取る汎用ピッチ輪郭射 `voiced_f0_contour_morphism` を実装する（有声率 `voiced_ratio`、音域 `f0_min_hz`/`f0_max_hz`/`f0_median_hz`/`pitch_range_semitones`、音程跳躍度 `melodic_interval_mean`、ピッチ安定度/ケロケロ度 `pitch_quantization_score`、4–8Hz 変調から算出する `vibrato_rate_hz`/`vibrato_extent_cents`）。
  - [ ] `P2-M2-S2`: `vocals` ステムに `voiced_f0_contour_morphism(C2..C7)` と `common_signal.subband_energy_morphism` を適用し、**シンガーズフォルマント比 (2.5–3.5kHz)**、**歯擦音比 (Sibilance: 5–10kHz)**、**フレーズ長・残響減衰指標** を抽出する。
  - [ ] `P2-M2-S3`: `bass` ステムに `voiced_f0_contour_morphism(E1..E4)` と3帯域エネルギー分割射（**Sub-bass 20–60Hz / Mid-bass 60–250Hz / Harmonics-Attack 250–1500Hz 比**）および `decay_slope_morphism`（**サステイン/レガート vs スタッカート度**）を適用する。
  - [ ] `P2-M2-S4`: 合成サイン波・FM ビブラート信号・矩形波ベース信号を用いた単体テスト `tests/test_vocal_and_bass_morphisms.py` を作成し、ビブラート周波数/深さと帯域比の理論値一致を検証する。

- [ ] **中目標 P2-M3: `drums` / `guitar` / `piano` / `other` 楽器特化純粋射の実装**
  - [ ] `P2-M3-S1`: `drums` 用に3帯域オンセット分解純粋射 `drum_band_onset_morphism`（Low 30–120Hz ≒ Kick, Mid 150–400Hz+1.5–4kHz ≒ Snare, High 6–16kHz ≒ HiHat）を実装し、各帯域の**打撃密度 (hits/sec)**、**Kick/Snare/HiHat バランス**、**マイクロタイミング揺らぎ（グリッド偏差＝打ち込み度 vs 生ドラム度）**、**トランジェント鋭さ**を算出する。
  - [ ] `P2-M3-S2`: `drums` 低域包絡 × `bass` 包絡に `common_cross_stem.rhythm_pocket_and_ducking_morphism` を適用し、**Kick–Bass 時間ラグ [ms]**、**ポケット同期スコア**、**サイドチェイン・ダッキング強度**を算出する。
  - [ ] `P2-M3-S3`: `guitar` / `piano` / `other` 用に純粋射 `harmonic_instrument_character_morphism` を実装し、`guitar` の**歪み度（高次倍音フラットネス＋Roughness＋クリッピング率）＆ストローク対単音リード比**、`piano` の**和音密度（クロマエントロピー）＆打鍵ベロシティ分散＆ペダル減衰**、`other` の **HPSS 調波/打撃比＆パッド持続度**を算出する。
  - [ ] `P2-M3-S4`: `tests/test_instrument_specific_morphisms.py` を作成し、各楽器特化射の単体テストと計算時間を検証する。

---

#### 【大目標 Phase 3】クロスステム和声統合・編曲構造 SSM・選択的 Essentia ステム推論 (`goal:phase3-cross-harmonic-semantic`)

- [ ] **中目標 P3-M1: 純和声ステム合成クロマによる高精度キー/コード進行 & クロスステム和声テンション解析**
  - [ ] `P3-M1-S1`: `analyzer/common_pitch_harmony.py` と `common_cross_stem.py` に、`drums` と `vocals` を除いた和声ステム群（`bass + piano + guitar + other`）から純和声 CQT クロマを合成する射 `harmonic_bus_chroma_morphism` を実装し、`mix` クロマとのキー/コード一致度および確信度向上を算出する。
  - [ ] `P3-M1-S2`: `bass` の F0 ルート音 × 和声ステムのコード構成音 × `vocals` の F0 メロディ音を照合する積射 `cross_stem_harmonic_tension_morphism` を実装し、**ベース・ルート整合率（オンコード率）**、**ボーカル・コードトーン整合率 vs テンション/ブルーノート率**を算出する。
  - [ ] `P3-M1-S3`: `tests/test_cross_stem_harmony.py` にて、既知コード進行＋ベースルート＋テンションメロディの合成マルチステム入力に対する推論精度を検証する。

- [ ] **中目標 P3-M2: マルチステム編曲構造 SSM と選択的 Essentia ONNX ステム推論（ドメインシフト検証付き）**
  - [ ] `P3-M2-S1`: `analyzer/structure_ssm.py` を拡張し、`mix` 単体だけでなく6ステムのエネルギー＋純和声クロマを結合した状態空間上の自己相似行列射 `multistem_structure_ssm_morphism` を実装して、ブレイク・落ちサビ・ラスサビ・ドロップの境界検出精度を高める。
  - [ ] `P3-M2-S2`: `worker_cpu_daemon.py` / `analyzer/essentia_dsp.py` に設定駆動の選択的ステム推論射（`vocals` に対する `gender` / `voice_instrumental` 等、デフォルトは `analyzer.toml` で on/off 制御可能）を追加し、`mix` 推論値との乖離（ドメインシフト指標）を併記する。
  - [ ] `P3-M2-S3`: PostgreSQL `raw.library_flac.features` JSONB スキーマ（`analysis_schema_version = 2` または後方互換拡張）および `zig/migrate_features.py` との整合・E2E 回帰テストを完了する。

---

#### 🔁 POLYGLYPH形式 ループコーディング共有命令セット (`loop-agent` / Proposer × Judge 共通ペイロード)

以下のブロックは、各 Phase / 中目標 / 小目標を `loop-agent`（Proposer × On-Demand Researcher × Claude Judge）またはサブエージェントで反復実装・検証する際にそのまま共有可能な **POLYGLYPH×SIGMA/1 契約シード**です。

```text
@meta=text/POLYGLYPH×SIGMA/1
@codec=nearest_ancestor(POLYGLYPH.md);read_before_body=true
@authority=SIGMA/1+routed-LRF-only;PG=representation-only
@decode_failure=STOP;unknown=preserve;action=blocked;ask=true

#SIGMA LRF/1
@lrf=1|aud=LLM|scope=demucs-stem-mir-loop
R|cat.purity|*|MUST|LW_SCOPE|all new DSP/MIR logic MUST reside in pure domain modules analyzer/common_signal.py, analyzer/common_stereo.py, analyzer/common_cross_stem.py, analyzer/common_pitch_harmony.py, analyzer/common_dispatch.py as explicit morphisms Mor(Domain->Codomain) free of filesystem/SHM/DB/ONNX/global-mutable-state effects
R|cat.functor|*|MUST|LW_SCOPE|compose stem extractors via Reader Applicative FeatureExtractor (pure/map/ap/__mul__/product_all) and pure product objects; workers (demucs_daemon.py, worker_cpu_daemon.py, worker_gpu_daemon.py) perform IO/SHM attachment only and invoke composed pure morphisms
R|shm.invariant|*|MUST|LW_SCOPE|compute stereo (2,N) and cross-stem wave morphisms inside demucs_daemon/models.py immediately BEFORE mono downmix; NEVER double SHM arena size to stereo and NEVER break single-stem wavefront streaming or RAM admission budgets
R|code.rule|*|MUST|LW_SCOPE|enforce EarlyReturn, GuardClause(exit_fast), no nested if-else, no silent exception swallowing, explicit context logging on boundary errors, and name affix (Large/Heavy/Complex/Batch/Mega) on functions >30 lines
R|schema.compat|*|MUST|LW_SCOPE|preserve orchestrator/dispatcher/analysis_decision.go requiredAnalysisStems completeness contract and raw.library_flac.features JSONB backward compatibility; replace skipped stem computations with type-safe neutral elements
R|verify.gate|*|MUST|LW_SCOPE|every small ticket (P*-M*-S*) MUST pass pytest focused unit/regression tests, git diff --check, and proof-checker.exe -path <target> before marking [*]tested or [x]done

§LOOP_CONVERGENCE_PROPOSITIONS (Judge 収束判定命題 P1..P6)
P1(圏論的疎結合): ∀新規特徴量 f ∈ Phase1..3: f = Mor_pure ∘ IO_adapter ∧ Mor_pure ∈ analyzer/common_*.py ∧ IO副作用(SHM/File/DB/ONNX)=∅.
P2(SHM不変・ゼロ肥大): Stereo特徴量(Pan/Width/ρ_LR) ∧ CrossStem基礎特徴量(EnergyShare/ActiveRatio/ResidualSNR/Masking/ArrangementArc) は `HTDemucsSeparator.separate` 内のモノラル化直前 `(2,N)` オンメモリ段階で算出され、SHM割当サイズ増分 = 0 bytes.
P3(バグ解消・既存資産結線): `essentia_dsp.py:21` `"vocal"`→`"vocals"` 修正済 ∧ `rms_min/median`,`centroid_min/median` 実測値結線済 ∧ 未接続4プラグイン(`voice_cpp`,`audio_cutoff_lufs`,`psychoacoustics_din45692`,`structure_ssm`)が `common_dispatch.py` 経由で対象ステムへ結線済.
P4(楽器特化・無駄計算削減): `drums` から不適合な CQT/和声計算が除外され、`vocals`(有声F0輪郭/ビブラート/フォルマント/歯擦音/CPP)、`drums`(Kick/Snare/HiHat帯域オンセット/マイクロタイミング)、`bass`(低域F0/3帯域比/サステイン/Kick×Bassポケット&ダッキング)、`guitar/piano/other`(歪み度/和音密度/HPSS) が純粋射の合成として稼働.
P5(クロスステム和声・構造統合): `bass+piano+guitar+other` 純和声合成クロマ射 ∧ `bass×chord×vocals` テンション整合射 ∧ マルチステムSSM構造射が単体テストで理論値と一致.
P6(検証・健全性): 変更対象に対する `pytest` 単体・回帰テスト全緑 ∧ `git diff --check` クリーン ∧ `¬SilentSwallow` / `EarlyReturn` 遵守.

§DISPATCH_SEED_TEMPLATE (各イテレーションで `<TICKET_ID>` を指定して投入)
Role: Proposer/Worker (or Judge)
Target: `issues.md` §14 `<TICKET_ID>` (例: `P1-M1-S1`..`S4`, `P1-M2-S1`..`S4`, `P1-M3-S1`..`S3`, `P2-M1`..`M3`, `P3-M1`..`M2`)
Acceptance: 命題 P1..P6 のうち `<TICKET_ID>` 該当命題をすべて充足 ∧ 単体テスト追加・PASS ∧ 既存テスト非破壊
Scope: `analyzer/common_*.py` (純粋射新設) ⊕ 最小限の境界アダプタ (`analyzer/librosa_dsp.py`, `analyzer/essentia_dsp.py`, `models.py`, `demucs_daemon.py`, `worker_cpu_daemon.py`, `tests/test_*.py`)
Known facts:
- `models.py:377-380` で `htdemucs_6s` の `(2,N)` 出力が即 `np.mean(axis=0)` されており、この直前がステレオ射＆クロスステム射の唯一無二のゼロコピー計測点である。
- `analyzer/essentia_dsp.py:21` は `"vocal"` タイポにより `"vocals"` ステムのコード列が `["C"]*32` 固定になっている。
- `analyzer/librosa_dsp.py:351-471` の `STEM_CONFIGS` は7エントリ完全同一コピーであり、`common_dispatch.py` の関手ルーティングへ置換する。
```
