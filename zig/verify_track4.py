"""
zig/verify_track4.py
====================
単一 FLAC ファイル（または指定トラック）のデコード、Demucs 分離、Librosa 特徴量抽出を
単体プロセス内で一気通貫実行して検証する治具スクリプトですわ！

使い方:
    python zig/verify_track4.py [<flac_path>] [--track <track_num>]
"""

import sys
import os
import time
import logging
import traceback
import argparse

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

logging.basicConfig(level=logging.INFO, format="[%(levelname)s] [%(name)s] %(message)s")

from flac_decode import build_flac_handle, process_slice_with_seq_safety
import models
from analyzer import AudioContext, STEM_CONFIGS, librosa_extractor

def run_verification(target_file: str, track_num: int):
    if not os.path.exists(target_file):
        print(f"Error: File not found: {target_file}", file=sys.stderr)
        sys.exit(1)

    print(f"=== Single Track Verification: {target_file} (Track {track_num}) ===", flush=True)

    # 1. FLAC Handle Building & Track Slicing
    handle = build_flac_handle(target_file)
    target_slice = None
    for s in handle.slices:
        if s.track_number == track_num:
            target_slice = s
            break

    if not target_slice:
        if len(handle.slices) > 0:
            target_slice = handle.slices[0]
            print(f"Track {track_num} not found. Fallback to Track {target_slice.track_number}", flush=True)
        else:
            print("No slices found in file!", flush=True)
            sys.exit(1)

    print(f"Track Slice Info: '{target_slice.title}' | Start: {target_slice.start_sample} | End: {target_slice.end_sample}", flush=True)

    # 2. Decode PCM
    print("[Step 1] Decoding PCM with process_slice_with_seq_safety...", flush=True)
    y, audio_hash = process_slice_with_seq_safety(
        handle.filepath, 
        target_slice.start_sample, 
        target_slice.end_sample, 
        handle.sample_rate, 
        handle.channels
    )
    sr = 44100
    print(f"Decoded PCM Shape: {y.shape}, Sample Rate: {sr}, Duration: {y.shape[1]/sr:.2f}s ({y.shape[1]/sr/60:.2f}min), Hash: {audio_hash}", flush=True)

    # 3. Demucs Separation
    print("[Step 2] Testing Demucs separation...", flush=True)
    t0 = time.time()
    try:
        models.init_global_demucs(use_dml=False)
        stem_context = models.GLOBAL_DEMUCS.separate(y, sr)
        print(f"Demucs separation SUCCESS! Time taken: {time.time() - t0:.2f}s", flush=True)
        for name, ctx in stem_context.stems.items():
            print(f"  Stem '{name}': shape={ctx.y.shape}, sr={ctx.sr}", flush=True)
    except Exception as e:
        print(f"Demucs separation FAILED: {e}", flush=True)
        traceback.print_exc()
        sys.exit(1)

    # 4. Librosa Feature Extraction
    print("[Step 3] Testing Librosa feature extraction on stems...", flush=True)
    t0 = time.time()
    extracted_features = {}
    try:
        for stem_name, stem_audio_ctx in stem_context.stems.items():
            print(f"  Extracting Librosa features for stem [{stem_name}]...", flush=True)
            config = STEM_CONFIGS.get(stem_name, STEM_CONFIGS["other"])
            for prop in config["warmup"]:
                try:
                    _ = getattr(stem_audio_ctx, prop)
                except Exception as e:
                    print(f"    Pre-warming '{prop}' warning: {e}", flush=True)
            raw_features = librosa_extractor.run(stem_audio_ctx)
            extracted_features[stem_name] = raw_features
            stem_audio_ctx.clear()
            print(f"  Stem [{stem_name}] Librosa extraction DONE!", flush=True)

        print(f"Librosa feature extraction SUCCESS! Time taken: {time.time() - t0:.2f}s", flush=True)
    except Exception as e:
        print(f"Librosa feature extraction FAILED: {e}", flush=True)
        traceback.print_exc()
        sys.exit(1)

    print("=== ALL STEPS PASSED SUCCESSFULLY FOR SINGLE TRACK ===", flush=True)

def main():
    parser = argparse.ArgumentParser(description="Verify single track pipeline execution.")
    parser.add_argument("flac_path", nargs="?", help="Path to FLAC audio file")
    parser.add_argument("--track", type=int, default=4, help="Track number to verify (default: 4)")
    args = parser.parse_args()

    target_file = args.flac_path
    if not target_file:
        test_flac = os.path.join(PROJECT_ROOT, "testFLAC", "01_08_Reply.flac")
        if os.path.exists(test_flac):
            target_file = test_flac
            args.track = 1
        else:
            print("Usage: python zig/verify_track4.py [<flac_path>] [--track <track_num>]", file=sys.stderr)
            sys.exit(1)

    run_verification(target_file, args.track)

if __name__ == "__main__":
    main()
