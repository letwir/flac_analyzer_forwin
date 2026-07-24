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

def main():
    setup_logger()
    logger = logging.getLogger("CueWorker")

    parser = argparse.ArgumentParser()
    parser.add_argument("--flac-path", required=True, help="Target FLAC file path")
    args = parser.parse_args()

    filepath = os.path.abspath(args.flac_path)
    if not os.path.exists(filepath):
        logger.error(f"File not found: {filepath}")
        sys.exit(1)

    try:
        handle = build_flac_handle(filepath)
        
        album = handle.tags.get("album", "")
        if isinstance(album, list):
            album = album[0] if album else ""
            
        album_artist = handle.tags.get("albumartist", handle.tags.get("album artist", ""))
        if isinstance(album_artist, list):
            album_artist = album_artist[0] if album_artist else ""

        tracks = []
        for slice_item in handle.slices:
            tracks.append({
                "track_number": slice_item.track_number,
                "start_sample": slice_item.start_sample,
                "end_sample": slice_item.end_sample,
                "title": slice_item.title,
                "artist": slice_item.artist or handle.tags.get("artist", "")
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
        logger.exception(f"Failed to parse CUE/FLAC tags for {filepath}")
        sys.exit(1)

if __name__ == "__main__":
    main()
