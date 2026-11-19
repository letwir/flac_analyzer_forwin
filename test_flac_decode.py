import hashlib
import os
import subprocess
import tempfile
import unittest
from unittest import mock

import numpy as np
import soundfile as sf

import flac_decode


ROOT = os.path.dirname(os.path.abspath(__file__))
VALUES = np.array([0.0, 0.5, -0.5, 0.25], dtype=np.float64)


class FlacDecodeFallbackTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=ROOT)
        self.path = os.path.join(self.temp.name, "fixture.flac")

    def tearDown(self):
        self.temp.cleanup()

    def write_fixture(self, subtype):
        file_format = "WAV" if subtype == "PCM_32" else "FLAC"
        self.path = os.path.join(self.temp.name, "fixture.wav" if file_format == "WAV" else "fixture.flac")
        sf.write(self.path, VALUES, 44100, format=file_format, subtype=subtype)

    def expected_pcm(self, bits):
        scale = 1 << (bits - 1)
        values = np.array([0, scale // 2, -(scale // 2), scale // 4], dtype=np.int64)
        if bits == 16:
            return values.astype("<i2").tobytes()
        if bits == 32:
            return values.astype("<i4").tobytes()
        packed = bytearray()
        for value in values:
            packed.extend((int(value) & 0xFFFFFF).to_bytes(3, "little"))
        return bytes(packed)

    def test_native_integer_pcm_matches_expected_raw_bytes_and_md5(self):
        for subtype, bits in (("PCM_16", 16), ("PCM_24", 24), ("PCM_32", 32)):
            with self.subTest(subtype=subtype):
                self.write_fixture(subtype)
                raw, tag, channels, rate, actual_bits = flac_decode.decode_flac_range_fallback(self.path, 0, 0)
                expected = self.expected_pcm(bits)
                self.assertEqual((tag, channels, rate, actual_bits), (1, 1, 44100, bits))
                self.assertEqual(raw, expected)
                self.assertEqual(hashlib.md5(raw).hexdigest(), hashlib.md5(expected).hexdigest())

    def test_pcm24_stereo_preserves_low_bits_and_extremes(self):
        samples = np.array([[-8388608, -1], [0, 1], [8388607, -2]], dtype=np.int32) << 8
        sf.write(self.path, samples, 44100, format="FLAC", subtype="PCM_24")
        raw, tag, channels, rate, bits = flac_decode.decode_flac_range_fallback(self.path, 0, 0)
        expected = b"".join((int(value) & 0xFFFFFF).to_bytes(3, "little") for value in samples.reshape(-1) >> 8)
        self.assertEqual((tag, channels, rate, bits), (1, 2, 44100, 24))
        self.assertEqual(raw, expected)

    def test_short_missing_cli_uses_soundfile_and_open_end_means_eof(self):
        self.write_fixture("PCM_24")
        with mock.patch.object(flac_decode.subprocess, "Popen", side_effect=FileNotFoundError("flac")):
            raw, _, _, _, bits = flac_decode.decode_flac_range(self.path, 1, 0)
        self.assertEqual(bits, 24)
        self.assertEqual(raw, self.expected_pcm(24)[3:])

    def test_non_missing_oserror_is_not_treated_as_missing_cli(self):
        with mock.patch.object(flac_decode.subprocess, "Popen", side_effect=PermissionError("denied")):
            with self.assertRaises(PermissionError):
                flac_decode.decode_flac_range(self.path, 0, 2, max_retries=1)

    def test_decoder_failure_keeps_process_context(self):
        class FailedProcess:
            returncode = 7

            def communicate(self):
                return b"", b"decoder exploded"

        with mock.patch.object(flac_decode.subprocess, "Popen", return_value=FailedProcess()), \
             mock.patch.object(flac_decode, "decode_flac_range_fallback", side_effect=ValueError("fallback failed")), \
             mock.patch.object(flac_decode.time, "sleep"):
            with self.assertRaisesRegex(RuntimeError, "rc=7.*decoder exploded.*cmd="):
                flac_decode.decode_flac_range(self.path, 0, 2, max_retries=1)

    def test_short_sample_change_changes_pcm_hash(self):
        self.write_fixture("PCM_16")
        first = flac_decode.decode_flac_range_fallback(self.path, 0, 0)[0]
        sf.write(self.path, np.array([0.0, 0.5, -0.5, 0.5]), 44100, format="FLAC", subtype="PCM_16")
        second = flac_decode.decode_flac_range_fallback(self.path, 0, 0)[0]
        self.assertNotEqual(hashlib.md5(first).digest(), hashlib.md5(second).digest())

    def test_short_read_and_unsupported_subtype_fail(self):
        self.write_fixture("PCM_16")
        with mock.patch.object(sf.SoundFile, "read", return_value=np.array([[1]], dtype=np.int16)):
            with self.assertRaisesRegex(RuntimeError, "short read"):
                flac_decode.decode_flac_range_fallback(self.path, 0, 3)

    def test_libsndfile_seek_error_uses_sequential_fallback(self):
        self.write_fixture("PCM_16")
        seek_error = sf.LibsndfileError(1, "seek failed")
        expected = (b"sequential", 1, 1, 44100, 16)
        with mock.patch.object(sf.SoundFile, "seek", side_effect=seek_error), \
             mock.patch.object(flac_decode, "decode_flac_range_stream_fallback", return_value=expected) as stream:
            self.assertEqual(flac_decode.decode_flac_range_fallback(self.path, 0, 2), expected)
        stream.assert_called_once_with(self.path, 0, 2)
        with mock.patch.object(sf.SoundFile, "subtype", new_callable=mock.PropertyMock, return_value="FLOAT"):
            with self.assertRaisesRegex(ValueError, "Unsupported FLAC subtype"):
                flac_decode.decode_flac_range_fallback(self.path, 0, 3)

    def test_long_missing_cli_hashes_raw_integer_pcm(self):
        self.write_fixture("PCM_24")
        expected = self.expected_pcm(24)
        with mock.patch.object(flac_decode.subprocess, "Popen", side_effect=FileNotFoundError("flac")):
            audio, digest = flac_decode.process_slice_with_seq_safety(self.path, 0, 600 * 44100, 44100, 1)
        self.assertEqual(digest, hashlib.md5(expected).hexdigest())
        self.assertEqual(audio.shape[1], 1)

    def test_sequential_fallback_closes_both_pipes_on_header_error(self):
        class Pipe:
            closed = False

            def read(self, _size):
                return b"bad"

            def close(self):
                self.closed = True

        class Process:
            def __init__(self):
                self.stdout, self.stderr = Pipe(), Pipe()
                self.stopped = False

            def poll(self):
                return -1 if self.stopped else None

            def terminate(self):
                self.stopped = True

            def wait(self, timeout=None):
                return -1

            def kill(self):
                self.stopped = True

        proc = Process()
        with mock.patch.object(flac_decode.subprocess, "Popen", return_value=proc):
            with self.assertRaisesRegex(RuntimeError, "WAV header|WAV ヘッダ"):
                flac_decode.decode_flac_range_stream_fallback(self.path, 0, 1)
        self.assertTrue(proc.stdout.closed)
        self.assertTrue(proc.stderr.closed)
        self.assertTrue(proc.stopped)

    def test_long_stream_process_is_cleaned_up_after_header_error(self):
        self.write_fixture("PCM_16")
        events = []

        class Pipe:
            closed = False

            def read(self, _size):
                events.append("read")
                return b"bad"

            def close(self):
                self.closed = True
                events.append("close")

        class Process:
            def __init__(self):
                self.stdout, self.stderr = Pipe(), Pipe()
                self.terminated = False
                self.killed = False
                self.wait_calls = 0

            def poll(self):
                return None if not self.terminated else -15

            def terminate(self):
                self.terminated = True
                events.append("terminate")

            def kill(self):
                self.killed = True
                events.append("kill")

            def wait(self, timeout=None):
                self.wait_calls += 1
                events.append("wait")
                if not self.killed:
                    raise subprocess.TimeoutExpired("flac", timeout)
                return -9

        proc = Process()
        real_fallback = flac_decode.decode_flac_range_fallback

        def fallback_with_event(*args):
            events.append("fallback")
            return real_fallback(*args)

        with mock.patch.object(flac_decode.subprocess, "Popen", return_value=proc), \
             mock.patch.object(flac_decode, "decode_flac_range_fallback", side_effect=fallback_with_event):
            _, digest = flac_decode.process_slice_with_seq_safety(self.path, 0, 600 * 44100, 44100, 1)
        self.assertEqual(digest, hashlib.md5(self.expected_pcm(16)).hexdigest())
        self.assertTrue(proc.stdout.closed)
        self.assertTrue(proc.stderr.closed)
        self.assertTrue(proc.terminated)
        self.assertTrue(proc.killed)
        self.assertEqual(proc.wait_calls, 2)
        self.assertLess(events.index("close"), events.index("fallback"))
        self.assertLess(events.index("terminate"), events.index("fallback"))
        self.assertGreater(events.index("fallback"), max(i for i, event in enumerate(events) if event == "wait"))


if __name__ == "__main__":
    unittest.main()
