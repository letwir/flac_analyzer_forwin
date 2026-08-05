import sys
import os
import time
import logging
import traceback
import numpy as np

logging.basicConfig(level=logging.INFO, format="[%(levelname)s] [%(name)s] %(message)s")

from flac_decode import build_flac_handle, process_slice_with_seq_safety
import models
from analyzer import AudioContext, STEM_CONFIGS, librosa_extractor

target_file = r"M:\Music\album\CLASSIC\Zino Francescatti [2013] フォーレ-ヴァイオリン・ソナタ第1番&第2番-ピアノ四重奏曲第1番　他 [-13].flac"
track_num = 4

print(f"=== Single Track Verification: {target_file} (Track {track_num}) ===", flush=True)

# 1. FLAC Handle Building & Track Slicing
handle = build_flac_handle(target_file)
target_slice = None
for s in handle.slices:
    if s.track_number == track_num:
        target_slice = s
        break

if not target_slice:
    print(f"Track {track_num} not found in slices!", flush=True)
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
