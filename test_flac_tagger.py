"""Tagger contracts and real FLAC round trips; never touch the music library."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import numpy as np
import soundfile as sf
from mutagen.flac import FLAC

from flac_tagger import build_flac_tags


class FlacTaggerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.flac = self.root / "sample.flac"
        self.lib = {"mix": {"scalars": {"bpm": 128, "rms_mean": 0.25,
                                         "contrast": [1.25], "mfcc": [2.5]}},
                    "demucs": {"vocals": {"scalars": {"bpm": 64}}}}
        self.ess = {"ESSENTIA_MOOD_HAPPY_HAPPY": 0.8,
                    "ESSENTIA_MOOD_HAPPY_NON_HAPPY": 0.2}
        self.tensor = {"mix": {"hnr": 12.5}, "demucs": {"vocals": {"hnr": 9.0}}}

    def cli(self, lib=None, extra=()):
        files = []
        for name, data in (("lib", self.lib if lib is None else lib),
                           ("ess", self.ess), ("tensor", self.tensor)):
            path = self.root / (name + ".json")
            path.write_text(json.dumps(data), encoding="utf-8")
            files.append(path)
        return subprocess.run([
            sys.executable, "-B", str(Path(__file__).with_name("flac_tagger.py")),
            "--flac-path", str(self.flac), "--json-path", str(files[0]),
            "--predictions-json-path", str(files[1]), "--tensor-json-path", str(files[2]),
            "--prefix", "CUE_TRACK01", *extra,
        ], capture_output=True, timeout=30)

    def test_raw_and_wrapped_formats(self):
        tags = build_flac_tags(self.lib, self.ess, self.tensor, "CUE_TRACK01")
        self.assertEqual(tags, build_flac_tags(
            {"features": self.lib}, {"predictions": self.ess},
            {"features": self.tensor}, "CUE_TRACK01"))
        self.assertEqual(tags["CUE_TRACK01_LIBROSA_BPM"], "128")
        self.assertEqual(tags["CUE_TRACK01_TENSOR_HNR"], "12.5")
        self.assertEqual(tags["CUE_TRACK01_LIBROSA_CONTRAST_B0"], "125")
        self.assertEqual(tags["CUE_TRACK01_LIBROSA_MFCC00"], "250")
        self.assertEqual(tags["CUE_TRACK01_ESSENTIA_MOOD_HAPPY_HAPPY"], "800")
        self.assertEqual(tags["CUE_TRACK01_ESSENTIA_MOOD_HAPPY_TOP"], "HAPPY")
        self.assertEqual(tags["CUE_TRACK01_DEMUCS_VOCALS_LIBROSA_BPM"], "64")
        self.assertEqual(tags["CUE_TRACK01_DEMUCS_VOCALS_TENSOR_HNR"], "9.0")
        legacy = build_flac_tags(self.lib["mix"], {"predictions": self.ess}, self.tensor["mix"])
        self.assertEqual(legacy["LIBROSA_BPM"], "128")

    def test_flac_round_trip_and_force(self):
        samples = (np.sin(np.arange(4410) * 0.1) * 0.1).astype(np.float32)
        sf.write(self.flac, samples, 44100, subtype="PCM_16")
        original_pcm, _ = sf.read(self.flac, dtype="int16")
        original = FLAC(self.flac)
        original["TITLE"] = "Keep title"
        original["CUE_TRACK02_LIBROSA_BPM"] = "150"
        original["CUE_TRACK01_LIBROSA_BPM"] = "100"
        original.save()
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        tagged = FLAC(self.flac)
        self.assertEqual(tagged["CUE_TRACK01_LIBROSA_BPM"], ["100"])
        self.assertEqual(tagged["CUE_TRACK01_TENSOR_HNR"], ["12.5"])
        self.assertEqual(tagged["CUE_TRACK01_ESSENTIA_MOOD_HAPPY_HAPPY"], ["800"])
        result = self.cli(extra=("--force",))
        self.assertEqual(result.returncode, 0, result.stderr)
        tagged = FLAC(self.flac)
        self.assertEqual(tagged["CUE_TRACK01_LIBROSA_BPM"], ["128"])
        self.assertEqual(tagged["CUE_TRACK02_LIBROSA_BPM"], ["150"])
        self.assertEqual(tagged["TITLE"], ["Keep title"])
        new_pcm, _ = sf.read(self.flac, dtype="int16")
        np.testing.assert_array_equal(new_pcm, original_pcm)

    def test_invalid_inputs_fail(self):
        for value in (None, [], {"mix": []}, {"features": []}, {"demucs": {"vocals": 3}}):
            with self.subTest(value=value), self.assertRaises((ValueError, TypeError)):
                build_flac_tags(value, {}, {})
        self.ess, self.tensor = {}, {}
        self.assertNotEqual(self.cli(lib={}).returncode, 0)
        self.assertNotEqual(self.cli(lib={"mix": []}).returncode, 0)
        # Supplied optional files must exist and be valid JSON.
        bad = self.root / "bad.json"
        bad.write_text("{", encoding="utf-8")
        self.assertNotEqual(self.cli(extra=("--predictions-json-path", str(bad))).returncode, 0)
        self.assertNotEqual(self.cli(extra=("--tensor-json-path", str(self.root / "missing.json"))).returncode, 0)

    def test_missing_flac_fails(self):
        self.assertNotEqual(self.cli().returncode, 0)


if __name__ == "__main__":
    unittest.main()
