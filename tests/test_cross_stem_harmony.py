"""
tests/test_cross_stem_harmony.py
================================
P3-M1-S3: 純和声合成クロマ射 (harmonic_bus_chroma_morphism) と
クロスステム和声テンション整合射 (cross_stem_harmonic_tension_morphism) の検証テストですわ！
"""

import os
import sys
import unittest

import numpy as np

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from analyzer.common_cross_stem import cross_stem_foundation_morphism
from analyzer.common_pitch_harmony import (
    cross_stem_harmonic_tension_morphism,
    harmonic_bus_chroma_morphism,
)


class TestCrossStemHarmony(unittest.TestCase):
    def test_harmonic_bus_and_tension_ratios(self) -> None:
        sr = 22050
        t = np.linspace(0.0, 2.0, sr * 2, endpoint=False, dtype=np.float32)

        # C Major Triad (C4=261.63, E4=329.63, G4=392.00) on piano & guitar
        piano = (
            0.35 * np.sin(2.0 * np.pi * 261.63 * t)
            + 0.30 * np.sin(2.0 * np.pi * 329.63 * t)
            + 0.30 * np.sin(2.0 * np.pi * 392.00 * t)
        ).astype(np.float32)
        guitar = (0.25 * np.sin(2.0 * np.pi * 261.63 * t)).astype(np.float32)
        rng = np.random.default_rng(42)
        drums_noise = (0.5 * rng.standard_normal(t.size)).astype(np.float32)

        bass_root_c = (0.5 * np.sin(2.0 * np.pi * 65.41 * t)).astype(np.float32)  # C2
        bass_slash_e = (0.5 * np.sin(2.0 * np.pi * 82.41 * t)).astype(np.float32)  # E2 (C/E)
        vocals_g4 = (0.5 * np.sin(2.0 * np.pi * 392.00 * t)).astype(np.float32)  # Chord tone G4
        vocals_fs4 = (0.5 * np.sin(2.0 * np.pi * 369.99 * t)).astype(np.float32)  # Tension F#4

        bus = harmonic_bus_chroma_morphism(
            {"piano": piano, "guitar": guitar, "bass": bass_root_c, "drums": drums_noise, "vocals": vocals_fs4},
            sr,
        )
        self.assertEqual(bus["harmonic_bus_key"], "C")
        self.assertEqual(bus["harmonic_bus_scale"], "major")
        self.assertIn("C", bus["harmonic_bus_chord_seq_32"])

        t_root_chord = cross_stem_harmonic_tension_morphism(bass_root_c, vocals_g4, bus["harmonic_chroma"], sr)
        t_slash_tens = cross_stem_harmonic_tension_morphism(bass_slash_e, vocals_fs4, bus["harmonic_chroma"], sr)
        self.assertGreater(t_root_chord["bass_root_agreement_ratio"], 0.7)
        self.assertLess(t_slash_tens["bass_root_agreement_ratio"], 0.3)
        self.assertGreater(t_root_chord["vocal_chord_tone_ratio"], 0.7)
        self.assertGreater(t_slash_tens["vocal_tension_ratio"], 0.7)

        mix = piano + guitar + bass_root_c + vocals_g4 + drums_noise
        cs = cross_stem_foundation_morphism(
            mix,
            {"piano": piano, "guitar": guitar, "bass": bass_root_c, "vocals": vocals_g4, "drums": drums_noise},
            sr,
        )
        self.assertEqual(cs["harmonic_bus"]["harmonic_bus_key"], "C")
        self.assertGreater(cs["harmonic_tension"]["bass_root_agreement_ratio"], 0.7)

    def test_multistem_ssm_includes_harmonic_chroma_with_flat_energy(self) -> None:
        from analyzer.structure_ssm import multistem_structure_ssm_morphism

        sr = 22050
        n = sr * 2
        half = n // 2
        t1 = np.linspace(0.0, 1.0, half, endpoint=False, dtype=np.float32)
        t2 = np.linspace(1.0, 2.0, n - half, endpoint=False, dtype=np.float32)
        # 音量 (RMS) は全区間一定のまま、後半で C major -> F# major へ転調させるわ
        c_maj = 0.3 * (np.sin(2.0 * np.pi * 261.63 * t1) + np.sin(2.0 * np.pi * 329.63 * t1) + np.sin(2.0 * np.pi * 392.00 * t1))
        fs_maj = 0.3 * (np.sin(2.0 * np.pi * 369.99 * t2) + np.sin(2.0 * np.pi * 466.16 * t2) + np.sin(2.0 * np.pi * 554.37 * t2))
        piano = np.concatenate([c_maj, fs_maj]).astype(np.float32)
        ssm_res = multistem_structure_ssm_morphism(piano, {"piano": piano}, sr)
        self.assertEqual(ssm_res["state_dim"], 18)
        self.assertTrue(ssm_res["chroma_included"])
        self.assertGreater(ssm_res["structural_complexity"], 0.0)
        # 転調境界付近 (中間フレーム) で novelty ピークが立っていること
        nov = ssm_res["novelty_seq_32"]
        self.assertGreater(max(nov[10:22]), min(nov) + 0.05)


if __name__ == "__main__":
    unittest.main()
