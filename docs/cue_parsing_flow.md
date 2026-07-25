# CUE シートパース＆フォールバックフロー

本ドキュメントは、`flac_decode.py` における FLAC ファイルの CUE シート検出・パース・トラック分割の判定ロジックと、長尺ファイルに対するストリーミングデコードの挙動を解説します。

---

## 1. CUE 検出・パース判定フロー

FLAC ファイルからトラック境界を抽出する際、以下の優先順序で CUE 情報を探索し、最初にヒットしたソースからスライスを構築します。すべて見つからなかった場合は、ファイル全体を単一トラックとして安全にフォールバックします。

```mermaid
flowchart TD
    Start["build_flac_handle(filepath)"] --> ReadMeta["mutagen.FLAC() でメタデータ読込"]
    ReadMeta --> CheckVorbis{"VorbisComment に<br/>'cuesheet' キーが存在？"}

    CheckVorbis -- "Yes" --> ParseCueText["parse_cue_text_to_slices()<br/>INDEX 01 MM:SS:FF → サンプルオフセット変換"]
    ParseCueText --> MergeGlobal["グローバル TITLE → album<br/>グローバル PERFORMER → albumartist / artist<br/>（未設定の場合のみマージ）"]
    MergeGlobal --> SliceReady["TrackSlice リスト構築完了"]

    CheckVorbis -- "No" --> CheckBlock{"mutagen metadata_blocks に<br/>CueSheet ブロックが存在？"}

    CheckBlock -- "Yes" --> ParseBlock["CueSheet ブロックから<br/>audio track (type=0) の<br/>start_offset を抽出"]
    ParseBlock --> CalcBoundary["隣接トラックの start_offset を<br/>end_sample として境界計算"]
    CalcBoundary --> SliceReady

    CheckBlock -- "No" --> SingleTrack["CUE 情報なし<br/>→ ファイル全体を<br/>単一トラック (track_number=1) として処理"]
    SingleTrack --> SliceReady

    SliceReady --> CheckCount{"スライス数 = 1？"}
    CheckCount -- "Yes" --> PreferFLACTag["CUE 由来の title/artist より<br/>FLAC 本体タグを優先<br/>（EAC 等の文字数制限対策）"]
    PreferFLACTag --> Done["FlacHandle 構築完了"]
    CheckCount -- "No (複数トラック)" --> Done
```

### 判定優先順位まとめ

| 優先度 | CUE ソース | 取得先 | 備考 |
|:---:|:---|:---|:---|
| **1** | VorbisComment `cuesheet` | FLAC タグ（テキスト形式） | EAC / foobar2000 等がエンコード時に埋め込む形式 |
| **2** | Mutagen CueSheet metadata block | FLAC メタデータブロック（バイナリ形式） | FLAC 仕様上の正式な CUESHEET ブロック |
| **3** | *(フォールバック)* | — | CUE 情報なし → ファイル全体を1トラックとして処理 |

---

## 2. INDEX 01 → サンプルオフセット変換

CUE テキストの `INDEX 01 MM:SS:FF` を内部サンプル位置へ変換するロジック：

```
total_seconds = MM × 60 + SS + FF / 75.0
sample_offset = int(total_seconds × sample_rate)
```

- `FF` は CD フレーム単位（1秒 = 75フレーム）
- 各トラックの `end_sample` は次トラックの `start_sample`、最終トラックは `total_samples`

---

## 3. 長尺ファイルのストリーミングデコード

`process_slice_with_seq_safety()` は、トラック長に応じてデコード戦略を動的に切り替えます。

```mermaid
flowchart TD
    Input["process_slice_with_seq_safety()"] --> CalcDuration["duration_sec = total_samples / sample_rate"]
    CalcDuration --> CheckDuration{"duration < 600秒<br/>（10分未満）？"}

    CheckDuration -- "Yes" --> BulkDecode["一括デコード<br/>flac -d -c --skip --until<br/>→ WAV 全量メモリ読込"]
    BulkDecode --> BulkMD5["MD5(raw_pcm) 算出"]
    BulkMD5 --> BulkResample["pcm → float32 変換<br/>→ 44.1kHz リサンプリング (soxr)"]
    BulkResample --> Return["(audio_44100, md5_hash) 返却"]

    CheckDuration -- "No (10分以上)" --> StreamDecode["ストリーミングデコード開始<br/>flac -d -c (subprocess.PIPE)"]
    StreamDecode --> ReadHeader["先頭 4096 bytes バッファリング<br/>→ WAV ヘッダパース"]
    ReadHeader --> StreamLoop["2秒分ブロック単位で<br/>stdout から逐次読出し"]
    StreamLoop --> StreamMD5["ブロック毎に MD5 Engine へ<br/>インクリメンタル更新"]
    StreamMD5 --> StreamMore{"次のブロック<br/>あり？"}
    StreamMore -- "Yes" --> StreamLoop
    StreamMore -- "No" --> MergePCM["全 PCM チャンク結合<br/>→ float32 変換<br/>→ 44.1kHz リサンプリング"]
    MergePCM --> Return
```

### 設計意図

| 項目 | 10分未満 | 10分以上（DJミックス等） |
|:---|:---|:---|
| **デコード方式** | 一括メモリ読込 | ストリーミング (2秒ブロック) |
| **MD5 算出** | 一括 `hashlib.md5(raw_pcm)` | インクリメンタル `md5_engine.update(block)` |
| **メリット** | シンプル＆高速 | RAM ピーク使用量を抑制 |

### WAV ヘッダパース (`parse_wav_header`)

- RIFF/WAVE チャンク走査で `fmt ` と `data` チャンクを探索
- `WAVE_FORMAT_EXTENSIBLE` (`0xFFFE`) の SubFormat GUID からも実フォーマットを正確に取得
- 16bit / 24bit / 32bit PCM Integer および 32bit / 64bit IEEE Float に対応
