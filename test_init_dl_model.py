import importlib.util
import os
import subprocess
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch


def _load_init_dl_model():
    path = os.path.join(os.path.dirname(__file__), "zig", "init_dl_model.py")
    spec = importlib.util.spec_from_file_location("init_dl_model", path)
    mod = importlib.util.module_from_spec(spec)
    assert spec is not None and spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


init_dl_model = _load_init_dl_model()


class InitDlModelTests(unittest.TestCase):
    def test_select_requirements_file_blackwell_branches(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            std_req = os.path.join(tmpdir, "requirements.txt")
            bw_req = os.path.join(tmpdir, "requirements-blackwell.txt")
            with open(std_req, "w", encoding="utf-8") as f:
                f.write("onnx\n")

            self.assertEqual(std_req, init_dl_model.select_requirements_file(tmpdir, True))

            with open(bw_req, "w", encoding="utf-8") as f:
                f.write("onnx==1.22.0\n")

            self.assertEqual(bw_req, init_dl_model.select_requirements_file(tmpdir, True))
            self.assertEqual(std_req, init_dl_model.select_requirements_file(tmpdir, False))

    def test_is_blackwell_gpu_detection(self):
        with patch.object(
            init_dl_model.subprocess,
            "run",
            return_value=SimpleNamespace(returncode=0, stdout="NVIDIA GeForce RTX 5070 Ti\n"),
        ):
            self.assertTrue(init_dl_model.is_blackwell_gpu())

        with patch.object(
            init_dl_model.subprocess,
            "run",
            return_value=SimpleNamespace(returncode=0, stdout="NVIDIA GeForce RTX 4090\n"),
        ):
            self.assertFalse(init_dl_model.is_blackwell_gpu())

        with patch.object(
            init_dl_model.subprocess,
            "run",
            side_effect=FileNotFoundError("nvidia-smi missing"),
        ):
            self.assertFalse(init_dl_model.is_blackwell_gpu())

        with patch.object(
            init_dl_model.subprocess,
            "run",
            side_effect=subprocess.TimeoutExpired(cmd="nvidia-smi", timeout=5),
        ):
            self.assertFalse(init_dl_model.is_blackwell_gpu())

    def test_pb_conversions_includes_backbone_and_genre_discogs400(self):
        conversions = {pb: (onnx, inp, out) for pb, onnx, inp, out in init_dl_model.PB_CONVERSIONS}
        self.assertEqual(
            (
                "discogs-effnet-bs64-1.onnx",
                "serving_default_melspectrogram:0",
                "PartitionedCall:1",
            ),
            conversions.get("discogs-effnet-bs64-1.pb"),
        )
        self.assertEqual(
            (
                "genre_discogs400-discogs-effnet-1.onnx",
                "serving_default_model_Placeholder:0",
                "PartitionedCall:0",
            ),
            conversions.get("genre_discogs400-discogs-effnet-1.pb"),
        )


if __name__ == "__main__":
    unittest.main()
