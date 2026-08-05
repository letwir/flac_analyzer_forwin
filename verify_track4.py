import sys
import os
import time
import logging
import traceback

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")

from flac_decode import build_flac_handle, decode_slice_pcm
import models
from analyzer import StemAudioAnalyzer

target_file = r"M:\Music\album\CLASSIC\Zino Francescatti [2013] フォーレ-ヴァイオリン・ソナタ第1番&第2番-ピアノ四重奏曲第1番　他 [-13].flac"
track_num = 4

print(f"=== Single Track Verification: {target_file} (Track {track_num}) ===")

# 1. FLAC Handle Building & Track Slicing
handle = build_flac_handle(target_file)
target_slice = None
for s in handle.slices:
    if s.track_number == track_num:
        target_slice = s
        break

if not target_slice:
    print(f"Track {track_num} not found in slices!")
    sys.exit(1)

print(f"Track Slice Info: '{target_slice.title}' | Start: {target_slice.start_sample} | End: {target_slice.end_sample}")

# 2. Decode PCM
print("[Step 1] Decoding PCM...")
y, sr = decode_slice_pcm(handle.filepath, target_slice.start_sample, target_slice.end_sample)
print(f"Decoded PCM Shape: {y.shape}, Sample Rate: {sr}, Duration: {y.shape[1]/sr:.2f}s")

# 3. Demucs Separation
print("[Step 2] Testing Demucs separation...")
t0 = time.time()
try:
    models.init_global_demucs(use_dml=False)
    stem_context = models.GLOBAL_DEMUCS.separate(y, sr)
    print(f"Demucs separation SUCCESS! Time taken: {time.time() - t0:.2f}s")
    for name, ctx in stem_context.stems.items():
        print(f"  Stem '{name}': shape={ctx.y.shape}, sr={ctx.sr}")
except Exception as e:
    print(f"Demucs separation FAILED: {e}")
    traceback.print_exc()
    sys.exit(1)

# 4. Librosa Feature Extraction
print("[Step 3] Testing Librosa feature extraction on stems...")
t0 = time.time()
try:
    analyzer = StemAudioAnalyzer(stem_context)
    features = analyzer.to_dict()
    print(f"Librosa feature extraction SUCCESS! Time taken: {time.time() - t0:.2f}s")
    print(f"Extracted keys: {list(features.keys())}")
    for k, v in features.items():
        if isinstance(v, dict):
            print(f"  Stems analyzed: '{k}' -> scalar keys count: {len(v.get('scalars', {}))}, sequence keys count: {len(v.get('sequences', {}))}")
except Exception as e:
    print(f"Librosa feature extraction FAILED: {e}")
    traceback.print_exc()
    sys.exit(1)

print("=== ALL STEPS PASSED SUCCESSFULLY FOR SINGLE TRACK ===")
