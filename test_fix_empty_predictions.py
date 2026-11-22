import importlib.util
import os
import sys
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import numpy as np

PROJECT_ROOT = os.path.dirname(__file__)
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

import flac_decode


def _load_fix_empty_predictions():
    path = os.path.join(PROJECT_ROOT, "zig", "fix_empty_predictions.py")
    spec = importlib.util.spec_from_file_location("fix_empty_predictions", path)
    mod = importlib.util.module_from_spec(spec)
    assert spec is not None and spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


fix_pred = _load_fix_empty_predictions()


class FixEmptyPredictionsTests(unittest.TestCase):
    def test_build_select_query_filters_and_ordering(self):
        sql, params = fix_pred.build_select_query()
        self.assertIn("predictions IS NULL OR predictions = '{}'::jsonb", sql)
        self.assertIn("features IS NOT NULL AND features <> '{}'::jsonb", sql)
        self.assertIn("ORDER BY filepath, track_number, id", sql)
        self.assertEqual([], params)

        sql_filtered, params_filtered = fix_pred.build_select_query(
            ids=[83484, 83485],
            filepath=r"M:\Music\album.flac",
            include_empty_features=True,
            limit=10,
        )
        self.assertNotIn("features <> '{}'::jsonb", sql_filtered)
        self.assertIn("id = ANY(%s)", sql_filtered)
        self.assertIn("filepath = %s", sql_filtered)
        self.assertIn("LIMIT %s", sql_filtered)
        self.assertEqual([[83484, 83485], r"M:\Music\album.flac", 10], params_filtered)

    def test_select_track_slice_single_and_cue_multitrack(self):
        single_slice = flac_decode.TrackSlice(1, 0, 44100, "Song", "Artist")
        single_handle = flac_decode.FlacHandle(
            filepath="single.flac",
            sample_rate=44100,
            channels=2,
            bits_per_sample=16,
            total_samples=44100,
            tags={},
            slices=[single_slice],
        )
        self.assertEqual(single_slice, fix_pred.select_track_slice(single_handle, 1))
        self.assertEqual(single_slice, fix_pred.select_track_slice(single_handle, None))

        s1 = flac_decode.TrackSlice(1, 0, 1000, "T1", "A")
        s16 = flac_decode.TrackSlice(16, 1000, 2000, "T16", "A")
        cue_handle = flac_decode.FlacHandle(
            filepath="album.flac",
            sample_rate=44100,
            channels=2,
            bits_per_sample=16,
            total_samples=2000,
            tags={},
            slices=[s1, s16],
        )
        self.assertEqual(s16, fix_pred.select_track_slice(cue_handle, 16))

        with self.assertRaises(ValueError):
            fix_pred.select_track_slice(cue_handle, 99)

    def test_extract_track_predictions_uses_handle_cache_and_fails_on_empty(self):
        s1 = flac_decode.TrackSlice(1, 0, 44100, "T1", "A")
        s16 = flac_decode.TrackSlice(16, 44100, 88200, "T16", "A")
        cue_handle = flac_decode.FlacHandle(
            filepath="album.flac",
            sample_rate=44100,
            channels=2,
            bits_per_sample=16,
            total_samples=88200,
            tags={},
            slices=[s1, s16],
        )
        cache = {}
        patches = np.zeros((64, 128, 96), dtype=np.float32)

        with (
            patch.object(fix_pred.flac_decode, "build_flac_handle", return_value=cue_handle) as build_mock,
            patch.object(
                fix_pred.flac_decode,
                "process_slice_with_seq_safety",
                return_value=(np.ones(44100, dtype=np.float32), "deadbeef"),
            ) as dec_mock,
            patch.object(fix_pred, "extract_mel_patches", return_value=patches),
            patch.object(
                fix_pred,
                "run_essentia_serialized",
                return_value={"ESSENTIA_MOOD_HAPPY_HAPPY": 0.75},
            ),
        ):
            out1 = fix_pred.extract_track_predictions("album.flac", 1, {"mood": SimpleNamespace()}, cache)
            out16 = fix_pred.extract_track_predictions("album.flac", 16, {"mood": SimpleNamespace()}, cache)

        self.assertEqual({"ESSENTIA_MOOD_HAPPY_HAPPY": 0.75}, out1)
        self.assertEqual({"ESSENTIA_MOOD_HAPPY_HAPPY": 0.75}, out16)
        self.assertEqual(1, build_mock.call_count)
        self.assertEqual(2, dec_mock.call_count)
        self.assertEqual(("album.flac", 44100, 88200, 44100, 2), dec_mock.call_args_list[1].args)

        with (
            patch.object(
                fix_pred.flac_decode,
                "process_slice_with_seq_safety",
                return_value=(np.ones(44100, dtype=np.float32), "deadbeef"),
            ),
            patch.object(fix_pred, "extract_mel_patches", return_value=patches),
            patch.object(fix_pred, "run_essentia_serialized", return_value={}),
        ):
            with self.assertRaisesRegex(RuntimeError, "empty predictions"):
                fix_pred.extract_track_predictions("album.flac", 16, {"mood": SimpleNamespace()}, cache)

    def test_guarded_update_sql_only_mutates_predictions(self):
        sql = fix_pred.GUARDED_UPDATE_SQL
        self.assertIn("SET predictions = %s", sql)
        self.assertIn("WHERE id = %s", sql)
        self.assertIn("AND audio_hash = %s", sql)
        self.assertIn("predictions IS NULL OR predictions = '{}'::jsonb", sql)
        self.assertNotIn("features =", sql)
        self.assertNotIn("meta =", sql)
        self.assertNotIn("analyzed_at =", sql)


if __name__ == "__main__":
    unittest.main()
