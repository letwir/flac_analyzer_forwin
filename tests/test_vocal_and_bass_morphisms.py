"""
tests/test_vocal_and_bass_morphisms.py
======================================
P2-M2-S4: voiced_f0_contour_morphism (FMビブラート復元), vocal_character_morphism,
および bass_character_morphism の単体・統合テストですわ！
"""

import os
import sys
import unittest

import numpy as np

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from analyzer import AudioContext, build_stem_extractor_morphism, librosa_extractor
from analyzer.common_pitch_harmony import (
    bass_character_morphism,
    vocal_character_morphism,
    voiced_f0_contour_morphism,
)


class TestVocalAndBassMorphisms(unittest.TestCase):
    def test_fm_vibrato_recovery(self) -> None:
        sr = 22050
        dur = 3.0
        t = np.linspace(0.0, dur, int(sr * dur), endpoint=False, dtype=np.float64)
        # 440Hz に 5.5Hz・±50 cents (約 ±2.93% 周波数変調) のビブラートをかけるわ
        vib_freq = 5.5
        cents_amp = 50.0
        max_dev_hz = 440.0 * (2.0 ** (cents_amp / 1200.0) - 1.0)
        phase = 2.0 * np.pi * (440.0 * t - (max_dev_hz / vib_freq) * np.cos(2.0 * np.pi * vib_freq * t) / (2.0 * np.pi))
        y = np.sin(phase).astype(np.float32)

        res = voiced_f0_contour_morphism(y, sr, fmin_hz=65.41, fmax_hz=2093.0)
        self.assertGreater(res["voiced_ratio"], 0.9)
        self.assertAlmostEqual(res["f0_median_hz"], 440.0, delta=6.0)
        self.assertAlmostEqual(res["vibrato_rate_hz"], 5.5, delta=0.8)
        self.assertGreater(res["vibrato_extent_cents"], 25.0)
        self.assertLess(res["vibrato_extent_cents"], 80.0)

    def test_vocal_formant_sibilance_and_dispatch(self) -> None:
        sr = 22050
        t = np.linspace(0.0, 2.0, sr * 2, endpoint=False, dtype=np.float32)
        y = (
            0.5 * np.sin(2.0 * np.pi * 220.0 * t)
            + 0.4 * np.sin(2.0 * np.pi * 3000.0 * t)
            + 0.2 * np.sin(2.0 * np.pi * 7000.0 * t)
        ).astype(np.float32)

        vp = vocal_character_morphism(y, sr)
        self.assertGreater(vp["singers_formant_ratio"], 0.25)
        self.assertGreater(vp["sibilance_ratio"], 0.05)

        ctx = AudioContext(y=y, sr=sr, source="vocals")
        try:
            pg = build_stem_extractor_morphism("vocals", librosa_extractor).run(ctx).to_postgres_dict("voc_test")
            self.assertIn("vocal_profile", pg["scalars"])
            self.assertGreater(pg["scalars"]["vocal_profile"]["singers_formant_ratio"], 0.25)
        finally:
            ctx.clear()

    def test_bass_sub_vs_harmonic_and_dispatch(self) -> None:
        sr = 22050
        t = np.linspace(0.0, 2.0, sr * 2, endpoint=False, dtype=np.float32)
        sub_y = (0.8 * np.sin(2.0 * np.pi * 45.0 * t)).astype(np.float32)
        atk_y = (0.8 * np.sin(2.0 * np.pi * 110.0 * t) + 0.6 * np.sin(2.0 * np.pi * 550.0 * t)).astype(np.float32)

        sub_prof = bass_character_morphism(sub_y, sr)
        atk_prof = bass_character_morphism(atk_y, sr)
        self.assertGreater(sub_prof["sub_bass_ratio"], 0.9)
        self.assertGreater(atk_prof["mid_bass_ratio"], 0.5)
        self.assertGreater(atk_prof["harmonic_attack_ratio"], 0.25)

        ctx = AudioContext(y=sub_y, sr=sr, source="bass")
        try:
            pg = build_stem_extractor_morphism("bass", librosa_extractor).run(ctx).to_postgres_dict("bass_test")
            self.assertIn("bass_profile", pg["scalars"])
            self.assertGreater(pg["scalars"]["bass_profile"]["sub_bass_ratio"], 0.9)
        finally:
            ctx.clear()


if __name__ == "__main__":
    unittest.main()
