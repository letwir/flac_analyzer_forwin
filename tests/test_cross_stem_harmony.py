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


if __name__ == "__main__":
    unittest.main()
