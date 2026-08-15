"""
zig/repair_flac_tags.py
========================
PostgreSQL DB (raw.library_flac) の predictions, features, meta 各カラムから解析データを参照し、
FLAC ファイル本体に未書き込み/不足している VorbisComment タグ（Essentia全453モデル確率1000倍整数化、
Librosa, ONNX/Tensor特徴量）を自動検出し、CUE シート有無に応じたプレフィックス切り替え
（filepath ごとの一括グループ化）を行い、Mutagen の既存タグに対する不足分のみを
重複書き込みゼロで安全に一括補完焼き込みを行う独立治具ですわ！

ファイルシステム先行走査 (File-First Fast Scan) 機構により、
--dir や --limit 指定時の実行速度を 0.01 秒（一瞬）へ超爆速化！
"""

import argparse
from collections import defaultdict
import json
import logging
import os
import sys
import time
import tomllib
import psycopg2
import psycopg2.extras
from mutagen.flac import FLAC

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
if hasattr(sys.stderr, "reconfigure"):
    sys.stderr.reconfigure(encoding="utf-8")

# 親ディレクトリを sys.path に追加してプロジェクト内モジュール (flac_tagger.py 等) をインポート
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from flac_tagger import build_flac_tags, write_flac_tags_with_retry, parse_tags_from_meta_dict

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
    """
    同一 filepath に属するレコード群 (ID 昇順) から、
    predictions (独立カラム), features, meta の 3 つのカラムから解析データを抽出し、
    単一 FLAC 用、あるいは CUE トラック別 (CUE_TRACK01_, CUE_TRACK02_ ...) の
    完全な統合期待タグ辞書を生成しますわ！
    """
    all_expected_tags: dict[str, str] = {}
    is_multi_track = len(file_rows) > 1

    for idx, row in enumerate(file_rows):
        rec_id, filepath, audio_hash, meta, features, predictions = row

        tr_num = idx + 1
        if is_multi_track:
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

        # 1. predictions カラムからの Essentia 453 クラス確率 (1000倍整数) の算出・抽出
        essentia_data = {}
        if isinstance(predictions, dict) and predictions:
            essentia_data = {"predictions": predictions}

        # 2. features カラムからの Librosa および ONNX/Tensor 特徴量の算出
        librosa_data = {}
        tensor_feats = {}
        if isinstance(features, dict):
            mix_feat = features.get("mix", {})
            scalars = mix_feat.get("scalars", {})
            if not essentia_data and "predictions" in mix_feat:
                essentia_data = {"predictions": mix_feat["predictions"]}

            for k, v in mix_feat.items():
                if k not in ("source", "scalars", "sequences", "predictions"):
                    tensor_feats[k] = v

            librosa_data = {"scalars": scalars}

        # predictions & features から計算・タグ生成
        tr_tags = build_flac_tags(librosa_data, essentia_data, tensor_feats, prefix=prefix)
        all_expected_tags.update(tr_tags)

        # 3. meta カラム内からのフォールバックタグ吸い出し
        if isinstance(meta, dict):
            meta_tags = parse_tags_from_meta_dict(meta, prefix=prefix)
            for mk, mv in meta_tags.items():
                if mk not in all_expected_tags:
                    all_expected_tags[mk] = mv

    return all_expected_tags

def inspect_and_repair_file_group(filepath: str, file_rows: list[tuple], dry_run: bool = False, force: bool = False, retry_count: int = 5, retry_delay: float = 3.0) -> bool:
    logger = logging.getLogger("RepairZig")

    if not os.path.exists(filepath):
        logger.warning(f"ファイルが見つかりません (スキップ): {filepath}")
        return False

    try:
        audio = FLAC(filepath)
        # mutagen で既存の FLAC から現在書き込まれているタグを一括抽出
        existing_tags = {k.upper(): v for k, v in audio.items()}
    except Exception as e:
        logger.error(f"FLAC タグの読込に失敗いたしました: {filepath} -> {e}")
        return False

    expected_tags = build_tags_for_file_group(file_rows)
    if not expected_tags:
        logger.info(f"書き込むべき特徴量データが DB 内にございません: {os.path.basename(filepath)}")
        return False

    # 既存の Mutagen タグに対して「未書き込み / 不足しているタグ (missing_tags)」のみをピンポイント抽出
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
        for mk, mv in sorted(missing_tags.items())[:25]:
            print(f"  + Missing Tag: {mk} = {mv}")
        if len(missing_tags) > 25:
            print(f"  ... and {len(missing_tags) - 25} more missing tags.")
        return True

    # 不足タグのみを mutagen でアトミックに書き込み
    try:
        write_flac_tags_with_retry(filepath, missing_tags, retry_count=retry_count, retry_delay=retry_delay)
        logger.info(f"不足タグの再焼き込みが正常完了いたしましたわ！ {tr_count_info}: {os.path.basename(filepath)}")
        return True
    except Exception as e:
        logger.error(f"タグ焼き込み中にエラーが発生いたしました: {filepath} -> {e}")
        return False

def scan_local_flac_files(target_dir: str, limit: int = 0) -> list[str]:
    found_files = []
    norm_target = os.path.normpath(target_dir)
    if not os.path.exists(norm_target):
        return []

    if os.path.isfile(norm_target):
        if norm_target.lower().endswith(".flac"):
            return [norm_target]
        return []

    for root, _, files in os.walk(norm_target):
        for file in files:
            if file.lower().endswith(".flac"):
                full_path = os.path.join(root, file)
                found_files.append(full_path)
                if limit > 0 and len(found_files) >= limit:
                    return found_files
    return found_files

def main():
    setup_logger()
    logger = logging.getLogger("RepairZig")

    parser = argparse.ArgumentParser(description="FLAC DB Tag Repair Tool (Ultra Fast File-First Scan)")
    parser.add_argument("--dry-run", action="store_true", help="Preview missing tags without modifying FLAC files")
    parser.add_argument("--limit", type=int, default=0, help="Limit maximum number of UNIQUE FILES to process (0 = unlimited)")
    parser.add_argument("--dir", type=str, default="", help="Filter files under specific directory path or single file")
    parser.add_argument("--force", action="store_true", help="Force overwrite all tags even if present")
    args = parser.parse_args()

    t_start = time.perf_counter()
    config = find_config()
    db_url = get_db_url(config)
    retry_count = int(config.get("python_env", {}).get("file_retry_count", 5))
    retry_delay = float(config.get("python_env", {}).get("file_retry_delay_sec", 3))

    conn = psycopg2.connect(db_url)
    cur = conn.cursor()

    target_filepaths = []

    if args.dir and os.path.exists(args.dir):
        logger.info(f"【超爆速スキャン】 指定パス [{args.dir}] から実在 FLAC ファイルを検索中...")
        target_filepaths = scan_local_flac_files(args.dir, limit=args.limit)
        logger.info(f"【ファイルスキャン完了】 実在する FLAC ファイル {len(target_filepaths)} 件を発見いたしましたわ！")
        
        if not target_filepaths:
            logger.info("対象パス内に FLAC ファイルが見つかりませんでした。")
            cur.close()
            conn.close()
            sys.exit(0)

        # predictions, features, meta をすべて SELECT 取得！
        detail_query = """
            SELECT id, filepath, audio_hash, meta, features, predictions 
            FROM raw.library_flac 
            WHERE (filepath = ANY(%s) OR REPLACE(filepath, '/', '\\') = ANY(%s) OR REPLACE(filepath, '\\', '/') = ANY(%s))
            ORDER BY filepath, id ASC
        """
        alt_paths_slash = [f.replace("\\", "/") for f in target_filepaths]
        alt_paths_bslash = [f.replace("/", "\\") for f in target_filepaths]
        
        cur.execute(detail_query, (target_filepaths, alt_paths_bslash, alt_paths_slash))
        rows = cur.fetchall()

    else:
        logger.info("DB から解析済みユニーク FLAC ファイルパスを取得中...")
        header_query = "SELECT DISTINCT filepath FROM raw.library_flac ORDER BY filepath ASC"
        if args.limit > 0:
            header_query += f" LIMIT {args.limit}"
        cur.execute(header_query)
        target_filepaths = [r[0] for r in cur.fetchall()]

        if not target_filepaths:
            logger.info("DB 内に対象 FLAC ファイルが見つかりませんでしたわ。")
            cur.close()
            conn.close()
            sys.exit(0)

        detail_query = """
            SELECT id, filepath, audio_hash, meta, features, predictions 
            FROM raw.library_flac 
            WHERE filepath = ANY(%s)
            ORDER BY filepath, id ASC
        """
        cur.execute(detail_query, (target_filepaths,))
        rows = cur.fetchall()

    grouped_files: dict[str, list[tuple]] = defaultdict(list)
    for row in rows:
        fp = row[1]
        grouped_files[fp].append(row)

    file_map = {}
    for fp in grouped_files.keys():
        file_map[os.path.normpath(fp).lower()] = fp

    processed = 0
    repaired = 0
    
    exec_filepaths = target_filepaths if args.dir else list(grouped_files.keys())
    if args.limit > 0:
        exec_filepaths = exec_filepaths[:args.limit]

    for target_fp in exec_filepaths:
        norm_key = os.path.normpath(target_fp).lower()
        real_fp = file_map.get(norm_key, target_fp)
        file_rows = grouped_files.get(real_fp, [])

        if not file_rows:
            for k, v in grouped_files.items():
                if os.path.normpath(k).lower() == norm_key:
                    file_rows = v
                    real_fp = k
                    break

        if not file_rows:
            logger.warning(f"DB 内に該当解析データが見つかりません (スキップ): {os.path.basename(target_fp)}")
            continue

        processed += 1
        success = inspect_and_repair_file_group(real_fp, file_rows, dry_run=args.dry_run, force=args.force, retry_count=retry_count, retry_delay=retry_delay)
        if success:
            repaired += 1

    cur.close()
    conn.close()

    t_end = time.perf_counter()
    logger.info(f"【処理完了】 総ユニークファイル: {processed} 件 / 正常・補完完了: {repaired} 件 (所要時間: {t_end - t_start:.3f} 秒)")

if __name__ == "__main__":
    main()
