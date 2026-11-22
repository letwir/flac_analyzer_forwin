"""
tests/test_instrument_specific_morphisms.py
===========================================
P2-M3-S4: drums 帯域オンセット分解、Drums×Bass リズムポケット/ダッキング、
および guitar / piano / other 楽器特化純粋射の単体・統合テストですわ！
"""

import os
import sys
import unittest

import numpy as np

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from analyzer import AudioContext, build_stem_extractor_morphism, librosa_extractor
from analyzer.common_cross_stem import cross_stem_foundation_morphism, rhythm_pocket_and_ducking_morphism
from analyzer.common_pitch_harmony import harmonic_instrument_character_morphism
from analyzer.common_signal import drum_band_onset_morphism


class TestInstrumentSpecificMorphisms(unittest.TestCase):
    def test_drum_band_onset_kick_vs_hihat(self) -> None:
        sr = 22050
        dur = 2.0
        n = int(sr * dur)
        t = np.linspace(0.0, dur, n, endpoint=False, dtype=np.float32)
        env = np.zeros(n, dtype=np.float32)
        # 0.5 秒ごとに量子化された 4 発の打撃 (2 hits/sec)
        for beat_sec in (0.1, 0.6, 1.1, 1.6):
            idx = int(beat_sec * sr)
            decay = np.exp(-np.arange(min(2200, n - idx), dtype=np.float32) / 400.0)
            env[idx : idx + decay.size] += decay

        kick_y = env * np.sin(2.0 * np.pi * 65.0 * t)
        hat_y = env * np.sin(2.0 * np.pi * 9000.0 * t)

        k_prof = drum_band_onset_morphism(kick_y, sr)
        h_prof = drum_band_onset_morphism(hat_y, sr)
        self.assertGreater(k_prof["kick_energy_ratio"], 0.85)
        self.assertGreater(k_prof["kick_hits_per_sec"], 1.5)
        self.assertLess(k_prof["human_feel_std_ms"], 15.0)
        self.assertGreater(h_prof["hihat_energy_ratio"], 0.85)
        self.assertGreater(h_prof["hihat_hits_per_sec"], 1.5)

        ctx = AudioContext(y=kick_y.astype(np.float32), sr=sr, source="drums")
        try:
            pg = build_stem_extractor_morphism("drums", librosa_extractor).run(ctx).to_postgres_dict("drum_test")
            self.assertIn("drum_profile", pg["scalars"])
            self.assertGreater(pg["scalars"]["drum_profile"]["kick_energy_ratio"], 0.85)
        finally:
            ctx.clear()

    def test_rhythm_pocket_and_sidechain_ducking(self) -> None:
        sr = 22050
        dur = 2.0
        n = int(sr * dur)
        t = np.linspace(0.0, dur, n, endpoint=False, dtype=np.float32)
        kick_env = np.zeros(n, dtype=np.float32)
        for beat_sec in (0.2, 0.7, 1.2, 1.7):
            idx = int(beat_sec * sr)
            pulse = np.exp(-np.arange(min(2500, n - idx), dtype=np.float32) / 500.0)
            kick_env[idx : idx + pulse.size] += pulse

        drums_y = kick_env * np.sin(2.0 * np.pi * 65.0 * t)
        bass_sync = kick_env * np.sin(2.0 * np.pi * 80.0 * t)
        bass_ducked = (1.0 - 0.9 * np.clip(kick_env, 0.0, 1.0)) * np.sin(2.0 * np.pi * 80.0 * t)

        sync_res = rhythm_pocket_and_ducking_morphism(drums_y, bass_sync, sr)
        duck_res = rhythm_pocket_and_ducking_morphism(drums_y, bass_ducked, sr)
        self.assertGreater(sync_res["pocket_sync_score"], 0.75)
        self.assertGreater(duck_res["sidechain_ducking_score"], 0.75)

        cs = cross_stem_foundation_morphism(drums_y + bass_sync, {"drums": drums_y, "bass": bass_sync}, sr)
        self.assertIn("rhythm_pocket", cs)
        self.assertGreater(cs["rhythm_pocket"]["pocket_sync_score"], 0.75)

    def test_guitar_piano_other_character_morphisms(self) -> None:
        sr = 22050
        t = np.linspace(0.0, 1.5, int(sr * 1.5), endpoint=False, dtype=np.float32)

        # Guitar: clean vs distorted (hard clipped)
        clean_gtr = (0.4 * np.sin(2.0 * np.pi * 220.0 * t)).astype(np.float32)
        dist_gtr = np.tanh(12.0 * clean_gtr).astype(np.float32)
        g_clean = harmonic_instrument_character_morphism(clean_gtr, sr, "guitar")
        g_dist = harmonic_instrument_character_morphism(dist_gtr, sr, "guitar")
        self.assertGreater(g_dist["distortion_index"], g_clean["distortion_index"])

        # Piano: mono note vs C-major triad
        mono_pno = (0.5 * np.sin(2.0 * np.pi * 440.0 * t)).astype(np.float32)
        triad_pno = (
            0.3 * np.sin(2.0 * np.pi * 261.63 * t)
            + 0.3 * np.sin(2.0 * np.pi * 329.63 * t)
            + 0.3 * np.sin(2.0 * np.pi * 392.00 * t)
        ).astype(np.float32)
        p_mono = harmonic_instrument_character_morphism(mono_pno, sr, "piano")
        p_triad = harmonic_instrument_character_morphism(triad_pno, sr, "piano")
        self.assertGreater(p_triad["polyphony_entropy"], p_mono["polyphony_entropy"])

        # Other: sustained pad vs dispatch serialization
        pad_other = (0.5 * np.sin(2.0 * np.pi * 330.0 * t)).astype(np.float32)
        o_pad = harmonic_instrument_character_morphism(pad_other, sr, "other")
        self.assertGreater(o_pad["harmonic_percussive_ratio"], 0.5)
        self.assertGreater(o_pad["pad_sustain_score"], 0.9)

        ctx = AudioContext(y=dist_gtr, sr=sr, source="guitar")
        try:
            pg = build_stem_extractor_morphism("guitar", librosa_extractor).run(ctx).to_postgres_dict("gtr_test")
            self.assertIn("instrument_profile", pg["scalars"])
            self.assertGreater(pg["scalars"]["instrument_profile"]["distortion_index"], 0.2)
        finally:
            ctx.clear()


if __name__ == "__main__":
    unittest.main()
