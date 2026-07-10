# FLAC Analyzer DB Information

現在のプロジェクトにおけるデータベース（PostgreSQL）のER図とテーブル定義です。
以下のMermaidコードは、draw.io（[Insert] -> [Advanced] -> [Mermaid]）に直接貼り付けてインポートすることができます。

## ER図 (Mermaid形式)

`mermaid
erDiagram
    library_flac ||--o{ library_flac_history : ""履歴保存 (トリガー経由)""
    
    library_flac {
        SERIAL id PK
        VARCHAR(32) audio_hash UK ""波形MD5""
        TEXT filepath ""パス""
        TEXT filename ""ファイル名""
        INT track_number ""トラック番号""
        VARCHAR album_artist ""アルバムアーティスト""
        VARCHAR album ""アルバム名""
        VARCHAR artist ""曲アーティスト""
        VARCHAR title ""曲タイトル""
        JSONB meta ""メタデータ""
        JSONB features ""音響特徴量(Librosa)""
        JSONB predictions ""分類結果(Essentia)""
        TIMESTAMP collected_at ""収集日時""
        TIMESTAMP analyzed_at ""解析実行日時""
    }

    library_flac_history {
        SERIAL history_id PK
        INT library_id FK ""library_flac.id""
        VARCHAR(32) audio_hash
        TEXT filepath
        TEXT filename
        INT track_number
        VARCHAR album_artist
        VARCHAR album
        VARCHAR artist
        VARCHAR title
        JSONB meta
        JSONB features
        JSONB predictions
        TIMESTAMP collected_at
        TIMESTAMP analyzed_at
        TIMESTAMP archived_at ""履歴退避日時""
    }
`

## テーブル定義詳細

### aw.library_flac
メインとなる楽曲情報・解析結果を格納するテーブルです。常に最新の情報を保持します。

| カラム名 | 型 | 制約等 | 説明 |
| :--- | :--- | :--- | :--- |
| id | SERIAL | PRIMARY KEY | 主キー |
| udio_hash | VARCHAR(32) | NOT NULL, UNIQUE | 各曲のデコード後波形(numpy)のMD5(16進数32文字)。トラックごとの一意性を担保 |
| ilepath | TEXT | NOT NULL, INDEX | 最新のファイル絶対パス。ファイル移動の検知に使用 |
| ilename | TEXT | NOT NULL | 最新のファイル名 |
| 	rack_number | INT | | CUEシート分割時のトラック番号（なければNULL） |
| lbum_artist | VARCHAR | INDEX | アルバムアーティスト (検索性能向上用平坦化カラム) |
| lbum | VARCHAR | INDEX | アルバム名 (検索性能向上用平坦化カラム) |
| rtist | VARCHAR | INDEX | 曲のアーティスト (検索性能向上用平坦化カラム) |
| 	itle | VARCHAR | INDEX | 曲のタイトル (検索性能向上用平坦化カラム) |
| meta | JSONB | NOT NULL, DEFAULT '{}' | アーティスト、アルバム、タイトル等の最新詳細メタデータ |
| eatures | JSONB | NOT NULL, DEFAULT '{}', GIN | 各ステムのLibrosa音響特徴量 |
| predictions | JSONB | NOT NULL, DEFAULT '{}', GIN | Essentiaによる分類結果（mix等） |
| collected_at | TIMESTAMP (TZ) | NOT NULL, DEFAULT CURRENT_TIMESTAMP | 収集・更新検知日時 |
| nalyzed_at | TIMESTAMP (TZ) | | 解析実行日時（未解析やスキップ時はNULL） |

### aw.library_flac_history
library_flac テーブルの更新時に、古いレコードの情報を退避するための履歴保存用テーブルです。

| カラム名 | 型 | 制約等 | 説明 |
| :--- | :--- | :--- | :--- |
| history_id | SERIAL | PRIMARY KEY | 履歴の主キー |
| library_id | INT | NOT NULL | library_flac.id に対応（外部キー相当） |
| udio_hash | VARCHAR(32) | NOT NULL | 当時のaudio_hash |
| ilepath | TEXT | NOT NULL | 当時のファイルパス |
| ilename | TEXT | NOT NULL | 当時のファイル名 |
| 	rack_number | INT | | 当時のトラック番号 |
| lbum_artist | VARCHAR | | 当時のアルバムアーティスト |
| lbum | VARCHAR | | 当時のアルバム名 |
| rtist | VARCHAR | | 当時の曲アーティスト |
| 	itle | VARCHAR | | 当時の曲タイトル |
| meta | JSONB | NOT NULL | 当時のメタデータ |
| eatures | JSONB | NOT NULL | 当時の音響特徴量 |
| predictions | JSONB | NOT NULL | 当時の分類結果 |
| collected_at | TIMESTAMP (TZ) | NOT NULL | 当時の収集日時 |
| nalyzed_at | TIMESTAMP (TZ) | | 当時の解析実行日時 |
| rchived_at | TIMESTAMP (TZ) | NOT NULL, DEFAULT CURRENT_TIMESTAMP | 履歴として退避された日時 |

### データベーストリガー
- 	rg_archive_library_flac
  - 対象: aw.library_flac の BEFORE UPDATE
  - 条件: meta または eatures に変更があった場合
  - 動作: 更新前の古いレコード（OLD）を aw.library_flac_history にINSERTして履歴を残します。