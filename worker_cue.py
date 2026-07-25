"""
worker_cue.py
=============
Goから起動される CUE/FLAC タグ自動解析インスペクタですわ。
指定された FLAC ファイルから CUE シート境界および VorbisComment メタデータを抽出し、
トラック（スライス）単位のパース結果を JSON で標準出力して exit 0 しますの。
"""

import argparse
import json
import logging
import sys
import os

from flac_decode import build_flac_handle

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [%(name)s] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)]
    )

def preserve_tag_value(val):
    if isinstance(val, list):
        clean_list = [str(x) for x in val if str(x).strip()]
        if len(clean_list) == 1:
            return clean_list[0]
        elif len(clean_list) > 1:
            return clean_list
        return ""
    return str(val) if val is not None else ""

def main():
    setup_logger()
    logger = logging.getLogger("CueWorker")

    parser = argparse.ArgumentParser()
    parser.add_argument("--flac-path", required=True, help="Target FLAC file path")
    args = parser.parse_args()

    filepath = os.path.abspath(args.flac_path)
    if not os.path.exists(filepath):
        logger.error(f"ファイルが存在いたしませんわ: {filepath}")
        sys.exit(1)

    try:
        handle = build_flac_handle(filepath)
        
        album = preserve_tag_value(handle.tags.get("album", ""))
        album_artist = preserve_tag_value(handle.tags.get("albumartist", handle.tags.get("album artist", "")))

        tracks = []
        for slice_item in handle.slices:
            artist_val = slice_item.artist or handle.tags.get("artist", "")
            tracks.append({
                "track_number": slice_item.track_number,
                "start_sample": slice_item.start_sample,
                "end_sample": slice_item.end_sample,
                "title": preserve_tag_value(slice_item.title),
                "artist": preserve_tag_value(artist_val)
            })

        output = {
            "status": "success",
            "filepath": filepath,
            "album": album,
            "album_artist": album_artist,
            "tracks": tracks
        }
        print(json.dumps(output, ensure_ascii=False))
        sys.exit(0)

    except Exception as e:
        logger.exception(f"{filepath} の CUE/FLAC メタデータ解析に失敗いたしましたわ！")
        sys.exit(1)

if __name__ == "__main__":
    main()
