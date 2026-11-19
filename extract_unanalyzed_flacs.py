#!/usr/bin/env python3
"""Extract unanalyzed or incomplete FLAC files by diffing disk paths against PostgreSQL.

Mor: (Roots x PgConfig) -> PrioritizedTargets (IO Effect + Pure Selection Functor)
Semantics(Category: Read-Only Preflight Diff & Priority Projection)
"""

from __future__ import annotations

import argparse
import contextlib
from dataclasses import dataclass
import json
import ntpath
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tomllib
from typing import Callable, Iterable, Mapping, Sequence

import psycopg2

ROOT = Path(__file__).resolve().parent
DEFAULT_ROOTS = (r"M:\Music\album", r"N:\Music")
EMPTY_JSONB_THRESHOLD_BYTES = 50  # Short-circuit TOAST reads; '{}' in JSONB is ~5-8 bytes.

PG_SUMMARY_QUERY = """
SELECT
    filepath,
    count(*) AS total_rows,
    count(*) FILTER (
        WHERE analyzed_at IS NULL
           OR features IS NULL
           OR (CASE WHEN pg_column_size(features) < %s THEN features = '{}'::jsonb ELSE false END)
           OR predictions IS NULL
           OR (CASE WHEN pg_column_size(predictions) < %s THEN predictions = '{}'::jsonb ELSE false END)
    ) AS incomplete_rows
FROM raw.library_flac
GROUP BY filepath
"""


@dataclass(frozen=True)
class PgFileSummary:
    """Aggregated PostgreSQL analysis completeness for one normalized file path."""

    total_rows: int
    incomplete_rows: int


@dataclass(frozen=True)
class ExtractedTarget:
    """Prioritized unanalyzed or incomplete FLAC file candidate."""

    path: str
    reason: str  # "missing" | "empty"
    size_bytes: int
    pg_rows: int
    incomplete_rows: int


def normalize_path_key(path: str) -> str:
    """Pure morphism: canonicalize a Windows path into a case-folded comparison key."""
    cleaned = path.strip().replace("/", "\\")
    if not cleaned:
        return ""
    if ntpath.isabs(cleaned):
        return ntpath.normpath(cleaned).lower()
    return os.path.normpath(os.path.abspath(cleaned)).replace("/", "\\").lower()


def aggregate_pg_summaries(rows: Iterable[tuple[str, int, int]]) -> dict[str, PgFileSummary]:
    """Pure morphism: merge PostgreSQL row counts across case/slash path variants."""
    summaries: dict[str, PgFileSummary] = {}
    for raw_path, total_rows, incomplete_rows in rows:
        key = normalize_path_key(raw_path)
        if not key:
            continue
        prev = summaries.get(key)
        if prev is None:
            summaries[key] = PgFileSummary(total_rows=int(total_rows), incomplete_rows=int(incomplete_rows))
            continue
        summaries[key] = PgFileSummary(
            total_rows=prev.total_rows + int(total_rows),
            incomplete_rows=prev.incomplete_rows + int(incomplete_rows),
        )
    return summaries


def classify_target_reason(
    summary: PgFileSummary | None,
    include_missing: bool,
    include_empty: bool,
) -> str | None:
    """Pure guard: classify whether a disk file needs priority scanning."""
    if summary is None:
        return "missing" if include_missing else None
    if summary.incomplete_rows > 0:
        return "empty" if include_empty else None
    return None


def priority_sort_key(target: ExtractedTarget, order: str) -> tuple[int, int, str]:
    """Pure ordering morphism for extracted scan candidates."""
    if order == "size-asc":
        return (0, target.size_bytes, target.path.lower())
    if order == "empty-first":
        bucket = 0 if target.reason == "empty" else 1
        return (bucket, target.size_bytes, target.path.lower())
    bucket = 0 if target.reason == "missing" else 1
    return (bucket, target.size_bytes, target.path.lower())


def select_and_prioritize_targets(
    disk_paths: Sequence[str],
    pg_summaries: Mapping[str, PgFileSummary],
    size_lookup: Callable[[str], int],
    *,
    include_missing: bool = True,
    include_empty: bool = True,
    priority: str = "missing-first",
) -> list[ExtractedTarget]:
    """Pure selection functor: diff disk FLACs against PG summaries and order candidates."""
    seen_keys: set[str] = set()
    targets: list[ExtractedTarget] = []
    for raw_path in disk_paths:
        key = normalize_path_key(raw_path)
        if not key or key in seen_keys:
            continue
        seen_keys.add(key)

        summary = pg_summaries.get(key)
        reason = classify_target_reason(summary, include_missing, include_empty)
        if reason is None:
            continue

        targets.append(
            ExtractedTarget(
                path=raw_path,
                reason=reason,
                size_bytes=size_lookup(raw_path),
                pg_rows=0 if summary is None else summary.total_rows,
                incomplete_rows=0 if summary is None else summary.incomplete_rows,
            )
        )

    targets.sort(key=lambda item: priority_sort_key(item, priority))
    return targets


def build_summary_payload(
    roots: Sequence[str],
    disk_count: int,
    pg_file_count: int,
    targets: Sequence[ExtractedTarget],
) -> dict[str, object]:
    """Pure projection: assemble machine-readable summary and target list."""
    missing_count = sum(1 for item in targets if item.reason == "missing")
    empty_count = sum(1 for item in targets if item.reason == "empty")
    return {
        "roots": list(roots),
        "disk_files": disk_count,
        "pg_files": pg_file_count,
        "missing_count": missing_count,
        "empty_count": empty_count,
        "total_targets": len(targets),
        "targets": [
            {
                "path": item.path,
                "reason": item.reason,
                "size_bytes": item.size_bytes,
                "pg_rows": item.pg_rows,
                "incomplete_rows": item.incomplete_rows,
            }
            for item in targets
        ],
    }


def load_database_settings(config_path: Path) -> tuple[str, int]:
    """IO boundary: load PostgreSQL URL and timeout without leaking secrets."""
    db_url = ""
    timeout_sec = 20
    if config_path.is_file():
        with config_path.open("rb") as stream:
            loaded = tomllib.load(stream)
        db_section = loaded.get("database", {})
        db_url = str(db_section.get("url", "") or "").strip()
        raw_timeout = db_section.get("db_timeout_sec", 20)
        if isinstance(raw_timeout, int) and not isinstance(raw_timeout, bool) and raw_timeout > 0:
            timeout_sec = raw_timeout

    if not db_url:
        db_url = os.environ.get("FLAC_DB_URL", "").strip()
    if not db_url:
        raise RuntimeError(f"PostgreSQL URL is missing in {config_path} [database].url and FLAC_DB_URL")
    return db_url, timeout_sec


def fetch_pg_rows(db_url: str, timeout_sec: int) -> list[tuple[str, int, int]]:
    """IO boundary: read aggregated completeness rows in a read-only transaction."""
    connect_timeout = min(max(timeout_sec, 5), 15)
    statement_timeout_ms = max(timeout_sec, 5) * 1000
    pg_options = f"-c default_transaction_read_only=on -c statement_timeout={statement_timeout_ms}"
    try:
        with contextlib.closing(
            psycopg2.connect(db_url, connect_timeout=connect_timeout, options=pg_options)
        ) as conn:
            conn.autocommit = True
            with conn.cursor() as cur:
                cur.execute(PG_SUMMARY_QUERY, (EMPTY_JSONB_THRESHOLD_BYTES, EMPTY_JSONB_THRESHOLD_BYTES))
                return [(str(r[0]), int(r[1]), int(r[2])) for r in cur.fetchall()]
    except psycopg2.Error as exc:
        # Never expose the raw connection URI or credentials in diagnostics or __cause__.
        raise RuntimeError(f"PostgreSQL query failed ({type(exc).__name__}); credentials omitted") from None


def enumerate_root_flacs(root: str) -> list[str]:
    """IO boundary: recursively enumerate .flac files under one file or directory."""
    target = Path(root)
    if not target.exists():
        raise FileNotFoundError(f"Target root does not exist: {root}")
    if target.is_file():
        return [os.path.normpath(os.path.abspath(root))]

    fd_bin = shutil.which("fd.exe") or shutil.which("fd")
    if fd_bin:
        res = subprocess.run(
            [fd_bin, "-I", "-e", "flac", "-t", "f", "-a", "--search-path", root],
            capture_output=True, text=True, encoding="utf-8", errors="replace", check=True,
        )
        return [line.strip() for line in res.stdout.splitlines() if line.strip()]

    rg_bin = shutil.which("rg.exe") or shutil.which("rg")
    if rg_bin:
        res = subprocess.run(
            [rg_bin, "--no-ignore", "--files", "-g", "*.flac", root],
            capture_output=True, text=True, encoding="utf-8", errors="replace", check=True,
        )
        return [line.strip() for line in res.stdout.splitlines() if line.strip()]

    return [str(p) for p in target.rglob("*") if p.is_file() and p.suffix.lower() == ".flac"]


def collect_disk_flacs(roots: Sequence[str]) -> list[str]:
    """IO boundary: gather unique FLAC file paths across all configured roots."""
    collected: list[str] = []
    seen: set[str] = set()
    for root in roots:
        for path in enumerate_root_flacs(root):
            key = normalize_path_key(path)
            if key and key not in seen:
                seen.add(key)
                collected.append(path)
    return collected


def stat_file_size(path: str) -> int:
    """IO boundary: measure byte size of an extracted candidate for priority sorting."""
    try:
        return os.path.getsize(path)
    except OSError as exc:
        raise OSError(f"Failed to stat extracted FLAC {path}: {exc}") from exc


def write_paths_utf8(output_path: Path, targets: Sequence[ExtractedTarget]) -> None:
    """IO boundary: persist extracted paths as UTF-8 without BOM."""
    output_path.parent.mkdir(parents=True, exist_ok=True)
    content = "\n".join(item.path for item in targets)
    if content:
        content += "\n"
    output_path.write_text(content, encoding="utf-8", newline="\n")


def parse_args(argv: Sequence[str] | None) -> argparse.Namespace:
    """Parse CLI arguments for unanalyzed FLAC extraction."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--roots", nargs="+", default=list(DEFAULT_ROOTS), help="FLAC files or root directories")
    parser.add_argument("--config", type=Path, default=ROOT / "config.toml", help="Path to config.toml")
    parser.add_argument("--include-missing", action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument("--include-empty", action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument("--priority", choices=["missing-first", "empty-first", "size-asc"], default="missing-first")
    parser.add_argument("--format", choices=["paths", "json"], default="paths")
    parser.add_argument("--output", type=Path, help="Optional UTF-8 file to write extracted paths")
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")
    if hasattr(sys.stderr, "reconfigure"):
        sys.stderr.reconfigure(encoding="utf-8")

    args = parse_args(argv)
    if not args.include_missing and not args.include_empty:
        raise ValueError("At least one of --include-missing or --include-empty must be enabled")

    db_url, timeout_sec = load_database_settings(args.config)
    pg_summaries = aggregate_pg_summaries(fetch_pg_rows(db_url, timeout_sec))
    disk_paths = collect_disk_flacs(args.roots)
    targets = select_and_prioritize_targets(
        disk_paths,
        pg_summaries,
        stat_file_size,
        include_missing=args.include_missing,
        include_empty=args.include_empty,
        priority=args.priority,
    )

    if args.output is not None:
        write_paths_utf8(args.output, targets)

    if args.format == "json":
        payload = build_summary_payload(args.roots, len(disk_paths), len(pg_summaries), targets)
        print(json.dumps(payload, ensure_ascii=False))
        return 0

    for item in targets:
        print(item.path)
    missing_count = sum(1 for item in targets if item.reason == "missing")
    empty_count = sum(1 for item in targets if item.reason == "empty")
    print(
        f"[Summary] disk={len(disk_paths)} pg_files={len(pg_summaries)} "
        f"targets={len(targets)} (missing={missing_count}, empty={empty_count})",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"Extraction failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
