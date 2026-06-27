import sys
import re
import json
from mutagen.flac import FLAC

# UTF-8 出力の設定
sys.stdout.reconfigure(encoding='utf-8')

def test_meta_merge():
    file_path = r"testFLAC\01_01_修羅日記.flac"
    print(f"テストファイルをロードしますわ: {file_path}")
    
    meta = FLAC(file_path)
    
    # 1. pipeline.py と同様の raw_tags 構築
    raw_tags = {}
    for k, v in meta.items():
        val_list = [str(x) for x in v]
        key_lower = k.lower()
        if len(val_list) == 1:
            raw_tags[key_lower] = val_list[0]
        elif len(val_list) == 0:
            raw_tags[key_lower] = ""
        else:
            raw_tags[key_lower] = val_list

    print(f"総タグ数: {len(raw_tags)} 個を抽出いたしましたわ。")
    
    # トラックタグの正規表現
    TRACK_TAG_PAT = re.compile(r"^(?:cue_)?track_?(\d+)_(.+)$", re.IGNORECASE)

    # Cuesheet があった場合を想定して、トラック1用の track_meta を構築してみますわ
    num = 1
    track_meta = {}
    for k, v in raw_tags.items():
        m = TRACK_TAG_PAT.match(k)
        if m:
            tag_track_num = int(m.group(1))
            tag_name = m.group(2)
            # 自トラックの個別タグであれば、プレフィックスを除いたキーでマージ
            if tag_track_num == num:
                track_meta[tag_name] = v
        else:
            # ファイル共通タグ
            track_meta[k] = v

    print("\n--- [検証結果: トラック1用のマージ後メタデータ (一部抜粋)] ---")
    # 検証のために、いくつか特徴的なキーを出力してみますわ
    target_keys = ["album", "albumartistsort", "created", "genre", "title", "artist", "event", "composer"]
    
    # 全てのキーを表示（量が多いのでインデント付きのJSONで出力しますわ）
    # ただし、demucs_* や librosa_* などの大量の特徴量タグは多すぎるので除外してスッキリ見せますの
    clean_meta = {k: v for k, v in track_meta.items() if not (k.startswith("librosa_") or k.startswith("demucs_") or k.startswith("essentia_"))}
    
    print(json.dumps(clean_meta, indent=2, ensure_ascii=False))

if __name__ == "__main__":
    test_meta_merge()
