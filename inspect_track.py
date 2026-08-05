import soundfile as sf
from mutagen.flac import FLAC
from flac_decode import build_flac_handle

target_file = r"M:\Music\album\CLASSIC\Zino Francescatti [2013] フォーレ-ヴァイオリン・ソナタ第1番&第2番-ピアノ四重奏曲第1番　他 [-13].flac"

handle = build_flac_handle(target_file)
print(f"Total Samples: {handle.total_samples}, SR: {handle.sample_rate}")
print(f"Total Slices (Tracks): {len(handle.slices)}")

for s in handle.slices:
    duration_sec = (s.end_sample - s.start_sample) / handle.sample_rate
    print(f"Track {s.track_number:02d}: '{s.title}' | Start: {s.start_sample} | End: {s.end_sample} | Duration: {duration_sec:.2f}s ({duration_sec/60:.2f}min)")
