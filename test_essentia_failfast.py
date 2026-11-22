import tempfile
import threading
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import numpy as np

import models
import worker_cpu_daemon
import worker_daemon
from analyzer.essentia_dsp import run_essentia_serialized


class _DummyIO:
    def __init__(self, name, shape=(1, 128)):
        self.name = name
        self.shape = shape


class _RaisingEffnet:
    def run(self, *_args, **_kwargs):
        raise RuntimeError("effnet crashed")


class _OkEffnet:
    def run(self, *_args, **_kwargs):
        return [np.ones((4, 128), dtype=np.float32)]


class _RaisingClassifier:
    def get_inputs(self):
        return [_DummyIO("in")]

    def get_outputs(self):
        return [_DummyIO("out")]

    def run(self, *_args, **_kwargs):
        raise RuntimeError("clf crashed")


class EssentiaFailFastTests(unittest.TestCase):
    def test_run_essentia_raises_when_effnet_missing(self):
        patches = np.zeros((4, 128, 96), dtype=np.float32)
        with self.assertRaisesRegex(RuntimeError, "not initialized"):
            run_essentia_serialized(
                patches,
                {"mood": {"classes": ["happy", "sad"]}},
                sessions={"effnet": None, "classifiers": {}},
                lock=threading.Lock(),
            )

    def test_run_essentia_raises_when_effnet_run_fails(self):
        patches = np.zeros((4, 128, 96), dtype=np.float32)
        sessions = {
            "effnet": _RaisingEffnet(),
            "eff_in": "in",
            "eff_out": "out",
            "classifiers": {},
        }
        with self.assertRaisesRegex(RuntimeError, "backbone inference failed"):
            run_essentia_serialized(
                patches,
                {"mood": {"classes": ["happy", "sad"]}},
                sessions=sessions,
                lock=threading.Lock(),
            )

    def test_run_essentia_raises_when_all_classifiers_fail(self):
        patches = np.zeros((4, 128, 96), dtype=np.float32)
        sessions = {
            "effnet": _OkEffnet(),
            "eff_in": "in",
            "eff_out": "out",
            "classifiers": {"mood": _RaisingClassifier()},
        }
        with self.assertRaisesRegex(RuntimeError, "empty predictions"):
            run_essentia_serialized(
                patches,
                {"mood": {"classes": ["happy", "sad"]}},
                sessions=sessions,
                lock=threading.Lock(),
            )

    def test_init_global_onnx_sessions_records_missing_effnet_and_fails_on_run(self):
        patches = np.zeros((4, 128, 96), dtype=np.float32)
        with tempfile.TemporaryDirectory() as tmpdir:
            models.init_global_onnx_sessions(
                tmpdir,
                {},
                providers_override=["CPUExecutionProvider"],
            )
            self.assertIsNone(models.GLOBAL_ONNX_SESSIONS["effnet"])
            self.assertIn("discogs-effnet-bs64-1.onnx", models.GLOBAL_ONNX_SESSIONS["init_error"])
            with self.assertRaisesRegex(RuntimeError, "discogs-effnet-bs64-1\\.onnx"):
                run_essentia_serialized(
                    patches,
                    {"mood": {"classes": ["happy", "sad"]}},
                    sessions=models.GLOBAL_ONNX_SESSIONS,
                    lock=threading.Lock(),
                )

    def test_daemons_reject_missing_essentia_on_mix_but_allow_stems_only(self):
        for daemon_mod in (worker_cpu_daemon, worker_daemon):
            class FakeShm:
                closed = False

                def close(self):
                    self.closed = True

            class FakeCtx:
                cleared = False

                def clear(self):
                    self.cleared = True

            payload_mix = {
                "sr": 44100,
                "stems": {"mix": {"shape": [1, 1], "dtype": "float32"}},
            }
            with (
                patch.object(daemon_mod, "_open_stem_samples", return_value=(FakeShm(), np.zeros(16, dtype=np.float32))),
                patch.object(daemon_mod, "AudioContext", return_value=FakeCtx()),
                patch.object(daemon_mod.librosa_extractor, "run", return_value={"bpm": 120.0}),
            ):
                with self.assertRaisesRegex(RuntimeError, "Essentia models are not loaded"):
                    daemon_mod.handle_extract_cpu(payload_mix, {})

            with (
                patch.object(daemon_mod, "_open_stem_samples", return_value=(FakeShm(), np.zeros(16, dtype=np.float32))),
                patch.object(daemon_mod, "AudioContext", return_value=FakeCtx()),
                patch.object(daemon_mod.librosa_extractor, "run", return_value={"bpm": 120.0}),
                patch.object(daemon_mod, "extract_mel_patches", return_value=np.zeros((1, 128, 96), dtype=np.float32)),
                patch.object(daemon_mod, "run_essentia_serialized", return_value={}),
            ):
                with self.assertRaisesRegex(RuntimeError, "empty predictions"):
                    daemon_mod.handle_extract_cpu(payload_mix, {"mood": SimpleNamespace()})

            payload_stems_only = {
                "sr": 44100,
                "stems": {"vocals": {"shape": [1, 1], "dtype": "float32"}},
            }
            with (
                patch.object(daemon_mod, "_open_stem_samples", return_value=(FakeShm(), np.zeros(16, dtype=np.float32))),
                patch.object(daemon_mod, "AudioContext", return_value=FakeCtx()),
                patch.object(daemon_mod.librosa_extractor, "run", return_value={"rms": 0.5}),
            ):
                resp = daemon_mod.handle_extract_cpu(payload_stems_only, {})
                self.assertEqual("success", resp["status"])
                self.assertEqual({}, resp["essentia"])


if __name__ == "__main__":
    unittest.main()
