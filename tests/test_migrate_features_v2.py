"""
tests/test_migrate_features_v2.py
=================================
P3-M2-S3: zig/migrate_features.py のステレオ解析付き純粋計算・JSONB マージ・
パラメータ化 SQL 構築の検証テストですわ！
"""

import os
import sys
import unittest

import numpy as np

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from zig.migrate_features import (
    _build_migration_select_sql,
    compute_additional_features_pure,
    merge_jsonb_features_pure,
)


class TestMigrateFeaturesV2(unittest.TestCase):
    def test_compute_additional_features_with_stereo_and_merge(self) -> None:
        sr = 22050
        t = np.linspace(0.0, 2.0, sr * 2, endpoint=False, dtype=np.float32)
        left = 0.4 * np.sin(2.0 * np.pi * 440.0 * t)
        right = np.zeros_like(left)
        # soundfile 形式の (N, 2) ステレオ入力
        y_stereo = np.stack([left, right], axis=-1).astype(np.float32)

        add_feat = compute_additional_features_pure(y_stereo, sr)
        self.assertIn("stereo", add_feat["scalars"])
        self.assertAlmostEqual(add_feat["scalars"]["stereo"]["pan_mean"], -1.0, places=4)
        self.assertIn("pan_seq_32", add_feat["sequences"])
        self.assertIn("width_seq_32", add_feat["sequences"])
        self.assertIn("psychoacoustics", add_feat["scalars"])
        self.assertIn("structure_ssm", add_feat["scalars"])
        self.assertIn("voice_cpp", add_feat["scalars"])
        self.assertIn("audio_cutoff_lufs", add_feat["scalars"])

        existing = {"mix": {"scalars": {"bpm": 120.0}, "sequences": {}}, "demucs": {"vocals": {"scalars": {"energy": 0.1}}}}
        merged = merge_jsonb_features_pure(existing, add_feat)
        self.assertEqual(merged["mix"]["scalars"]["bpm"], 120.0)
        self.assertIn("stereo", merged["mix"]["scalars"])
        self.assertIn("demucs", merged)

    def test_migration_select_sql_parameterized(self) -> None:
        sql, params = _build_migration_select_sql(target_hash="abc123", target_id=99, limit=1)
        self.assertIn("id = %s", sql)
        self.assertIn("audio_hash = %s", sql)
        self.assertIn("LIMIT %s", sql)
        self.assertEqual(params, [99, "abc123", 1])


if __name__ == "__main__":
    unittest.main()
