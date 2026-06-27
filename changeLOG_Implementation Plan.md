# 旦那様の次世代アーキテクチャ実装計画（Go+Python 共有メモリ・オーケストレーション）

おーほほほほ！ 旦那様、前時代的なOOMキラーやロック地獄とはこれでお別れですわ！
Windows 11の「檻」である `/dev/shm` 不在という制約を、**Goの低レイヤ Win32 API (`CreateFileMapping`)** と **Python の `mmap` (または `multiprocessing.shared_memory`)** を用いた華麗な名前付き共有メモリ・マッピングで打ち破り、完璧なゼロ・コピー（あるいは最小オーバーヘッド）の波形引き渡しを実現いたしますわ！

## 目標 (Goal)

現在の「Python単体・泥縄式直列実行」から、「**Goオーケストレーター ＋ Python並列ワーカー ＋ 非同期DBレイヤ**」の三位一体次世代アーキテクチャへと移行しますわ。

1. **ps1によるファイル探査とキューイング**: PowerShellがFLACを検索し、Goオーケストレーターのキューに投下する。
2. **Goによる完全なタスク・リソース管理と共有メモリ管轄**: Goはファイルパスの文字列だけを受け取り、ワーカー群のステータス管理、および Windows API (`syscall.CreateFileMapping`) を用いて波形保存用の共有メモリ領域の確保・管理を行う。
3. **Pythonワーカー内でのDemucs分離・Librosa並列解析**: Demucsが分離したステム波形をGoが確保した共有メモリ領域に直接配置。LibrosaはディスクIOを経由せずRAM上の共有メモリから直接波形を読み出し、並列に事前キャッシュ・解析を行う。Essentiaでの解析も並列化する。
4. **GoによるDB (INSERT/UPSERT) の非同期ゴルーチン管理**: Pythonは重いUPSERTの完了を待たず、解析結果(JSON等)をGoに返すだけ。GoがバックグラウンドのGoroutineで非同期にDBアクセスを行い、12分の虚無ロックを消滅させる。

> [!WARNING]
> **User Review Required: 共有メモリの設計について**
> Windows上でGoとPython間で共有メモリ通信を行う場合、Go側から `Local\FLAC_SHM_UUID` のような名前付きファイルマッピングオブジェクトを作成し、Python側から `mmap` を用いてそこにアタッチする設計といたします。波形データの形状 (shape) や dtype (float32等) のメタデータは、別途GoとPython間でJSONを用いた名前付きパイプや標準入出力でやり取りしますが、これでよろしいでしょうか？

> [!TIP]
> **Performance Optimizations**
> DBへのUPSERT処理をGoのGoroutineで完全に非同期化（バッファリングキュー）することで、Python側のプロセスは即座に次のFLACファイルのDemucs解析へ移ることができます。これによりGPU/CPUの稼働率を限界まで引き上げられますわ！

## Open Questions

- **Go ↔ Python のプロセス間通信 (IPC)**:
  タスクのディスパッチやメタデータのやり取りには、GoからPythonをサブプロセスとして起動し、標準入出力 (stdin/stdout) でJSON Lines形式でやり取りする方式（LSP風）が最も堅牢で高速かと存じますが、ご意向はございますでしょうか？
- **DBドライバ (Go)**:
  既存の `db.py` のロジックをGoに移植することになりますが、DBドライバとしては `pgx` (Jackc) を用いて爆速のコネクションプーリングとバルクインサートを実装する形でよろしいでしょうか？

## 圏論的・関数型アーキテクチャに基づく修正案 (Functional Architecture Fixes)

共有メモリは本質的に「可変状態（Mutable State）」であり、そのまま扱うと並行処理における非決定性を生み、参照透過性（Referential Transparency）が破綻いたします。
これを圏論的原則に基づいて安全に運用するため、以下の修正案（アーキテクチャの制約）を本計画に組み込みます：

1. **WORM (Write-Once, Read-Many) / 線形型の適用**:
   共有メモリ領域をグローバルな変数として扱うのではなく、**Demucsが波形を書き込んだ直後に Immutable（不変データ）として凍結**いたします。Librosa や Essentia などの並列ワーカーはこれを **Read-Only** としてのみ参照します。これによりデータ競合が原理上発生しなくなり、関数（Morphism）としての純粋性が保たれます。
2. **Actor Model パターンと State/IO Monad の境界分離**:
   GoオーケストレーターとPythonワーカー間の通信はメッセージパッシング（Actor Model）とし、共有メモリの名前（ポインタ）をメッセージとして移譲します。Python内では、`mmap` による共有メモリへのアタッチ処理を強烈な副作用（`[IO Monad]` または `[Effect]`）として分離し、波形データをNumPy配列として取り出した後は純粋な `[Morphism]` 処理に徹する設計といたします。

## Proposed Changes

### [Architecture & Orchestration]

#### [NEW] `orchestrator/main.go`
- Goオーケストレーターのエントリーポイント。
- `ps1`からのタスクを受け取るHTTPサーバーまたは標準入力リスナー。
- Goroutineワーカープールを構築し、Pythonサブプロセスを管理。
- 処理完了タスクのDB保存用非同期キュー（Goroutine）を起動。

#### [NEW] `orchestrator/shm_windows.go`
- `syscall` パッケージを用い、`CreateFileMapping` / `MapViewOfFile` による Windows API を直叩きして、名前付き共有メモリ領域を確保・解放する低レイヤロジック。

#### [NEW] `orchestrator/db.go`
- PostgreSQL (`pgx`) を用いたコネクションプールと非同期 UPSERT 処理の実装。
- Pythonから受け取った解析結果のJSONをDBスキーマに合わせて構造体へマッピングし、バッチ挿入する。

---

### [Worker (Python)]

#### [MODIFY] `pipeline.py` & `main.py`
- DBアクセス (`db.py` への依存) を完全に削除。
- `multiprocessing.shared_memory` (または `mmap`) を用い、Goから渡された共有メモリ名 (例: `Local\SHM_XYZ`) を開いて波形データを読み書きするロジックに変更。
- Demucsが分離したステムを共有メモリ領域へ直接ダンプ。
- Librosa処理タスクにおいて、ディスク上のWAVではなく共有メモリ上のNumPy配列から処理するよう修正。
- 処理結果をGoオーケストレーターへ JSON として stdout で返す。

#### [DELETE] `db.py`
- Python側でのDB接続は不要となるため削除（機能はGoへ移行）。

---

### [Scripts]

#### [MODIFY] `run_batch.ps1`
- `Get-ChildItem` で取得したFLACファイルのパスを、`python main.py` に直列で渡す処理から、Goのオーケストレータープロセスへキューイング（例えば HTTP POST や名前付きパイプへの書き込み）する処理に変更。

## Verification Plan

### Automated Tests
- Go側での共有メモリアロケーションと、Python側からの読み出しの整合性を確認するための小さな単体テストを作成し、Windows環境で `go test` 及び `pytest` を実行。

### Manual Verification
- 実際に The Album Leaf のような楽曲を1曲流し、PythonプロセスがDB待機でブロックされず、GoのGoroutineが非同期にUPSERTを完了させることをログで確認いたします。
- GPU使用率とCPU使用率が同時に高水準で維持されているか、OOMが発生しないか、リソースモニタリングで確認。
