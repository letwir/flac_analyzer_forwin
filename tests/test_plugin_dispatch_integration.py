"""
tests/test_plugin_dispatch_integration.py
=========================================
P1-M3-S3: common_dispatch.py のステム別プラグインルーティングおよび
4プラグイン (voice_cpp, audio_cutoff_lufs, psychoacoustics, structure_ssm) 統合テストですわ！
"""

import os
import sys
import unittest

import numpy as np

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from analyzer import (
    AudioContext,
    build_stem_extractor_morphism,
    librosa_extractor,
    resolve_stem_plugins_pure,
)


class TestPluginDispatchIntegration(unittest.TestCase):
    def test_resolve_stem_plugins_matrix(self) -> None:
        self.assertEqual(
            resolve_stem_plugins_pure("mix"),
            ["voice_cpp", "audio_cutoff_lufs", "psychoacoustics", "structure"],
        )
        self.assertEqual(
            resolve_stem_plugins_pure("vocals"),
            ["voice_cpp", "audio_cutoff_lufs", "psychoacoustics"],
        )
        self.assertEqual(
            resolve_stem_plugins_pure("bass"),
            ["audio_cutoff_lufs"],
        )
        # execute=false 安全弁で全停止すること
        self.assertEqual(resolve_stem_plugins_pure("mix", {"execute": False, "plugins": {}}), [])

    def test_mix_and_vocals_and_bass_dispatch_output(self) -> None:
        sr = 22050
        t = np.linspace(0.0, 2.0, sr * 2, endpoint=False, dtype=np.float32)
        y = (0.4 * np.sin(2.0 * np.pi * 220.0 * t) + 0.1 * np.sin(2.0 * np.pi * 440.0 * t)).astype(np.float32)

        for stem_name in ("mix", "vocals", "bass"):
            ctx = AudioContext(y=y, sr=sr, source=stem_name)
            try:
                raw = build_stem_extractor_morphism(stem_name, librosa_extractor).run(ctx)
                pg = raw.to_postgres_dict("track_dispatch")
                scalars = pg["scalars"]
                self.assertIn("audio_cutoff_lufs", scalars)
                if stem_name == "mix":
                    self.assertIn("voice_cpp", scalars)
                    self.assertIn("psychoacoustics", scalars)
                    self.assertIn("structure_ssm", scalars)
                if stem_name == "vocals":
                    self.assertIn("voice_cpp", scalars)
                    self.assertIn("psychoacoustics", scalars)
                    self.assertNotIn("structure_ssm", scalars)
                if stem_name == "bass":
                    self.assertNotIn("voice_cpp", scalars)
                    self.assertNotIn("structure_ssm", scalars)
            finally:
                ctx.clear()


if __name__ == "__main__":
    unittest.main()
