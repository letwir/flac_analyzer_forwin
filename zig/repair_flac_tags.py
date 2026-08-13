"""
zig/repair_flac_tags.py
========================
PostgreSQL DB (raw.library_flac) から既存の解析データを参照し、
FLAC ファイル本体に未書き込み/不足している VorbisComment タグを検出して
CUE シート有無に応じたプレフィックス切り替え（filepath ごとの一括グループ化）を行い、
重複書き込みゼロで安全に一括補完焼き込みを行う独立治具ですわ！
"""

import argparse
from collections import defaultdict
import json
import logging
import os
import sys
import tomllib
import psycopg2
import psycopg2.extras
from mutagen.flac import FLAC

# 親ディレクトリを sys.path に追加してプロジェクト内モジュール (flac_tagger.py 等) をインポート
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from flac_tagger import build_flac_tags, write_flac_tags_with_retry

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [RepairZig] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)]
    )

def find_config() -> dict:
    search_paths = [
        os.path.join(os.path.dirname(__file__), "config.toml"),
        os.path.join(PROJECT_ROOT, "config.toml"),
        os.path.join(os.getcwd(), "config.toml")
    ]
    for path in search_paths:
        if os.path.exists(path):
            try:
                with open(path, "rb") as f:
                    logging.info(f"設定ファイルから設定をロードいたしました: {path}")
                    return tomllib.load(f)
            except Exception as e:
                logging.warning(f"{path} のパース中に警告が発生いたしました: {e}")
    return {}

def get_db_url(config: dict) -> str:
    db_url = config.get("database", {}).get("url", "")
    if not db_url:
        db_url = os.environ.get("FLAC_DB_URL", "")
    if not db_url:
        logging.error("DB URL が設定ファイルまたは環境変数 FLAC_DB_URL から取得できませんでした。")
        sys.exit(1)
    return db_url

def build_tags_for_file_group(file_rows: list[tuple]) -> dict[str, str]:
    all_expected_tags: dict[str, str] = {}
    is_multi_track = len(file_rows) > 1

    for idx, row in enumerate(file_rows):
        rec_id, filepath, audio_hash, meta, features = row
        if not isinstance(features, dict):
            continue

        mix_feat = features.get("mix", {})
        scalars = mix_feat.get("scalars", {})
        predictions = mix_feat.get("predictions", {})

        tensor_feats = {}
        for k, v in mix_feat.items():
            if k not in ("source", "scalars", "sequences", "predictions"):
                tensor_feats[k] = v

        if is_multi_track:
            tr_num = idx + 1
            if isinstance(meta, dict):
                tn = meta.get("tracknumber") or meta.get("TRACKNUMBER") or meta.get("track")
                if tn:
                    try:
                        tr_num = int(str(tn).split("/")[0])
                    except ValueError:
                        pass
            prefix = f"CUE_TRACK{tr_num:02d}"
        else:
            prefix = ""

        librosa_data = {"scalars": scalars}
        essentia_data = {"predictions": predictions}
        tr_tags = build_flac_tags(librosa_data, essentia_data, tensor_feats, prefix=prefix)
        all_expected_tags.update(tr_tags)

    return all_expected_tags

def inspect_and_repair_file_group(filepath: str, file_rows: list[tuple], dry_run: bool = False, force: bool = False, retry_count: int = 5, retry_delay: float = 3.0) -> bool:
    logger = logging.getLogger("RepairZig")

    if not os.path.exists(filepath):
        logger.warning(f"ファイルが見つかりません (スキップ): {filepath}")
        return False

    try:
        audio = FLAC(filepath)
        existing_tags = {k.upper(): v for k, v in audio.items()}
    except Exception as e:
        logger.error(f"FLAC タグの読込に失敗いたしました: {filepath} -> {e}")
        return False

    expected_tags = build_tags_for_file_group(file_rows)
    if not expected_tags:
        logger.info(f"書き込むべき特徴量データが DB 内にございません: {os.path.basename(filepath)}")
        return False

    missing_tags: dict[str, str] = {}
    for k, v in expected_tags.items():
        k_upper = k.upper()
        if force or k_upper not in existing_tags or not existing_tags[k_upper]:
            missing_tags[k] = v

    tr_count_info = f"({len(file_rows)} トラック)" if len(file_rows) > 1 else "(単体 FLAC)"

    if not missing_tags:
        logger.info(f"すべてのタグが既に完璧に焼き込まれておりますわ！ {tr_count_info}: {os.path.basename(filepath)}")
        return True

    logger.info(f"不足タグを {len(missing_tags)} 件検出いたしましたわ！ {tr_count_info}: {os.path.basename(filepath)}")
    if dry_run:
        print(f"\n--- Dry-Run: Inspected {os.path.basename(filepath)} {tr_count_info} ---")
        for mk, mv in sorted(missing_tags.items())[:10]:
            print(f"  + Missing Tag: {mk} = {mv}")
        if len(missing_tags) > 10:
            print(f"  ... and {len(missing_tags) - 10} more missing tags.")
        return True

    try:
        write_flac_tags_with_retry(filepath, missing_tags, retry_count=retry_count, retry_delay=retry_delay)
        logger.info(f"不足タグの再焼き込みが正常完了いたしましたわ！ {tr_count_info}: {os.path.basename(filepath)}")
        return True
    except Exception as e:
        logger.error(f"タグ焼き込み中にエラーが発生いたしました: {filepath} -> {e}")
        return False

def main():
    setup_logger()
    logger = logging.getLogger("RepairZig")

    parser = argparse.ArgumentParser(description="FLAC DB Tag Repair Tool (Grouped by Filepath)")
    parser.add_argument("--dry-run", action="store_true", help="Preview missing tags without modifying FLAC files")
    parser.add_argument("--limit", type=int, default=0, help="Limit maximum number of UNIQUE FILES to process (0 = unlimited)")
    parser.add_argument("--dir", type=str, default="", help="Filter files under specific directory path")
    parser.add_argument("--force", action="store_true", help="Force overwrite all tags even if present")
    args = parser.parse_args()

    config = find_config()
    db_url = get_db_url(config)
    retry_count = int(config.get("python_env", {}).get("file_retry_count", 5))
    retry_delay = float(config.get("python_env", {}).get("file_retry_delay_sec", 3))

    conn = psycopg2.connect(db_url)
    cur = conn.cursor()

    query = "SELECT id, filepath, audio_hash, meta, features FROM raw.library_flac WHERE features IS NOT NULL ORDER BY id ASC"
    cur.execute(query)
    rows = cur.fetchall()
    
    # Python 側で filepath を正規化してグループ化 ＆ --dir フィルタリング
    target_dir_norm = os.path.normpath(args.dir).lower() if args.dir else ""

    grouped_files: dict[str, list[tuple]] = defaultdict(list)
    for row in rows:
        fp = row[1]
        if target_dir_norm:
            fp_norm = os.path.normpath(fp).lower()
            if not fp_norm.startswith(target_dir_norm):
                continue
        grouped_files[fp].append(row)

    unique_filepaths = list(grouped_files.keys())
    logger.info(f"DB から条件に一致する {sum(len(v) for v in grouped_files.values())} レコード / {len(unique_filepaths)} 個のユニーク FLAC ファイルを取得いたしましたわ！")

    if args.limit > 0:
        unique_filepaths = unique_filepaths[:args.limit]

    processed = 0
    repaired = 0
    for fp in unique_filepaths:
        file_rows = grouped_files[fp]
        processed += 1
        success = inspect_and_repair_file_group(fp, file_rows, dry_run=args.dry_run, force=args.force, retry_count=retry_count, retry_delay=retry_delay)
        if success:
            repaired += 1

    cur.close()
    conn.close()

    logger.info(f"【処理完了】 総ユニークファイル: {processed} 件 / 正常・補完完了: {repaired} 件")

if __name__ == "__main__":
    main()
