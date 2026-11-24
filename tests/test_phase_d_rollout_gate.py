import concurrent.futures
import ctypes
from ctypes import wintypes
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock

import numpy as np
import soundfile as sf
import torch

_REAL_NP_LOAD = np.load

import analyzer.core
import analyzer.tensor_dsp
from analyzer.core import AudioContext
from analyzer.librosa_dsp import librosa_extractor
from analyzer.tensor_dsp import compute_and_save_spectro_chunked_Large, extract_tensor_features
import worker_gpu_daemon


class _ProcessMemoryCounters(ctypes.Structure):
    _fields_ = [
        ("cb", wintypes.DWORD),
        ("PageFaultCount", wintypes.DWORD),
        ("PeakWorkingSetSize", ctypes.c_size_t),
        ("WorkingSetSize", ctypes.c_size_t),
        ("QuotaPeakPagedPoolUsage", ctypes.c_size_t),
        ("QuotaPagedPoolUsage", ctypes.c_size_t),
        ("QuotaPeakNonPagedPoolUsage", ctypes.c_size_t),
        ("QuotaNonPagedPoolUsage", ctypes.c_size_t),
        ("PagefileUsage", ctypes.c_size_t),
        ("PeakPagefileUsage", ctypes.c_size_t),
    ]


def _get_process_rss_mb() -> float:
    if os.name != "nt":
        raise RuntimeError(f"Unsupported OS for Windows RSS check: {os.name}")
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.GetCurrentProcess.argtypes = []
    kernel32.GetCurrentProcess.restype = wintypes.HANDLE

    psapi = ctypes.WinDLL("psapi", use_last_error=True)
    psapi.GetProcessMemoryInfo.argtypes = [
        wintypes.HANDLE,
        ctypes.POINTER(_ProcessMemoryCounters),
        wintypes.DWORD,
    ]
    psapi.GetProcessMemoryInfo.restype = wintypes.BOOL

    pmc = _ProcessMemoryCounters()
    pmc.cb = ctypes.sizeof(_ProcessMemoryCounters)
    proc = kernel32.GetCurrentProcess()
    if not psapi.GetProcessMemoryInfo(proc, ctypes.byref(pmc), pmc.cb):
        raise RuntimeError(f"GetProcessMemoryInfo failed (winerror={ctypes.get_last_error()})")
    return pmc.WorkingSetSize / (1024.0 * 1024.0)


def _measure_cuda_and_rss(device: torch.device, fn):
    torch.cuda.synchronize(device)
    torch.cuda.reset_peak_memory_stats(device)
    result = fn()
    torch.cuda.synchronize(device)
    peak_vram_mb = torch.cuda.max_memory_allocated(device) / (1024.0 * 1024.0)
    rss_mb = _get_process_rss_mb()
    return result, peak_vram_mb, rss_mb


def _find_test_flac() -> str | None:
    root = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "testFLAC"))
    if not os.path.isdir(root):
        return None
    preferred = os.path.join(root, "01_01_シンカンセンスゴイカタイアイス.flac")
    if os.path.isfile(preferred):
        return preferred
    candidates = sorted(
        os.path.join(root, name)
        for name in os.listdir(root)
        if name.lower().endswith(".flac")
    )
    return candidates[0] if candidates else None


def _load_real_flac_excerpt(path: str, max_seconds: float = 12.0, offset_seconds: float = 0.0) -> tuple[np.ndarray, int]:
    info = sf.info(path)
    sr = int(info.samplerate)
    start_frame = min(int(offset_seconds * sr), max(0, info.frames - sr))
    frames = min(int(max_seconds * sr), max(1, info.frames - start_frame))
    y, read_sr = sf.read(path, frames=frames, start=start_frame, dtype="float32", always_2d=False)
    if y.ndim > 1:
        y = np.mean(y, axis=-1)
    return np.ascontiguousarray(y, dtype=np.float32), int(read_sr)


def _assert_json_parity(
    tc: unittest.TestCase,
    actual: object,
    expected: object,
    path: str = "root",
    rtol: float = 1e-4,
    atol: float = 1e-4,
    exact: bool = False,
) -> None:
    if isinstance(expected, dict):
        tc.assertIsInstance(actual, dict, f"{path}: expected dict")
        tc.assertEqual(set(actual.keys()), set(expected.keys()), f"{path}: key mismatch")
        for key in expected:
            _assert_json_parity(tc, actual[key], expected[key], f"{path}.{key}", rtol=rtol, atol=atol, exact=exact)
        return
    if isinstance(expected, list):
        tc.assertIsInstance(actual, list, f"{path}: expected list")
        tc.assertEqual(len(actual), len(expected), f"{path}: len mismatch")
        for idx, (a_item, e_item) in enumerate(zip(actual, expected)):
            _assert_json_parity(tc, a_item, e_item, f"{path}[{idx}]", rtol=rtol, atol=atol, exact=exact)
        return
    if isinstance(expected, bool):
        tc.assertIsInstance(actual, bool, f"{path}: expected bool, got {type(actual).__name__}")
        tc.assertEqual(actual, expected, f"{path}: bool mismatch")
        return
    if expected is None:
        tc.assertIsNone(actual, f"{path}: expected None")
        return
    if isinstance(expected, str):
        tc.assertIsInstance(actual, str, f"{path}: expected str")
        tc.assertEqual(actual, expected, f"{path}: str mismatch")
        return
    if isinstance(expected, (int, float)):
        tc.assertIsInstance(actual, (int, float), f"{path}: expected numeric")
        tc.assertNotIsInstance(actual, bool, f"{path}: unexpected bool for numeric field")
        if exact:
            tc.assertEqual(actual, expected, f"{path}: exact numeric mismatch")
        else:
            np.testing.assert_allclose(float(actual), float(expected), rtol=rtol, atol=atol, err_msg=path)
        return
    tc.assertEqual(actual, expected, f"{path}: mismatch")


def _extract_track_json(
    y: np.ndarray,
    sr: int,
    device: torch.device,
    track_id: str,
    spectro_path: str | None = None,
    tc: unittest.TestCase | None = None,
    external_load_log: tuple[threading.Lock, dict[str, list[tuple[tuple, dict]]]] | None = None,
) -> tuple[dict, dict]:
    y_tensor = torch.from_numpy(y)
    tensor_feats = extract_tensor_features(y_tensor, sr, device, stem_name="mix")
    if spectro_path is not None:
        compute_and_save_spectro_chunked_Large(y_tensor, spectro_path, device)
    real_np_load = _REAL_NP_LOAD
    local_calls: list[tuple[tuple, dict]] = []
    loaded_spectro: np.ndarray | None = None

    if external_load_log is None:
        def spying_load(*args, **kwargs):
            local_calls.append((args, kwargs))
            return real_np_load(*args, **kwargs)

        with mock.patch.object(analyzer.core.np, "load", side_effect=spying_load):
            with AudioContext(y=y, sr=sr, source="mix", spectro_path=spectro_path) as ctx:
                raw = librosa_extractor.run(ctx)
                librosa_json = json.loads(json.dumps(raw.to_postgres_dict(track_id=track_id)))
                if ctx._spectro is not None:
                    loaded_spectro = ctx._spectro.copy()
    else:
        with AudioContext(y=y, sr=sr, source="mix", spectro_path=spectro_path) as ctx:
            raw = librosa_extractor.run(ctx)
            librosa_json = json.loads(json.dumps(raw.to_postgres_dict(track_id=track_id)))
            if ctx._spectro is not None:
                loaded_spectro = ctx._spectro.copy()
        lock, recorded = external_load_log
        with lock:
            local_calls = list(recorded.get(spectro_path or "", []))

    if spectro_path is not None and tc is not None:
        tc.assertIn(((spectro_path,), {"mmap_mode": "r"}), local_calls, "AudioContext did not load spectro cache")
        tc.assertIsNotNone(loaded_spectro, "AudioContext did not retain loaded spectrogram")
        disk_spectro = real_np_load(spectro_path)
        np.testing.assert_array_equal(loaded_spectro, disk_spectro)

    return tensor_feats, librosa_json


def _run_go_tests_json(
    tc: unittest.TestCase,
    pkg: str,
    test_names: list[str],
    extra_env: dict[str, str] | None = None,
) -> None:
    orchestrator_dir = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "orchestrator"))
    pattern = f"^({'|'.join(test_names)})$"
    env = os.environ.copy()
    if extra_env:
        env.update(extra_env)
    proc = subprocess.run(
        ["go", "test", "-json", "-count=1", "-run", pattern, pkg],
        cwd=orchestrator_dir,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    tc.assertEqual(
        proc.returncode,
        0,
        f"Go test runner exited non-zero:\nSTDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}",
    )
    passed: set[str] = set()
    skipped: set[str] = set()
    for line in proc.stdout.splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        ev = json.loads(line)
        tname = ev.get("Test")
        action = ev.get("Action")
        if not tname:
            continue
        if action == "pass":
            passed.add(tname)
        elif action == "skip":
            skipped.add(tname)

    tc.assertEqual(skipped & set(test_names), set(), f"Go tests were skipped: {skipped & set(test_names)}")
    tc.assertTrue(
        set(test_names).issubset(passed),
        f"Missing Go pass events: expected={test_names}, passed={sorted(passed)}\nSTDOUT:\n{proc.stdout}",
    )


class TestPhaseDRolloutGate(unittest.TestCase):
    def _require_cuda_and_flac(self) -> tuple[torch.device, str]:
        self.assertTrue(torch.cuda.is_available(), "CUDA device is required for Phase D rollout gate")
        flac_path = _find_test_flac()
        self.assertIsNotNone(flac_path, "testFLAC directory must contain at least one .flac file")
        return torch.device("cuda"), flac_path

    def test_spectro_cache_off_vs_on_real_flac_parity_vram_rss_zero_orphans(self):
        device, flac_path = self._require_cuda_and_flac()
        y, sr = _load_real_flac_excerpt(flac_path, max_seconds=12.0, offset_seconds=0.0)

        (tensor_off, librosa_off), vram_off, rss_off = _measure_cuda_and_rss(
            device,
            lambda: _extract_track_json(y, sr, device, track_id="gate-parity", spectro_path=None, tc=self),
        )

        with tempfile.TemporaryDirectory() as tmp_dir:
            spectro_path = os.path.join(tmp_dir, "real_mix.spectro.npy")
            (tensor_on, librosa_on), vram_on, rss_on = _measure_cuda_and_rss(
                device,
                lambda: _extract_track_json(
                    y, sr, device, track_id="gate-parity", spectro_path=spectro_path, tc=self
                ),
            )
            self.assertTrue(os.path.isfile(spectro_path))
            self.assertEqual(sorted(os.listdir(tmp_dir)), ["real_mix.spectro.npy"])
            os.remove(spectro_path)
            self.assertEqual(os.listdir(tmp_dir), [])

        self.assertGreater(vram_off, 0.0)
        self.assertGreater(vram_on, 0.0)
        self.assertGreater(rss_off, 0.0)
        self.assertGreater(rss_on, 0.0)
        print(
            f"[PhaseD RolloutGate SpectroCache] file={os.path.basename(flac_path)} "
            f"vram_off_mb={vram_off:.2f} vram_on_mb={vram_on:.2f} "
            f"rss_off_mb={rss_off:.2f} rss_on_mb={rss_on:.2f}"
        )

        self.assertEqual(tensor_off, tensor_on)
        _assert_json_parity(self, librosa_on, librosa_off, path="librosa", rtol=1e-4, atol=1e-4, exact=False)

    def test_worker_gpu_daemon_spectro_cache_outcomes(self):
        device, flac_path = self._require_cuda_and_flac()
        y, sr = _load_real_flac_excerpt(flac_path, max_seconds=4.0, offset_seconds=0.0)

        with tempfile.TemporaryDirectory() as tmp_dir:
            sample_npy = os.path.join(tmp_dir, "wave.npy")
            np.save(sample_npy, y)
            base_stem = {
                "storage_type": "file",
                "file_path": sample_npy,
                "shape": list(y.shape),
                "dtype": "float32",
            }

            written_path = os.path.join(tmp_dir, "written.spectro.npy")
            resp_written = worker_gpu_daemon.handle_extract_gpu(
                {"sr": sr, "stems": {"mix": {**base_stem, "spectro_path": written_path}}},
                device,
            )
            self.assertEqual(resp_written["status"], "success")
            self.assertEqual(resp_written["spectro_cache"]["mix"]["status"], "written")
            self.assertEqual(resp_written["spectro_cache"]["mix"]["spectro_path"], written_path)
            self.assertTrue(os.path.isfile(written_path))
            os.remove(written_path)
            self.assertEqual(sorted(os.listdir(tmp_dir)), ["wave.npy"])

            # Inject failure AFTER real NamedTemporaryFile/open_memmap writes valid .tmp.npy
            fallback_path = os.path.join(tmp_dir, "fallback.spectro.npy")
            expected_frames = 1 + len(y) // 512
            captured_tmp_srcs: list[str] = []

            def fail_after_real_tmp_write(src: str, dst: str) -> None:
                self.assertEqual(dst, fallback_path)
                self.assertTrue(os.path.isfile(src), f"expected real temp file before os.replace, got {src}")
                self.assertTrue(src.endswith(".tmp.npy"), f"unexpected temp file suffix: {src}")
                tmp_arr = _REAL_NP_LOAD(src, mmap_mode=None)
                try:
                    self.assertEqual(tmp_arr.shape, (1025, expected_frames))
                    self.assertEqual(tmp_arr.dtype, np.float32)
                finally:
                    del tmp_arr
                captured_tmp_srcs.append(src)
                raise OSError("simulated replace failure")

            with mock.patch.object(
                analyzer.tensor_dsp.os,
                "replace",
                side_effect=fail_after_real_tmp_write,
            ):
                resp_fallback = worker_gpu_daemon.handle_extract_gpu(
                    {"sr": sr, "stems": {"mix": {**base_stem, "spectro_path": fallback_path}}},
                    device,
                )
            self.assertEqual(len(captured_tmp_srcs), 1)
            self.assertFalse(os.path.exists(captured_tmp_srcs[0]), "temp file was not cleaned up after replace error")
            self.assertEqual(resp_fallback["status"], "success")
            self.assertEqual(resp_fallback["tensor"]["mix"], resp_written["tensor"]["mix"])
            self.assertEqual(resp_fallback["spectro_cache"]["mix"]["status"], "fallback")
            self.assertIn("simulated replace failure", resp_fallback["spectro_cache"]["mix"]["error"])
            self.assertFalse(os.path.exists(fallback_path))
            self.assertEqual(sorted(os.listdir(tmp_dir)), ["wave.npy"])

            resp_skipped = worker_gpu_daemon.handle_extract_gpu(
                {"sr": sr, "stems": {"mix": dict(base_stem)}},
                device,
            )
            self.assertEqual(resp_skipped["status"], "success")
            self.assertEqual(resp_skipped["tensor"]["mix"], resp_written["tensor"]["mix"])
            self.assertEqual(resp_skipped["spectro_cache"]["mix"], {"status": "skipped"})

            os.remove(sample_npy)
            self.assertEqual(os.listdir(tmp_dir), [])

    def test_downstream_tracks_1_vs_4_real_flac_parity_vram_rss(self):
        device, flac_path = self._require_cuda_and_flac()
        segments = [
            _load_real_flac_excerpt(flac_path, max_seconds=6.0, offset_seconds=float(i * 6))
            for i in range(4)
        ]

        def run_batch(max_workers: int) -> list[tuple[dict, dict]]:
            with tempfile.TemporaryDirectory() as tmp_dir:
                real_np_load = _REAL_NP_LOAD
                load_lock = threading.Lock()
                recorded_loads: dict[str, list[tuple[tuple, dict]]] = {}

                def batch_spying_load(*args, **kwargs):
                    key = str(args[0]) if args else ""
                    with load_lock:
                        recorded_loads.setdefault(key, []).append((args, kwargs))
                    return real_np_load(*args, **kwargs)

                def worker(idx: int) -> tuple[dict, dict]:
                    y_seg, sr_seg = segments[idx]
                    spec_path = os.path.join(tmp_dir, f"track_{idx}.spectro.npy")
                    try:
                        return _extract_track_json(
                            y_seg,
                            sr_seg,
                            device,
                            track_id=f"track-{idx}",
                            spectro_path=spec_path,
                            tc=self,
                            external_load_log=(load_lock, recorded_loads),
                        )
                    finally:
                        if os.path.exists(spec_path):
                            os.remove(spec_path)

                batch_err: BaseException | None = None
                try:
                    with mock.patch.object(analyzer.core.np, "load", side_effect=batch_spying_load):
                        with concurrent.futures.ThreadPoolExecutor(max_workers=max_workers) as pool:
                            results = list(pool.map(worker, range(4)))
                    return results
                except BaseException as exc:
                    batch_err = exc
                    raise
                finally:
                    remaining = os.listdir(tmp_dir)
                    if batch_err is None:
                        self.assertEqual(remaining, [], f"orphaned spectro files remained after batch: {remaining}")
                    elif remaining:
                        raise AssertionError(
                            f"orphaned spectro files remained after failed batch: {remaining}"
                        ) from batch_err

        res_1, vram_1, rss_1 = _measure_cuda_and_rss(device, lambda: run_batch(max_workers=1))
        res_4, vram_4, rss_4 = _measure_cuda_and_rss(device, lambda: run_batch(max_workers=4))

        self.assertGreater(vram_1, 0.0)
        self.assertGreater(vram_4, 0.0)
        self.assertGreater(rss_1, 0.0)
        self.assertGreater(rss_4, 0.0)
        print(
            f"[PhaseD RolloutGate Downstream1v4] vram_1_mb={vram_1:.2f} vram_4_mb={vram_4:.2f} "
            f"rss_1_mb={rss_1:.2f} rss_4_mb={rss_4:.2f}"
        )

        for idx in range(4):
            self.assertEqual(res_1[idx][0], res_4[idx][0], f"track {idx} tensor mismatch")
            _assert_json_parity(self, res_4[idx][1], res_1[idx][1], path=f"track[{idx}].librosa", exact=True)

        # End-to-end Go Orchestrator DownstreamTracks=1 vs 4 parity & concurrency gate
        repo_root = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
        with tempfile.TemporaryDirectory() as gate_dir:
            track_entries = []
            sample_rate = segments[0][1]
            for idx, (y_seg, sr_seg) in enumerate(segments):
                self.assertEqual(sr_seg, sample_rate)
                seg_path = os.path.join(gate_dir, f"seg_{idx}.npy")
                np.save(seg_path, y_seg)
                track_entries.append(
                    {
                        "track_id": f"track-{idx}",
                        "file_path": seg_path,
                        "shape": list(y_seg.shape),
                        "dtype": "float32",
                    }
                )
            with open(os.path.join(gate_dir, "manifest.json"), "w", encoding="utf-8") as mf:
                json.dump(
                    {
                        "python_path": sys.executable,
                        "repo_root": repo_root,
                        "sr": sample_rate,
                        "tracks": track_entries,
                    },
                    mf,
                )

            _run_go_tests_json(
                self,
                "./dispatcher",
                ["TestPhaseDRolloutGateDownstream1Vs4"],
                extra_env={"FLAC_PHASE_D_GATE_DIR": gate_dir},
            )

            with open(os.path.join(gate_dir, "report_cap_1.json"), encoding="utf-8") as f1:
                report_1 = json.load(f1)
            with open(os.path.join(gate_dir, "report_cap_4.json"), encoding="utf-8") as f4:
                report_4 = json.load(f4)

        self.assertEqual(report_1["max_observed_concurrent"], 1)
        self.assertGreaterEqual(report_4["max_observed_concurrent"], 2)
        self.assertLessEqual(report_4["max_observed_concurrent"], 4)
        self.assertEqual(report_1["orphan_count"], 0)
        self.assertEqual(report_4["orphan_count"], 0)
        print(
            f"[PhaseD GoOrchestrator] cap1_concurrent={report_1['max_observed_concurrent']} "
            f"cap4_concurrent={report_4['max_observed_concurrent']}"
        )

        for idx in range(4):
            t1 = report_1["tracks"][idx]
            t4 = report_4["tracks"][idx]
            self.assertTrue(t1["spectro_used"])
            self.assertTrue(t4["spectro_used"])
            _assert_json_parity(self, t4["librosa"], t1["librosa"], path=f"go.track[{idx}].librosa", exact=True)
            _assert_json_parity(self, t4["tensor"], t1["tensor"], path=f"go.track[{idx}].tensor", exact=True)
            _assert_json_parity(self, t4["essentia"], t1["essentia"], path=f"go.track[{idx}].essentia", exact=True)

    def test_go_orchestrator_downstream_cap_and_two_stage_teardown_gate(self):
        _run_go_tests_json(
            self,
            "./dispatcher",
            [
                "TestAdmissionDownstreamTracks4AndDrain",
                "TestTwoStageWavefrontGPUtoCPUFallbackAndCleanupAfterWait",
                "TestTwoStageWavefrontMixBeforeVocalsOrdering",
                "TestTwoStageWavefrontSingleCPUConsumerVocalsBeforeMixNoDeadlock",
            ],
        )


if __name__ == "__main__":
    unittest.main()
