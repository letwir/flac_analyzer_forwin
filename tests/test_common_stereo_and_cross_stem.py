"""
tests/test_common_stereo_and_cross_stem.py
==========================================
P1-M2-S4: common_stereo.py と common_cross_stem.py の不変条件テスト
および RawFeatures.to_postgres_dict() シリアライズ検証ですわ！
"""

import os
import sys
import unittest

import numpy as np

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from analyzer.common_cross_stem import cross_stem_foundation_morphism
from analyzer.common_stereo import mono_downmix_morphism, stereo_field_morphism
from analyzer.core import AudioContext
from analyzer.librosa_dsp import librosa_extractor


class TestCommonStereoAndCrossStem(unittest.TestCase):
    def test_stereo_invariants_center_hardpan_outofphase(self) -> None:
        sr = 44100
        t = np.linspace(0.0, 1.0, sr, endpoint=False, dtype=np.float32)
        sine = np.sin(2.0 * np.pi * 440.0 * t).astype(np.float32)

        # 1. Center Mono: L == R -> width == 0, pan == 0, corr == 1
        center = np.stack([sine, sine], axis=0)
        res_c = stereo_field_morphism(center, sr)
        self.assertAlmostEqual(res_c["stereo_width"], 0.0, places=5)
        self.assertAlmostEqual(res_c["pan_mean"], 0.0, places=5)
        self.assertAlmostEqual(res_c["lr_correlation"], 1.0, places=5)
        self.assertEqual(len(res_c["pan_seq_32"]), 32)
        self.assertEqual(len(res_c["width_seq_32"]), 32)

        # 2. Hard Right Pan: L == 0, R == sine -> pan == +1.0
        hard_r = np.stack([np.zeros_like(sine), sine], axis=0)
        res_r = stereo_field_morphism(hard_r, sr)
        self.assertAlmostEqual(res_r["pan_mean"], 1.0, places=4)
        self.assertAlmostEqual(res_r["stereo_width"], 0.5, places=3)

        # 3. Out-of-phase: L == sine, R == -sine -> width == 1.0, corr == -1.0
        anti = np.stack([sine, -sine], axis=0)
        res_a = stereo_field_morphism(anti, sr)
        self.assertAlmostEqual(res_a["stereo_width"], 1.0, places=5)
        self.assertAlmostEqual(res_a["lr_correlation"], -1.0, places=5)

        # 4. Downmix morphism returns contiguous 1D float32
        mono = mono_downmix_morphism(center)
        self.assertEqual(mono.ndim, 1)
        self.assertEqual(mono.dtype, np.float32)
        self.assertTrue(mono.flags["C_CONTIGUOUS"])

    def test_cross_stem_foundation_energy_share_and_residual(self) -> None:
        sr = 44100
        t = np.linspace(0.0, 1.0, sr, endpoint=False, dtype=np.float32)
        vocals = 0.5 * np.sin(2.0 * np.pi * 880.0 * t)
        bass = 0.5 * np.sin(2.0 * np.pi * 110.0 * t)
        drums = 0.25 * np.sin(2.0 * np.pi * 60.0 * t)
        guitar = np.zeros_like(t)
        piano = np.zeros_like(t)
        other = np.zeros_like(t)

        stems = {
            "vocals": vocals,
            "bass": bass,
            "drums": drums,
            "guitar": guitar,
            "piano": piano,
            "other": other,
        }
        mix = vocals + bass + drums
        cs = cross_stem_foundation_morphism(mix, stems, sr)
        total_share = sum(v["energy_share"] for v in cs["per_stem"].values())
        self.assertAlmostEqual(total_share, 1.0, places=4)
        self.assertGreater(cs["residual_snr_db"], 40.0)
        self.assertEqual(len(cs["arrangement_arc_32"]["active_stem_count_seq_32"]), 32)
        self.assertTrue(all(c == 3 for c in cs["arrangement_arc_32"]["active_stem_count_seq_32"]))

        # RawFeatures.to_postgres_dict() へのステレオ/クロスステム/SNR シリアライズ確認
        st_meta = stereo_field_morphism(np.stack([vocals, vocals], axis=0), sr)
        ctx = AudioContext(
            y=vocals.astype(np.float32),
            sr=sr,
            source="vocals",
            snr=cs["per_stem"]["vocals"]["snr_db"],
            stereo_meta=st_meta,
            cross_stem_meta=cs["per_stem"]["vocals"],
        )
        try:
            raw = librosa_extractor.run(ctx)
            pg = raw.to_postgres_dict("track_cs_test")
            self.assertIn("stereo", pg["scalars"])
            self.assertIn("cross_stem", pg["scalars"])
            self.assertGreater(pg["scalars"]["snr"], 30.0)
            self.assertIn("pan_seq_32", pg["sequences"])
            self.assertIn("width_seq_32", pg["sequences"])
        finally:
            ctx.clear()


if __name__ == "__main__":
    unittest.main()
