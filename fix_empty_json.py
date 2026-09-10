"""List empty feature records; --apply repairs only those exact record IDs."""
import argparse
import json
from pathlib import Path
import subprocess
import sys
import tomllib

import psycopg2

ROOT = Path(__file__).resolve().parent


def candidate_query(file=None, ids=None):
    conditions = ["features = '{}'::jsonb"]
    args = []
    if file:
        conditions.append("filepath = %s")
        args.append(file)
    if ids:
        conditions.append("id = ANY(%s)")
        args.append(ids)
    return (
        "SELECT id, filepath, track_number, title, audio_hash FROM raw.library_flac WHERE "
        + " AND ".join(conditions) + " ORDER BY id", args
    )


def repair_command(executable, row):
    return [str(executable), "-config", str(ROOT / "config.toml"),
            "-single-file", row[1], "-fix-record", str(row[0])]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--file", help="Exact filepath stored in the DB")
    parser.add_argument("--id", type=int, action="append", help="Record ID (repeatable)")
    parser.add_argument("--apply", action="store_true", help="Reanalyze selected tracks, write FLAC tags and repair DB rows")
    args = parser.parse_args(argv)
    if args.id and any(value <= 0 for value in args.id):
        parser.error("--id must be positive")
    conn = None
    try:
        with (ROOT / "config.toml").open("rb") as stream:
            config = tomllib.load(stream)
        conn = psycopg2.connect(config["database"]["url"], connect_timeout=10,
                               options="-c default_transaction_read_only=on -c statement_timeout=20000")
        conn.autocommit = True
        with conn.cursor() as cur:
            query, params = candidate_query(args.file, args.id)
            cur.execute(query, params)
            rows = cur.fetchall()
        for row in rows:
            print(json.dumps(dict(zip(("id", "filepath", "track_number", "title"), row[:4])), ensure_ascii=True))
        print(f"Empty-feature candidates: {len(rows)}")
        if not args.apply or not rows:
            if not args.apply:
                print("Preview only. Add --apply to repair the selected records.")
            return 0
        executable = ROOT / "single-orchestrator.exe"
        if not executable.is_file():
            raise RuntimeError("Run update.bat first: single-orchestrator.exe is missing")
        help_result = subprocess.run([str(executable), "-h"], cwd=ROOT, capture_output=True, timeout=20)
        if b"-fix-record" not in help_result.stdout + help_result.stderr:
            raise RuntimeError("Run update.bat first: the installed binary lacks -fix-record")
        # Validate all paths before launching the first repair.
        for row in rows:
            if not Path(row[1]).is_file():
                raise RuntimeError(f"Record {row[0]}: FLAC path is unavailable: {row[1]}")
        for row in rows:
            result = subprocess.run(repair_command(executable, row), cwd=ROOT)
            if result.returncode != 0:
                raise RuntimeError(f"Record {row[0]} repair failed (exit {result.returncode}); stopped")
            with conn.cursor() as cur:
                cur.execute("SELECT features <> '{}'::jsonb FROM raw.library_flac WHERE id=%s AND filepath=%s AND track_number=%s AND audio_hash=%s",
                            (row[0], row[1], row[2], row[4]))
                verified = cur.fetchone()
            if verified != (True,):
                raise RuntimeError(f"Record {row[0]} did not pass DB readback; stopped")
            print(f"Repaired and verified record {row[0]}")
        return 0
    except psycopg2.Error as exc:
        print(f"DB access failed ({type(exc).__name__}); no credentials displayed", file=sys.stderr)
        return 1
    except (OSError, RuntimeError, KeyError, ValueError, subprocess.SubprocessError) as exc:
        print(str(exc), file=sys.stderr)
        return 1
    finally:
        if conn is not None:
            conn.close()


if __name__ == "__main__":
    raise SystemExit(main())
