# `run_batch.ps1 -Unreg` 設計書

## 1. 目的

`-Unreg` は、指定された FLAC 群から「ローカル SQLite の `task_state` と PostgreSQL の `raw.library_flac` の双方に未登録」と判定できる対象だけを解析するモードとする。

利用例:

```powershell
.\run_batch.ps1 'N:\Music\DLC\' -Unreg -SingleTask
```

`-Unreg` は PostgreSQL という実装名ではなく、利用者の意図（未登録のみ）を表す公開 CLI 名とする。将来 PostgreSQL 以外のカタログへ置換しても、CLI 名は維持できる。

本書は設計のみを扱う。コード変更、DBへの書き込み、解析実行は行わない。

## 2. 調査時点の既存仕様

### 2.1 `run_batch.ps1`

- 第1引数はファイルまたはディレクトリで、再帰的に `.flac` を列挙する。
- 列挙には `fd.exe`、次に `rg.exe`、最後に PowerShell の再帰走査を使う。
- 通常モードは常駐 Orchestrator の `POST http://127.0.0.1:8080/task` にファイル単位で送る。
- `-SingleTask` は `single-orchestrator.exe -single-file <path>` を FLACごとに逐次起動する。
- 現在の `run_batch.ps1` に `-Retry` は存在しない。`-Force`、`-SingleTask`、`-DryRun` 等は存在する。
- `-Force` は標準モードでは HTTP payload の `force`、SingleTask では `-force` として下位へ渡される。

### 2.2 SQLite

SQLite の既定状態DBは `orchestrator/orchestrator.db`（実行場所により `orchestrator.db` へフォールバック）である。`task_state` の主キーは次の複合キーである。

```text
(file_path, track_number)
```

既存の通常 enqueue 判定は、`COMPLETED`、`RUNNING`、`PENDING`、`QUEUED` をスキップし、`FAILED` と `FAILED_MAYBE_RETRY` は再実行対象にする。SingleTask の `ClaimSingleTask` は、既存の `COMPLETED` を `force:false` でスキップし、active状態の扱いは `recoverActive` に依存する。現在の SingleTask 呼出しは `recoverActive=true` である。

### 2.3 PostgreSQL

接続URLは `config.toml` の `[database].url` からロードされ、既存の Go 実装は `database/sql` と `lib/pq` を使う。`raw.library_flac` は次の性質を持つ。

- `audio_hash` にユニークインデックスがある。解析後波形MD5であり、曲の実体識別に近い。
- `filepath` は最新の絶対パスで、検索用インデックスがある。
- `track_number` は CUE 分割時のトラック番号である。
- `analyzed_at` は nullable で、登録行の存在と解析完了を同一視できるかは別途決定が必要である。
- 既存の重複判定は、解析中に `audio_hash` を計算して `SELECT 1 ... WHERE audio_hash = $1` する方式である。これは `-Unreg` の事前フィルタとは別段階である。

`config.toml.example` と既存READMEには接続URIの例があるが、設計書・ログ・エラーメッセージでは実値を表示しない。

### 2.4 CUE と SingleTask

`single-orchestrator.exe -single-file` は、まず CUE を検査し、CUEトラックごとに `TaskPayload` を展開してから、各トラックを逐次処理する。CUEがない場合は Track 1 の全体ファイルへフォールバックする。

そのため、FLACファイル単位の事前除外だけでは、次のケースを表現できない。

```text
同じCUE FLACの Track 1 は両DBに登録済み、Track 2 は未登録
```

この場合にファイル全体をスキップすると Track 2 を取りこぼす。`-Unreg` をCUEトラック単位で厳密にするなら、CUE展開後の `(正規化パス, track_number)` 単位で照合し、未登録トラックだけを実行する経路が必要である。

## 3. 提案する意味論

### 3.1 登録判定

初期実装では、事前スキャンで得たパスを正規化し、PostgreSQLの `(filepath, track_number)` と照合する「パス＋トラック登録判定」を基本とする。該当行の `analyzed_at IS NOT NULL` を満たす場合だけ登録済みと判定し、`analyzed_at IS NULL` の行は未登録として扱う。

```text
should_process = NOT sqlite_registered AND NOT postgres_registered
```

PostgreSQLの `audio_hash` はファイルを解析しないと得られないため、スキャンだけで hash-only 判定はできない。したがって、`(filepath, track_number)` で判定する事前フィルタは、移動・改名・別名コピーを同一曲と認識する hash dedup ではない。実処理中の既存 `skip_dup_by_hash` は最後の安全網として残す。

### 3.2 パス正規化

比較キーは次の規則で一元生成する。

1. `Resolve-Path` / Goの絶対パス化で絶対パスにする。
2. Windowsの区切り文字を統一する。
3. `.`、`..` を解決し、末尾の区切り文字を除去する（ルート自体を除く）。
4. Windows既定の大文字小文字非区別に合わせて ordinal invariant の case-fold を行う。
5. 比較用キーだけを正規化し、実行に渡す実パスは実在する canonical path を保持する。

ネットワークドライブでは、同一共有を異なるドライブレター、UNC名、別名で表す可能性がある。これらの同一視は標準の文字列正規化だけでは保証できないため、初期範囲では「同一表記体系の正規化済み絶対パス」と定義する。

### 3.3 SQLiteの扱い

`task_state` はトラック単位で読み、`COMPLETED`、`PENDING`、`QUEUED`、`RUNNING` を登録済み/処理中として扱う。`FAILED` と `FAILED_MAYBE_RETRY` は未完了として扱い、`-Unreg` の実行対象に含める。

同一FLACについてCUEの一部だけが登録済みなら、登録済みトラックを除外し、未登録トラックだけを実行する。これには既存の SingleTask 全トラックループを拡張する必要がある。

### 3.4 PostgreSQLの扱い

`raw.library_flac` の `analyzed_at IS NOT NULL` 行から `filepath` と `track_number` を読み取り、入力側とDB側の双方を同じGoパス正規化関数へ通したキー集合を作る。その集合に一致する場合だけ登録済みと判定する。`analyzed_at IS NULL` の行は未登録として扱う。`raw.library_flac_history` は参照しない。

PostgreSQL照会に失敗した場合は、厳密モードの性質上「未登録」とは判定せず、fail-closed でバッチを開始しない。接続不能時に全件を実行すると、`-Unreg` の安全性を失うためである。

## 4. 推奨アーキテクチャ

PowerShellからPostgreSQLへ直接接続させず、既存Orchestratorと同じ設定・接続管理を使う Go 側の read-only preflight を追加する。

### 4.1 推奨フロー

```text
run_batch.ps1
  -> FLAC列挙・実パス確定
  -> -Unreg preflight 呼出し
       -> SQLite task_state 読み取り
       -> PostgreSQL raw.library_flac 読み取り
       -> 正規化キーで差分計算
       -> 未登録ファイル/トラック一覧を返却
  -> 一覧だけを既存の SingleTask または POST 経路へ渡す
```

preflight は読み取り専用とし、`task_state` への claim/insert は解析開始時の既存処理に任せる。これにより、照合途中の登録や並行プロセスによる競合を「予約済み」と誤認しない。

### 4.2 CUEトラック単位の実装案

推奨は、Go側に「候補ファイルをCUE展開し、両DBの登録キーを照合して、実行可能なトラックを返す」責務を置く方法である。SingleTaskでは、返されたトラックだけを `RunSingleTask` に渡す。

初期リリースは `-Unreg -SingleTask` に限定する。標準POST経路では `-Unreg` を拒否し、既存HTTP `/task` や通常並列モードの挙動は変更しない。

### 4.3 一括照合と再確認

候補を一括照合する場合でも、解析開始直前に SQLite の claim を原子的に行う。PostgreSQL側は照合後に別プロセスが登録する競合があり得るため、実処理中の hash重複チェックを無効化しない。最終的な登録は既存UPSERT/DLQ経路を使用する。

## 5. オプション間の関係

### `-Unreg` と `-SingleTask`

併用を正式サポートする。`-SingleTask` の逐次実行、CUE分割、完了待機、既存のmutex/claim/flushを維持する。`-Unreg` はその前段の候補選択だけを担当する。

### `-Unreg` と `-Force`

併用不可として、ファイル列挙やDB照合より前に早期エラーにする。`-Unreg` は登録済みを除外し、`-Force` は登録済みを再解析するため、意味が正反対である。

### `-Unreg` と `-Retry`

現状 `run_batch.ps1` に `-Retry` は存在しない。将来追加する場合、`-Retry` は失敗状態の再実行、`-Unreg` は両DB未登録の新規対象という別軸にする。併用時の集合は「未登録、またはSQLiteが失敗状態で、かつPostgreSQLにも登録がない」など、明示的な仕様を決めてから実装する。

### `-Unreg` と `-DryRun`

DB照合までは実行し、実際の解析・claim・POSTは行わない。出力には総候補数、SQLite登録数、PostgreSQL登録数、両方登録数、実行対象数、照合エラーを示す。ただしパスは必要最小限にし、接続情報は出力しない。

## 6. 失敗時の扱い

| 事象 | 推奨動作 |
|---|---|
| SQLite DBを開けない | バッチ開始せず exit 1 |
| PostgreSQL接続/SELECT失敗 | バッチ開始せず exit 1（fail-closed） |
| `raw.library_flac` が存在しない/権限不足 | 接続エラーとして exit 1。空テーブルとは扱わない |
| パス正規化に失敗 | 該当対象を未登録扱いにせず、照合エラーとして exit 1 |
| CUE検査失敗 | 既存SingleTaskの仕様どおり失敗。全体ファイルを暗黙に実行しない |
| SQLite claim競合 | 既存claim結果に従ってスキップし、競合を失敗扱いにするかはログ/終了基準を決める |
| 解析後のPostgreSQL UPSERT失敗 | 既存の `send_failed.db` DLQ と `FAILED`/端末状態処理を維持 |
| PostgreSQL行はあるが `analyzed_at IS NULL` | 未登録として扱い、実行対象に含める |

## 7. セキュリティ・接続情報

- 接続URLは既存の `config.toml` をGo側だけで読み込む。
- PowerShellの引数、標準出力、例外文字列に接続URLを渡さない。
- SQLはパラメータ化し、候補パスをSQL文字列連結しない。
- 照合は `SELECT` のみで、トランザクション中の書き込みを行わない。
- DB接続には既存 `database.db_timeout_sec` を適用し、タイムアウト・rows.Close・connection.Close を保証する。
- 一括取得か候補パス単位の照会かは、ライブラリサイズとSQLパラメータ上限を踏まえて選ぶ。大量ライブラリでは全filepath取得＋メモリ上の正規化、またはチャンク照会を候補とする。

## 8. テスト計画

コード実装時は少なくとも次をテストする。

1. SQLiteのみ登録、PostgreSQLのみ登録、双方登録、双方未登録の4象限。
2. Windowsの大文字小文字、`\`/`/`、`.`/`..`、末尾区切り文字。
3. CUEの全トラック登録、一部登録、全トラック未登録、CUEなしTrack 1。
4. `FAILED`、`FAILED_MAYBE_RETRY`、`PENDING`、`QUEUED`、`RUNNING`、`COMPLETED` の境界。
5. PostgreSQL接続失敗、SELECT失敗、権限不足、空結果と未接続の区別。
6. `-Unreg -SingleTask` が既存の逐次CUE処理、claim、terminal flushを壊さないこと。
7. `-Unreg -Force` の拒否、`-Unreg -DryRun` の無書き込み。
8. DB照合後の並行登録・claim競合。
9. ネットワークドライブ上の実在確認と、パスが消えた場合の失敗処理。

## 9. 実装受入条件

- `-Unreg` なしの既存挙動に変更がない。
- `-Unreg` は照合エラーを未登録として通さない。
- SQLiteとPostgreSQL双方の登録を正規化キーで判定できる。
- CUEを単一ファイルとして誤って丸ごとスキップせず、設計した粒度でトラックを選択できる。
- `-SingleTask` の既存のCUE、逐次、キャンセル、claim、flush、mutexの保証を維持する。
- `-Force` の意味を黙って変更しない。
- DB URLその他の秘密情報をログ・設計書・CLI表示へ出さない。
- 対象外のユーザー変更を上書きしない。

## 確定事項

1. PostgreSQLは、正規化した `filepath` と `track_number` の一致で照合する。
2. CUEは未登録トラックだけを実行する。
3. `analyzed_at IS NULL` のPostgreSQL行は登録済み扱いにしない。
4. SQLiteの `FAILED` / `FAILED_MAYBE_RETRY` は `-Unreg` の実行対象に含める。
5. `-Unreg` と `-Force` の併用は禁止し、早期エラーにする。

6. 初期実装は `-SingleTask` 専用とし、標準並列モードでは `-Unreg` を拒否する。
7. 照合はGo側の専用read-only preflightで行い、PowerShellへ接続情報を渡さない。
8. PostgreSQLは現行の `raw.library_flac` だけを参照し、`raw.library_flac_history` は参照しない。
9. 初期実装では `N:\...` とUNCパスを別キーとして扱い、共有マッピングの同一視は行わない。

## 実装状態

- PowerShellとGoの双方で `-Unreg` / `-SingleTask` / `-Force` の組み合わせを早期検証する。
- CUE展開後、全トラックのSQLite/PostgreSQL照合が完了してから実行対象を確定する。途中の照合失敗では1件もclaimしない。
- `-Unreg -DryRun` はGoのread-only check-only経路を使い、SQLiteの作成・マイグレーション・状態更新や解析を行わない。
- PostgreSQL接続・照会エラーはfail-closedとし、接続URLやパスワードをエラーへ含めない。
- 厳密なDB側パス正規化のため、各SingleTaskプロセスは解析済みPostgreSQLキーを一度読み込む。大規模ライブラリでの所要時間とメモリ量は実運用前に計測する。
