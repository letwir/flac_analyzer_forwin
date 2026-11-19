#!/usr/bin/env python3
"""Import fully analyzed PostgreSQL tracks into orchestrator task_state."""

from __future__ import annotations

import os
import sqlite3
import sys
import tomllib
from pathlib import Path

import psycopg2

ROOT = Path(__file__).resolve().parent
SQLITE_PATH = ROOT / "orchestrator" / "orchestrator.db"
SCHEMA_VERSION = 1
REQUIRED_STEMS = {"bass", "drums", "vocals", "other", "guitar", "piano"}


def get_database_url() -> str:
    config_path = ROOT / "config.toml"
    if config_path.is_file():
        with config_path.open("rb") as config_file:
            url = tomllib.load(config_file).get("database", {}).get("url", "")
        if url:
            return url
    url = os.environ.get("FLAC_DB_URL", "")
    if not url:
        raise RuntimeError("DB URL is missing from config.toml [database].url and FLAC_DB_URL")
    return url


def is_complete(meta: object, features: object, predictions: object) -> bool:
    if not isinstance(meta, dict) or meta.get("analysis_schema_version") != SCHEMA_VERSION:
        return False
    if not isinstance(features, dict) or not isinstance(predictions, dict) or not predictions:
        return False
    mix = features.get("mix")
    if not isinstance(mix, dict) or not mix:
        return False
    demucs = features.get("demucs")
    return isinstance(demucs, dict) and all(
        isinstance(demucs.get(stem), dict) and bool(demucs[stem])
        for stem in REQUIRED_STEMS
    )


def main() -> int:
    if not SQLITE_PATH.is_file():
        raise RuntimeError(f"SQLite state database not found: {SQLITE_PATH}")

    with psycopg2.connect(get_database_url()) as pg_conn:
        with pg_conn.cursor() as cursor:
            cursor.execute(
                """SELECT filepath, track_number, meta, features, predictions
                   FROM raw.library_flac
                   WHERE analyzed_at IS NOT NULL"""
            )
            records = [row for row in cursor.fetchall() if is_complete(*row[2:])]

    with sqlite3.connect(SQLITE_PATH, timeout=30) as sqlite_conn:
        sqlite_conn.execute("PRAGMA busy_timeout = 30000")
        sqlite_conn.execute("BEGIN IMMEDIATE")
        sqlite_conn.executemany(
            """INSERT INTO task_state (file_path, track_number, status, error_message, updated_at)
               VALUES (?, ?, 'COMPLETED', 'Imported from PostgreSQL', CURRENT_TIMESTAMP)
               ON CONFLICT(file_path, track_number) DO UPDATE SET
                   status = 'COMPLETED',
                   error_message = 'Imported from PostgreSQL',
                   updated_at = CURRENT_TIMESTAMP
               WHERE task_state.status NOT IN ('RUNNING', 'QUEUED')""",
            [(filepath, track_number or 0) for filepath, track_number, *_ in records],
        )
    print(f"PostgreSQL 完全解析済み曲を SQLite に反映しました: {len(records)} 件")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"PostgreSQL → SQLite 同期に失敗しました: {exc}", file=sys.stderr)
        raise SystemExit(1)
