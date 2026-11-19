"""Focused tests for extract_unanalyzed_flacs.py.

Mor: SyntheticFixtures -> TestAssertions (Pure + Mocked IO Boundary)
"""

import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import psycopg2

import extract_unanalyzed_flacs as extractor


class ExtractUnanalyzedFlacsTests(unittest.TestCase):
    def test_normalize_path_key_handles_slashes_case_and_dotdot(self) -> None:
        self.assertEqual(
            extractor.normalize_path_key(r"M:/Music/album/../album/Track01.FLAC"),
            r"m:\music\album\track01.flac",
        )

    def test_aggregate_pg_summaries_merges_case_and_slash_variants(self) -> None:
        rows = [
            (r"M:\Music\Album\Disc.flac", 3, 1),
            (r"m:/music/album/disc.flac", 2, 1),
            (r"N:\Music\Complete.flac", 4, 0),
        ]
        summaries = extractor.aggregate_pg_summaries(rows)
        self.assertEqual(len(summaries), 2)
        self.assertEqual(
            summaries[r"m:\music\album\disc.flac"],
            extractor.PgFileSummary(total_rows=5, incomplete_rows=2),
        )
        self.assertEqual(
            summaries[r"n:\music\complete.flac"],
            extractor.PgFileSummary(total_rows=4, incomplete_rows=0),
        )

    def test_select_and_prioritize_targets_orders_and_deduplicates(self) -> None:
        disk = [
            r"M:\Music\album\complete.flac",
            r"M:\Music\album\empty_big.flac",
            r"M:\Music\album\missing_big.flac",
            r"N:\Music\missing_small.flac",
            r"n:/music/missing_small.flac",
            r"N:\Music\empty_small.flac",
        ]
        pg = extractor.aggregate_pg_summaries([
            (r"M:\Music\album\complete.flac", 2, 0),
            (r"M:\Music\album\empty_big.flac", 2, 1),
            (r"N:\Music\empty_small.flac", 1, 1),
        ])
        sizes = {
            r"M:\Music\album\complete.flac": 999,
            r"M:\Music\album\empty_big.flac": 500,
            r"M:\Music\album\missing_big.flac": 400,
            r"N:\Music\missing_small.flac": 100,
            r"N:\Music\empty_small.flac": 50,
        }

        missing_first = extractor.select_and_prioritize_targets(
            disk, pg, lambda p: sizes[p], priority="missing-first"
        )
        self.assertEqual(
            [(t.path, t.reason) for t in missing_first],
            [
                (r"N:\Music\missing_small.flac", "missing"),
                (r"M:\Music\album\missing_big.flac", "missing"),
                (r"N:\Music\empty_small.flac", "empty"),
                (r"M:\Music\album\empty_big.flac", "empty"),
            ],
        )

        empty_first = extractor.select_and_prioritize_targets(
            disk, pg, lambda p: sizes[p], priority="empty-first"
        )
        self.assertEqual(
            [t.path for t in empty_first],
            [
                r"N:\Music\empty_small.flac",
                r"M:\Music\album\empty_big.flac",
                r"N:\Music\missing_small.flac",
                r"M:\Music\album\missing_big.flac",
            ],
        )

        size_asc = extractor.select_and_prioritize_targets(
            disk, pg, lambda p: sizes[p], priority="size-asc"
        )
        self.assertEqual(
            [t.path for t in size_asc],
            [
                r"N:\Music\empty_small.flac",
                r"N:\Music\missing_small.flac",
                r"M:\Music\album\missing_big.flac",
                r"M:\Music\album\empty_big.flac",
            ],
        )

        missing_only = extractor.select_and_prioritize_targets(
            disk, pg, lambda p: sizes[p], include_empty=False
        )
        self.assertEqual([t.reason for t in missing_only], ["missing", "missing"])

    def test_fetch_pg_rows_redacts_credentials_on_db_error(self) -> None:
        secret_url = "postgres://secret_user:super_secret_pw@db.example.invalid:5432/db"
        with mock.patch.object(
            extractor.psycopg2,
            "connect",
            side_effect=psycopg2.OperationalError(f"failed connecting to {secret_url}"),
        ):
            with self.assertRaises(RuntimeError) as ctx:
                extractor.fetch_pg_rows(secret_url, 10)
        message = str(ctx.exception)
        self.assertIsNone(ctx.exception.__cause__)
        self.assertIn("credentials omitted", message)
        self.assertNotIn("secret_user", message)
        self.assertNotIn("super_secret_pw", message)

    def test_main_json_and_output_file_utf8_without_bom(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = Path(tmpdir)
            cfg_path = tmp / "config.toml"
            cfg_path.write_text('[database]\nurl = "postgres://u:p@localhost:5432/db"\n', encoding="utf-8")
            out_path = tmp / "targets.txt"
            disk_files = [r"M:\Music\album\新曲.flac", r"M:\Music\album\済.flac"]
            pg_rows = [(r"M:\Music\album\済.flac", 1, 0)]

            with (
                mock.patch.object(extractor, "fetch_pg_rows", return_value=pg_rows),
                mock.patch.object(extractor, "collect_disk_flacs", return_value=disk_files),
                mock.patch.object(extractor, "stat_file_size", return_value=1234),
                mock.patch("sys.stdout", new_callable=lambda: __import__("io").StringIO()) as fake_out,
            ):
                rc = extractor.main([
                    "--roots", r"M:\Music\album",
                    "--config", str(cfg_path),
                    "--format", "json",
                    "--output", str(out_path),
                ])

            self.assertEqual(rc, 0)
            payload = json.loads(fake_out.getvalue())
            self.assertEqual(payload["disk_files"], 2)
            self.assertEqual(payload["pg_files"], 1)
            self.assertEqual(payload["missing_count"], 1)
            self.assertEqual(payload["empty_count"], 0)
            self.assertEqual(payload["total_targets"], 1)
            raw_bytes = out_path.read_bytes()
            self.assertFalse(raw_bytes.startswith(b"\xef\xbb\xbf"))
            self.assertEqual(raw_bytes.decode("utf-8"), "M:\\Music\\album\\新曲.flac\n")


if __name__ == "__main__":
    unittest.main()
