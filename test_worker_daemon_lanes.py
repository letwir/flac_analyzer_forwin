import unittest
from unittest.mock import patch
from pathlib import Path

import worker_daemon


class WorkerDaemonLaneTests(unittest.TestCase):
    def test_cpu_daemon_has_no_torch_import_and_forces_hidden_cuda(self):
        source = Path("worker_cpu_daemon.py").read_text(encoding="utf-8")
        self.assertIn('os.environ["CUDA_VISIBLE_DEVICES"] = "-1"', source)
        self.assertNotIn("import torch", source)

    def test_extract_all_remains_compatible_with_lane_results(self):
        cpu_response = {
            "status": "success",
            "librosa": {"mix": {"bpm": 120.0}, "demucs": {}},
            "essentia": {"mood_happy": 0.8},
            "profile": {"librosa_sec": 1.0, "essentia_sec": 2.0},
        }
        gpu_response = {
            "status": "success",
            "tensor": {"mix": {"spectral_flux": 3.0}, "demucs": {}},
            "profile": {"tensor_sec": 4.0},
        }
        with (
            patch.object(worker_daemon, "handle_extract_cpu", return_value=cpu_response),
            patch.object(worker_daemon, "handle_extract_gpu", return_value=gpu_response),
        ):
            response = worker_daemon.handle_extract_all({"sr": 44100, "stems": {}}, {}, "cpu")

        self.assertEqual(cpu_response["librosa"], response["librosa"])
        self.assertEqual(gpu_response["tensor"], response["tensor"])
        self.assertEqual(cpu_response["essentia"], response["essentia"])
        self.assertEqual(1.0, response["profile"]["librosa_sec"])
        self.assertEqual(4.0, response["profile"]["tensor_sec"])
        self.assertEqual(2.0, response["profile"]["essentia_sec"])

    def test_cpu_lane_releases_stem_handles_when_librosa_fails(self):
        class FakeShm:
            closed = False

            def close(self):
                self.closed = True

        class FakeContext:
            cleared = False

            def clear(self):
                self.cleared = True

        shm = FakeShm()
        context = FakeContext()
        payload = {
            "sr": 44100,
            "stems": {"mix": {"shape": [1, 1], "dtype": "float32"}},
        }
        with (
            patch.object(worker_daemon, "_open_stem_samples", return_value=(shm, object())),
            patch.object(worker_daemon, "AudioContext", return_value=context),
            patch.object(worker_daemon.librosa_extractor, "run", side_effect=RuntimeError("boom")),
        ):
            with self.assertRaisesRegex(RuntimeError, "boom"):
                worker_daemon.handle_extract_cpu(payload, {})

        self.assertTrue(context.cleared)
        self.assertTrue(shm.closed)


if __name__ == "__main__":
    unittest.main()
