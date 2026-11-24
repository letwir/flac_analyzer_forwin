import math
import os
import tempfile
import unittest
from unittest import mock

import librosa
import numpy as np
import torch

from analyzer.core import AudioContext
from analyzer.librosa_dsp import librosa_extractor
from analyzer.tensor_dsp import compute_and_save_spectro_chunked_Large, extract_tensor_features


class TestSpectroCacheParity(unittest.TestCase):
    def test_chunked_stft_shape_dtype_and_librosa_parity(self):
        rng = np.random.default_rng(42)
        lengths = [1000, 44100, 512 * 2050 + 123]
        with tempfile.TemporaryDirectory() as tmp_dir:
            for idx, n_samples in enumerate(lengths):
                y = rng.standard_normal(n_samples).astype(np.float32) * 0.25
                spectro_path = os.path.join(tmp_dir, f"stem_{idx}.spectro.npy")
                shape = compute_and_save_spectro_chunked_Large(
                    torch.from_numpy(y),
                    spectro_path,
                    torch.device("cpu"),
                    n_fft=2048,
                    hop_length=512,
                    chunk_frames=2048,
                )
                expected_frames = 1 + n_samples // 512
                self.assertEqual(shape, (1025, expected_frames))

                ref = np.abs(
                    librosa.stft(y, n_fft=2048, hop_length=512, center=True, pad_mode="constant")
                ).astype(np.float32)
                self.assertEqual(ref.shape, (1025, expected_frames))

                loaded = np.load(spectro_path)
                self.assertEqual(loaded.dtype, np.float32)
                self.assertEqual(loaded.shape, ref.shape)
                np.testing.assert_allclose(loaded, ref, atol=1e-4, rtol=1e-4)

                leftovers = [f for f in os.listdir(tmp_dir) if ".tmp.npy" in f]
                self.assertEqual(leftovers, [])

    def test_multichannel_3ch_float64_downmix_matches_audiocontext_roundtrip(self):
        # Precision-sensitive float64 channels: float64 averaging differs from pre-cast float32 averaging.
        y_3ch = np.empty((3, 2048), dtype=np.float64)
        y_3ch[0, :] = 100000001.0
        y_3ch[1, :] = -100000000.0
        y_3ch[2, :] = 0.0
        with tempfile.TemporaryDirectory() as tmp_dir:
            spectro_path = os.path.join(tmp_dir, "three_ch.spectro.npy")
            shape = compute_and_save_spectro_chunked_Large(
                torch.from_numpy(y_3ch),
                spectro_path,
                torch.device("cpu"),
            )
            self.assertEqual(shape, (1025, 1 + 2048 // 512))
            direct = np.load(spectro_path)
            self.assertEqual(direct.dtype, np.float32)
            self.assertEqual(direct.shape, shape)
            with AudioContext(y=y_3ch, sr=44100, source="mix", spectro_path=spectro_path) as cached_ctx, AudioContext(
                y=y_3ch, sr=44100, source="mix"
            ) as uncached_ctx:
                np.testing.assert_allclose(direct, uncached_ctx.spectro, atol=1e-4, rtol=1e-4)
                np.testing.assert_allclose(cached_ctx.spectro, uncached_ctx.spectro, atol=1e-4, rtol=1e-4)

    def test_oom_retry_halves_chunk_to_1024_and_cleans_temp(self):
        n_samples = 512 * 2100
        rng = np.random.default_rng(7)
        y = rng.standard_normal(n_samples).astype(np.float32) * 0.2
        ref = np.abs(
            librosa.stft(y, n_fft=2048, hop_length=512, center=True, pad_mode="constant")
        ).astype(np.float32)

        real_stft = torch.stft
        input_lengths: list[int] = []

        def flaky_stft(inp, *args, **kwargs):
            input_lengths.append(int(inp.shape[-1]))
            if len(input_lengths) == 1:
                raise RuntimeError("CUDA out of memory")
            return real_stft(inp, *args, **kwargs)

        with tempfile.TemporaryDirectory() as tmp_dir:
            spectro_path = os.path.join(tmp_dir, "oom_retry.spectro.npy")
            with mock.patch("analyzer.tensor_dsp.torch.stft", side_effect=flaky_stft):
                shape = compute_and_save_spectro_chunked_Large(
                    torch.from_numpy(y),
                    spectro_path,
                    torch.device("cpu"),
                    n_fft=2048,
                    hop_length=512,
                    chunk_frames=2048,
                )
            self.assertEqual(shape, ref.shape)
            # Exact input-length sequence: 2048-frame initial attempt -> OOM -> 1024, 1024, 53 frames.
            expected_lengths = [
                (2048 - 1) * 512 + 2048,
                (1024 - 1) * 512 + 2048,
                (1024 - 1) * 512 + 2048,
                (53 - 1) * 512 + 2048,
            ]
            self.assertEqual(input_lengths, expected_lengths)
            loaded = np.load(spectro_path)
            np.testing.assert_allclose(loaded, ref, atol=1e-4, rtol=1e-4)
            self.assertEqual([f for f in os.listdir(tmp_dir) if ".tmp.npy" in f], [])

    def test_extract_tensor_features_invariant_after_spectro_cache_generation(self):
        sr = 44100
        t = np.linspace(0, 1.0, sr, endpoint=False, dtype=np.float32)
        y = (0.3 * np.sin(2 * np.pi * 440.0 * t)).astype(np.float32)
        y_tensor = torch.from_numpy(y)
        with tempfile.TemporaryDirectory() as tmp_dir:
            spectro_path = os.path.join(tmp_dir, "vocals.spectro.npy")
            base = extract_tensor_features(y_tensor, sr, torch.device("cpu"), stem_name="vocals")
            compute_and_save_spectro_chunked_Large(y_tensor, spectro_path, torch.device("cpu"))
            after_cache = extract_tensor_features(
                y_tensor, sr, torch.device("cpu"), spectro_path=spectro_path, stem_name="vocals"
            )
            self.assertEqual(base, after_cache)

    def test_audiocontext_validates_and_deterministically_closes_mmap(self):
        sr = 44100
        rng = np.random.default_rng(11)
        y = rng.standard_normal(44100).astype(np.float32) * 0.2
        expected_frames = 1 + len(y) // 512

        real_np_load = np.load
        opened_mmaps = []

        def tracking_load(*args, **kwargs):
            res = real_np_load(*args, **kwargs)
            mm = getattr(res, "_mmap", None)
            if mm is not None:
                opened_mmaps.append(mm)
            return res

        with tempfile.TemporaryDirectory() as tmp_dir:
            spectro_path = os.path.join(tmp_dir, "valid.spectro.npy")
            compute_and_save_spectro_chunked_Large(
                torch.from_numpy(y), spectro_path, torch.device("cpu")
            )

            with mock.patch("analyzer.core.np.load", side_effect=tracking_load):
                with AudioContext(y=y, sr=sr, source="mix", spectro_path=spectro_path) as ctx:
                    spec = ctx.spectro
                    self.assertEqual(spec.shape, (1025, expected_frames))
                    self.assertEqual(spec.dtype, np.float32)
                    self.assertEqual(len(opened_mmaps), 1)
                    self.assertTrue(opened_mmaps[-1].closed)
                    # Owned copy remains readable for downstream power/mel without active mmap dependency.
                    self.assertEqual(ctx.power.shape, (1025, expected_frames))
            os.remove(spectro_path)
            self.assertFalse(os.path.exists(spectro_path))

            # Shape mismatch falls back to CPU STFT and closes bad mmap handle immediately.
            bad_shape_path = os.path.join(tmp_dir, "bad_shape.spectro.npy")
            np.save(bad_shape_path, np.zeros((1025, expected_frames + 3), dtype=np.float32))
            with mock.patch("analyzer.core.np.load", side_effect=tracking_load):
                ctx_bad_shape = AudioContext(y=y, sr=sr, source="mix", spectro_path=bad_shape_path)
                fallback = ctx_bad_shape.spectro
                self.assertEqual(fallback.shape, (1025, expected_frames))
                self.assertEqual(len(opened_mmaps), 2)
                self.assertTrue(opened_mmaps[-1].closed)
                ctx_bad_shape.clear()
            os.remove(bad_shape_path)

            # Dtype mismatch falls back to CPU STFT and closes bad mmap handle immediately.
            bad_dtype_path = os.path.join(tmp_dir, "bad_dtype.spectro.npy")
            np.save(bad_dtype_path, np.zeros((1025, expected_frames), dtype=np.float64))
            with mock.patch("analyzer.core.np.load", side_effect=tracking_load):
                ctx_bad_dtype = AudioContext(y=y, sr=sr, source="mix", spectro_path=bad_dtype_path)
                fallback2 = ctx_bad_dtype.spectro
                self.assertEqual(fallback2.dtype, np.float32)
                self.assertEqual(len(opened_mmaps), 3)
                self.assertTrue(opened_mmaps[-1].closed)
                ctx_bad_dtype.close()
            os.remove(bad_dtype_path)

    def test_ultra_short_and_silent_signals_avoid_domain_and_pad_errors(self):
        devices = [torch.device("cpu")]
        if torch.cuda.is_available():
            devices.append(torch.device("cuda"))
        stems = ("mix", "vocals", "drums", "bass", "piano", "guitar", "other")

        with tempfile.TemporaryDirectory() as tmp_dir:
            for dev in devices:
                for n_samples in (1, 512):
                    for amplitude in (0.0, 0.1):
                        y = np.full((n_samples,), amplitude, dtype=np.float32)
                        y_tensor = torch.from_numpy(y)
                        spectro_path = os.path.join(
                            tmp_dir, f"short_{dev.type}_{n_samples}_{amplitude}.spectro.npy"
                        )
                        try:
                            t_feats = extract_tensor_features(y_tensor, 44100, dev, stem_name="mix")
                            for key, val in t_feats.items():
                                if isinstance(val, (int, float)):
                                    self.assertTrue(
                                        math.isfinite(float(val)),
                                        f"non-finite tensor feature {key}={val} for len={n_samples}, amp={amplitude}",
                                    )
                            shape = compute_and_save_spectro_chunked_Large(y_tensor, spectro_path, dev)
                            self.assertEqual(shape, (1025, 1 + n_samples // 512))

                            if dev.type == "cpu":
                                for src in stems:
                                    with AudioContext(
                                        y=y,
                                        sr=44100,
                                        source=src,
                                        spectro_path=spectro_path if src == "mix" else None,
                                    ) as ctx:
                                        feats = librosa_extractor.run(ctx)
                                        self.assertIsNotNone(feats)
                        finally:
                            if os.path.exists(spectro_path):
                                os.remove(spectro_path)


if __name__ == "__main__":
    unittest.main()
