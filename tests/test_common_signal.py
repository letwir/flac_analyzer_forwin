"""
tests/test_common_signal.py
===========================
P1-M1-S4: analyzer/common_signal.py の純粋射、rms/centroid 実測 min/median 結線、
および vocals ステムの chord_sequence バグ修正の回帰テストですわ！
"""

import os
import sys
import unittest

import numpy as np

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from analyzer.common_signal import (
    decay_slope_morphism,
    envelope_stats7_morphism,
    subband_energy_morphism,
    xcorr_lag_morphism,
)
from analyzer.core import AudioContext
from analyzer.essentia_dsp import _calc_chord_sequence
from analyzer.librosa_dsp import librosa_extractor


class TestCommonSignalMorphisms(unittest.TestCase):
    def test_envelope_stats7_empty_and_nan(self) -> None:
        empty = envelope_stats7_morphism(np.array([], dtype=np.float32))
        self.assertEqual(empty["min"], 0.0)
        self.assertEqual(empty["median"], 0.0)
        self.assertEqual(empty["entropy"], 0.0)

        arr = np.array([1.0, 2.0, np.nan, 4.0, 10.0], dtype=np.float32)
        stats = envelope_stats7_morphism(arr)
        self.assertAlmostEqual(stats["min"], 0.0, places=5)
        self.assertAlmostEqual(stats["median"], 2.0, places=5)
        self.assertAlmostEqual(stats["max"], 10.0, places=5)
        self.assertGreater(stats["entropy"], 0.0)

    def test_subband_energy_pure_sine(self) -> None:
        sr = 44100
        t = np.linspace(0.0, 1.0, sr, endpoint=False, dtype=np.float32)
        y = np.sin(2.0 * np.pi * 100.0 * t).astype(np.float32)
        res = subband_energy_morphism(y, sr, {"low": (50.0, 200.0), "high": (2000.0, 8000.0)})
        self.assertGreater(res["low_ratio"], 0.95)
        self.assertLess(res["high_ratio"], 0.01)

    def test_xcorr_lag_known_shift(self) -> None:
        hop_sec = 0.01
        n = 200
        t = np.arange(n, dtype=np.float32)
        env_a = np.exp(-0.5 * ((t - 50.0) / 3.0) ** 2)
        env_b = np.exp(-0.5 * ((t - 53.0) / 3.0) ** 2)
        res = xcorr_lag_morphism(env_a, env_b, hop_sec=hop_sec, max_lag_sec=0.1)
        # env_b が 3 フレーム (30ms) 遅れているため、正のピークラグを検出すること
        self.assertGreater(res["peak_corr"], 0.9)
        self.assertAlmostEqual(abs(res["best_lag_ms"]), 30.0, delta=5.0)

    def test_decay_slope_morphism(self) -> None:
        sr = 44100
        hop = 512
        t = np.arange(80, dtype=np.float32) * (hop / sr)
        env = np.exp(-4.0 * t)
        res = decay_slope_morphism(env, sr=sr, hop_length=hop)
        self.assertLess(res["decay_db_per_sec"], -10.0)
        self.assertGreater(res["sustain_ratio"], 0.0)
        self.assertLess(res["sustain_ratio"], 1.0)

    def test_librosa_extractor_nonzero_min_median_and_vocals_chord(self) -> None:
        sr = 22050
        t = np.linspace(0.0, 2.0, sr * 2, endpoint=False, dtype=np.float32)
        # A minor (A3=220, C4=261.63, E4=329.63) + 振幅変調で min/median を非ゼロにするわ
        env = 0.3 + 0.7 * (0.5 + 0.5 * np.sin(2.0 * np.pi * 1.5 * t))
        y = env * (
            np.sin(2.0 * np.pi * 220.0 * t)
            + np.sin(2.0 * np.pi * 261.63 * t)
            + np.sin(2.0 * np.pi * 329.63 * t)
        )
        ctx = AudioContext(y=y.astype(np.float32), sr=sr, source="vocals")
        try:
            raw = librosa_extractor.run(ctx)
            self.assertGreater(raw.rms_min, 0.0)
            self.assertGreater(raw.rms_median, raw.rms_min)
            self.assertGreater(raw.centroid_min, 0.0)
            self.assertGreater(raw.centroid_median, 0.0)

            pg = raw.to_postgres_dict("test_track")
            self.assertGreater(pg["scalars"]["rms_min"], 0.0)
            self.assertGreater(pg["scalars"]["centroid_median"], 0.0)

            chords = _calc_chord_sequence(ctx)
            self.assertEqual(len(chords), 32)
            # "vocal" タイポ時は無条件で "C" のみになっていたため、Am が正しく推論されることを確認するわ
            self.assertIn("Am", chords)
        finally:
            ctx.clear()


if __name__ == "__main__":
    unittest.main()
