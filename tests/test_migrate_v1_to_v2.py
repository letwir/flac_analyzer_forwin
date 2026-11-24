import os
import subprocess
import sys
import unittest

from zig.migrate_v1_to_v2 import (
    build_orchestrator_command_pure,
    build_v1_select_sql,
    check_v2_schema,
    group_rows_by_filepath_pure,
    is_v2_feature_complete_pure,
)


class TestMigrateV1ToV2(unittest.TestCase):
    def test_is_v2_feature_complete_pure(self):
        v1_features = {"mix": {"scalars": {"bpm": 120.0}, "sequences": {}}}
        self.assertFalse(is_v2_feature_complete_pure(None, require_demucs=True))
        self.assertFalse(is_v2_feature_complete_pure({}, require_demucs=False))
        self.assertFalse(is_v2_feature_complete_pure(v1_features, require_demucs=False))

        mix_v2 = {
            "mix": {
                "scalars": {
                    "stereo": {"pan_mean": 0.0},
                    "psychoacoustics": {"sharpness_acum": 1.2},
                    "audio_cutoff_lufs": {"integrated_lufs": -14.0},
                }
            }
        }
        self.assertTrue(is_v2_feature_complete_pure(mix_v2, require_demucs=False))
        self.assertFalse(is_v2_feature_complete_pure(mix_v2, require_demucs=True))

        full_v2 = {
            **mix_v2,
            "demucs": {
                "vocals": {
                    "scalars": {
                        "cross_stem": {"energy_ratio": 0.5},
                    }
                }
            },
        }
        self.assertTrue(is_v2_feature_complete_pure(full_v2, require_demucs=True))

    def test_build_v1_select_sql(self):
        sql, params = build_v1_select_sql(ids=[10, 20], filepath="a/b.flac", require_demucs=True, limit=25)
        self.assertIn("FROM raw.library_flac", sql)
        self.assertIn("cross_stem", sql)
        self.assertIn("audio_cutoff_lufs", sql)
        self.assertIn("id = ANY(%s)", sql)
        self.assertIn("filepath = %s", sql)
        self.assertIn("LIMIT %s", sql)
        self.assertEqual(params, [[10, 20], "a/b.flac", 25])

        sql_mix, params_mix = build_v1_select_sql(require_demucs=False, limit=0)
        self.assertNotIn("cross_stem", sql_mix)
        self.assertNotIn("LIMIT %s", sql_mix)
        self.assertEqual(params_mix, [])

    def test_group_rows_by_filepath_pure_preserves_order(self):
        rows = [
            {"id": 1, "filepath": "album_b.flac", "track_number": 1},
            {"id": 2, "filepath": "album_a.flac", "track_number": 1},
            {"id": 3, "filepath": "album_b.flac", "track_number": 2},
        ]
        grouped = group_rows_by_filepath_pure(rows)
        self.assertEqual([fp for fp, _ in grouped], ["album_b.flac", "album_a.flac"])
        self.assertEqual([r["id"] for r in grouped[0][1]], [1, 3])
        self.assertEqual([r["id"] for r in grouped[1][1]], [2])

    def test_build_orchestrator_command_pure(self):
        cmd = build_orchestrator_command_pure(
            r"F:\repo\flac_analyzer_forwin\orchestrator\orchestrator.exe",
            r"N:\Music\test.flac",
            config_path=r"F:\repo\flac_analyzer_forwin\config.toml",
            force=True,
        )
        self.assertEqual(
            cmd,
            [
                r"F:\repo\flac_analyzer_forwin\orchestrator\orchestrator.exe",
                "-single-file",
                r"N:\Music\test.flac",
                "-config",
                r"F:\repo\flac_analyzer_forwin\config.toml",
                "-force",
            ],
        )
        cmd_min = build_orchestrator_command_pure("orchestrator.exe", "x.flac", config_path=None, force=False)
        self.assertEqual(cmd_min, ["orchestrator.exe", "-single-file", "x.flac"])

    def test_check_v2_schema_identifies_missing_columns(self):
        class _FakeCursor:
            def __init__(self, cols):
                self._cols = cols

            def execute(self, _sql):
                pass

            def fetchall(self):
                return [(c,) for c in self._cols]

        self.assertEqual(
            check_v2_schema(_FakeCursor(["id", "filepath"])),
            ["track_number", "album_artist", "album", "artist", "title"],
        )
        self.assertEqual(
            check_v2_schema(_FakeCursor(["track_number", "album_artist", "album", "artist", "title"])),
            [],
        )

    def test_cli_help_smoke(self):
        repo_root = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
        script_path = os.path.join(repo_root, "zig", "migrate_v1_to_v2.py")
        proc = subprocess.run(
            [sys.executable, script_path, "--help"],
            cwd=repo_root,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("--mode", proc.stdout)
        self.assertIn("--apply-schema", proc.stdout)
        self.assertIn("--dry-run", proc.stdout)


if __name__ == "__main__":
    unittest.main()
