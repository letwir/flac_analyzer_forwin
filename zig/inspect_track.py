"""
zig/inspect_track.py
====================
単一 FLAC ファイルまたは CUE シート付き FLAC ファイルの波形情報、
サンプル数、トラック（CUEスライス）分割情報、VorbisComment タグを即座にインスペクト・表示する治具スクリプトですわ。

使い方:
    python zig/inspect_track.py <flac_path>
"""

import sys
import os
import argparse

# 親ディレクトリを sys.path に追加してプロジェクト内モジュールを安全にロード
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from flac_decode import build_flac_handle
from mutagen.flac import FLAC

def inspect_flac(target_file: str):
    if not os.path.exists(target_file):
        print(f"Error: File not found: {target_file}", file=sys.stderr)
        sys.exit(1)

    print(f"=== Inspecting FLAC: {target_file} ===")
    
    # 1. Mutagen VorbisComment タグ確認
    try:
        audio = FLAC(target_file)
        print("\n--- VorbisComment Tags ---")
        for k, v in audio.items():
            if k.startswith("LIBROSA_") or k.startswith("ESSENTIA_") or k in ["TITLE", "ARTIST", "ALBUM", "ALBUMARTIST", "TRACKNUMBER", "CUESHEET"]:
                v_str = str(v)
                if len(v_str) > 100:
                    v_str = v_str[:97] + "..."
                print(f"  {k}: {v_str}")
    except Exception as e:
        print(f"Warning: Failed to read Mutagen tags: {e}", file=sys.stderr)

    # 2. CUE / Slices ハンドル構築
    try:
        handle = build_flac_handle(target_file)
        duration_total_sec = handle.total_samples / handle.sample_rate if handle.sample_rate > 0 else 0
        print(f"\n--- Stream & Slices Info ---")
        print(f"  Total Samples: {handle.total_samples:,}")
        print(f"  Sample Rate:   {handle.sample_rate} Hz")
        print(f"  Channels:      {handle.channels}")
        print(f"  Total Duration:{duration_total_sec:.2f}s ({duration_total_sec/60:.2f}min)")
        print(f"  Total Slices:  {len(handle.slices)}")

        print("\n--- Tracks (CUE Slices) ---")
        for s in handle.slices:
            end_samp = s.end_sample if s.end_sample > 0 else handle.total_samples
            duration_sec = (end_samp - s.start_sample) / handle.sample_rate
            print(f"  Track {s.track_number:02d}: '{s.title}' by '{s.artist}' | Samples [{s.start_sample:,} - {end_samp:,}] | Duration: {duration_sec:.2f}s ({duration_sec/60:.2f}min)")
    except Exception as e:
        print(f"Error building FLAC handle: {e}", file=sys.stderr)
        sys.exit(1)

def main():
    parser = argparse.ArgumentParser(description="Inspect FLAC audio properties, CUE tracks, and VorbisComment tags.")
    parser.add_argument("flac_path", nargs="?", help="Path to FLAC audio file")
    args = parser.parse_args()

    if not args.flac_path:
        # Default fallback to testFLAC if available
        test_flac = os.path.join(PROJECT_ROOT, "testFLAC", "01_08_Reply.flac")
        if os.path.exists(test_flac):
            args.flac_path = test_flac
        else:
            print("Usage: python zig/inspect_track.py <flac_path>", file=sys.stderr)
            sys.exit(1)

    inspect_flac(args.flac_path)

if __name__ == "__main__":
    main()
