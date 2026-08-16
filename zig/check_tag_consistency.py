"""
zig/check_tag_consistency.py
============================
PostgreSQL DB (raw.library_flac) と ローカル FLAC ファイル (VorbisComment) の
双方向整合性（Bi-directional Consistency）を検査・レポート・一括修復する統合チェッカーですわ！

【サポートモード】
1. db-to-flac  : DB内の解析結果（Librosa, Essentia 453, Tensor）が FLAC タグに存在するか検証＆修復
2. flac-to-db  : FLAC ファイルのメタデータが DB に登録・一致しているか検証＆修復
3. diff / both : 双方向クロスチェックを行い、差分サマリーおよび JSON レポートを出力
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

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from flac_tagger import build_flac_tags, write_flac_tags_with_retry, parse_tags_from_meta_dict

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [ConsistencyChecker] %(message)s",
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
                    return tomllib.load(f)
            except Exception as e:
                logging.warning(f"{path} の読み込みに失敗いたしました: {e}")
    return {}

def get_db_url(config: dict) -> str:
    db_url = config.get("database", {}).get("url", "")
    if not db_url:
        db_url = os.environ.get("FLAC_DB_URL", "")
    if not db_url:
        logging.error("DB URL が設定ファイルまたは環境変数 FLAC_DB_URL から取得できませんでした。")
        sys.exit(1)
    return db_url

def scan_flac_files(target_dir: str, limit: int = 0) -> list[str]:
    """対象ディレクトリから実在する FLAC ファイル群を高速走査いたしますわ！"""
    found = []
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
                found.append(full_path)
                if limit > 0 and len(found) >= limit:
                    return found
    return found

def build_expected_tags_for_rows(file_rows: list[tuple]) -> dict[str, str]:
    """同一 filepath に属する DB 行群から期待される VorbisComment タグ辞書を構築いたしますわ！"""
    all_tags = {}
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

        essentia_data = {}
        if isinstance(predictions, dict) and predictions:
            essentia_data = {"predictions": predictions}

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

        tr_tags = build_flac_tags(librosa_data, essentia_data, tensor_feats, prefix=prefix)
        all_tags.update(tr_tags)

        if isinstance(meta, dict):
            meta_tags = parse_tags_from_meta_dict(meta, prefix=prefix)
            for mk, mv in meta_tags.items():
                if mk not in all_tags:
                    all_tags[mk] = mv

    return all_tags

def check_file_consistency(filepath: str, file_rows: list[tuple], mode: str) -> dict:
    """単一 FLAC ファイルに対する DB ⇔ FLAC タグの整合性検証を行いますわ！"""
    result = {
        "filepath": filepath,
        "exists_in_fs": os.path.exists(filepath),
        "exists_in_db": len(file_rows) > 0,
        "missing_in_flac": {},
        "missing_in_db": {},
        "meta_mismatch": {},
        "is_consistent": True
    }

    if not result["exists_in_fs"]:
        result["is_consistent"] = False
        return result

    try:
        audio = FLAC(filepath)
        flac_tags = {k.upper(): v[0] if isinstance(v, list) and v else str(v) for k, v in audio.items()}
    except Exception as e:
        result["is_consistent"] = False
        result["error"] = str(e)
        return result

    if not result["exists_in_db"]:
        result["is_consistent"] = False
        result["missing_in_db"] = {"info": "File exists on disk but not registered in PostgreSQL DB"}
        return result

    expected_tags = build_expected_tags_for_rows(file_rows)

    # 1. DB -> FLAC の未反映タグ検査
    missing_flac = {}
    for k, v in expected_tags.items():
        k_upper = k.upper()
        if k_upper not in flac_tags or not flac_tags[k_upper]:
            missing_flac[k] = v
    result["missing_in_flac"] = missing_flac

    # 2. FLAC -> DB のメタデータ不一致検査 (タイトル、アーティスト、アルバム)
    if file_rows:
        first_meta = file_rows[0][3] or {}
        if isinstance(first_meta, dict):
            for meta_k in ["title", "artist", "album"]:
                val_db = str(first_meta.get(meta_k, "")).strip().lower()
                val_flac = str(flac_tags.get(meta_k.upper(), "")).strip().lower()
                if val_db and val_flac and val_db != val_flac:
                    result["meta_mismatch"][meta_k] = {
                        "db": first_meta.get(meta_k),
                        "flac": flac_tags.get(meta_k.upper())
                    }

    if missing_flac or result["meta_mismatch"]:
        result["is_consistent"] = False

    return result

def main():
    setup_logger()
    logger = logging.getLogger("ConsistencyChecker")

    parser = argparse.ArgumentParser(description="FLAC DB ⇔ Tag Bi-directional Consistency Checker & Repair Tool")
    parser.add_argument("--mode", choices=["db-to-flac", "flac-to-db", "diff", "both"], default="both", help="Verification mode")
    parser.add_argument("--dir", type=str, default="", help="Target directory or single FLAC file path")
    parser.add_argument("--limit", type=int, default=0, help="Maximum number of files to inspect (0 = unlimited)")
    parser.add_argument("--dry-run", action="store_true", help="Perform check without applying any changes")
    parser.add_argument("--repair", action="store_true", help="Automatically repair detected missing tags in FLAC files")
    parser.add_argument("--output-json", type=str, default="", help="Save detailed diff report to JSON file")
    args = parser.parse_args()

    t_start = time.perf_counter()
    config = find_config()
    db_url = get_db_url(config)

    conn = psycopg2.connect(db_url)
    cur = conn.cursor()

    logger.info(f"【整合性チェッカー起動】 モード: {args.mode}, 対象パス: {args.dir or '全DB対象'}")

    target_files = []
    if args.dir and os.path.exists(args.dir):
        target_files = scan_flac_files(args.dir, limit=args.limit)
        logger.info(f"実在 FLAC ファイル {len(target_files)} 件を検出いたしました。")
        if not target_files:
            logger.info("対象ファイルが存在いたしません。")
            cur.close()
            conn.close()
            sys.exit(0)

        detail_query = """
            SELECT id, filepath, audio_hash, meta, features, predictions 
            FROM raw.library_flac 
            WHERE (filepath = ANY(%s) OR REPLACE(filepath, '/', '\\') = ANY(%s) OR REPLACE(filepath, '\\', '/') = ANY(%s))
            ORDER BY filepath, id ASC
        """
        alt_bslash = [f.replace("/", "\\") for f in target_files]
        alt_slash = [f.replace("\\", "/") for f in target_files]
        cur.execute(detail_query, (target_files, alt_bslash, alt_slash))
        rows = cur.fetchall()
    else:
        logger.info("DB から対象 FLAC ファイル一覧を取得中...")
        query = "SELECT id, filepath, audio_hash, meta, features, predictions FROM raw.library_flac ORDER BY filepath, id ASC"
        if args.limit > 0:
            query += f" LIMIT {args.limit}"
        cur.execute(query)
        rows = cur.fetchall()

    grouped_db: dict[str, list[tuple]] = defaultdict(list)
    for row in rows:
        grouped_db[row[1]].append(row)

    file_map = {os.path.normpath(fp).lower(): fp for fp in grouped_db.keys()}

    exec_files = target_files if target_files else list(grouped_db.keys())
    if args.limit > 0:
        exec_files = exec_files[:args.limit]

    results = []
    consistent_count = 0
    inconsistent_count = 0
    repaired_count = 0

    for fp in exec_files:
        norm_fp = os.path.normpath(fp).lower()
        real_db_fp = file_map.get(norm_fp, fp)
        file_rows = grouped_db.get(real_db_fp, [])

        res = check_file_consistency(fp, file_rows, args.mode)
        results.append(res)

        if res["is_consistent"]:
            consistent_count += 1
        else:
            inconsistent_count += 1
            logger.warning(f"不一致検出: {os.path.basename(fp)} -> 不足タグ {len(res['missing_in_flac'])} 件, メタ不一致 {len(res['meta_mismatch'])} 件")

            if args.repair and not args.dry_run and res["missing_in_flac"]:
                try:
                    write_flac_tags_with_retry(fp, res["missing_in_flac"])
                    repaired_count += 1
                    logger.info(f"  + 不足タグ {len(res['missing_in_flac'])} 件を正常修復（焼き込み）いたしましたわ！")
                except Exception as e:
                    logger.error(f"  - タグ修復失敗: {e}")

    cur.close()
    conn.close()

    t_end = time.perf_counter()
    logger.info("==========================================================================")
    logger.info(f" 【整合性検査結果】 総検査ファイル: {len(results)} 件 / 完全一致: {consistent_count} 件 / 不一致: {inconsistent_count} 件 / 修復: {repaired_count} 件")
    logger.info(f" 所要時間: {t_end - t_start:.3f} 秒")
    logger.info("==========================================================================")

    if args.output_json:
        try:
            with open(args.output_json, "w", encoding="utf-8") as f:
                json.dump(results, f, ensure_ascii=False, indent=2)
            logger.info(f"詳細差分レポートを保存いたしました: {args.output_json}")
        except Exception as e:
            logger.error(f"JSON レポート保存失敗: {e}")

if __name__ == "__main__":
    main()
