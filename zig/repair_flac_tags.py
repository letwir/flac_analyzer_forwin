"""
zig/repair_flac_tags.py
========================
PostgreSQL DB (raw.library_flac) から既存の解析データを参照し、
FLAC ファイル本体に未書き込み/不足している VorbisComment タグを検出して
CUE シート有無に応じたプレフィックス切り替えを行いつつ自動補完焼き込みを行う独立治具ですわ！
"""

import argparse
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

def parse_cue_track_count(meta: dict) -> int:
    """meta データ内から CUE トラック数・CUE の有無を判定・抽出します。"""
    if not isinstance(meta, dict):
        return 0
    cuesheet = meta.get("cuesheet") or meta.get("CUESHEET") or ""
    if cuesheet:
        # TRACK 01, TRACK 02 などの出現回数をカウント
        import re
        matches = re.findall(r"TRACK\s+(\d+)\s+AUDIO", cuesheet, re.IGNORECASE)
        if matches:
            return len(matches)
    
    # 既存タグキーから cue_trackXX_ を探す
    max_track = 0
    import re
    for k in meta.keys():
        m = re.match(r"^cue_track(\d+)_", k.lower())
        if m:
            tr_num = int(m.group(1))
            if tr_num > max_track:
                max_track = tr_num
    return max_track

def build_tags_for_record(meta: dict, features: dict) -> dict[str, str]:
    """DB の meta および features データから、FLAC に書き込むべき期待タグ辞書を統合算出しますわ。"""
    expected_tags: dict[str, str] = {}
    if not isinstance(features, dict):
        return expected_tags

    mix_feat = features.get("mix", {})
    scalars = mix_feat.get("scalars", {})
    predictions = mix_feat.get("predictions", {})

    # ONNX/Tensor 特徴量
    tensor_feats = {}
    for k, v in mix_feat.items():
        if k not in ("source", "scalars", "sequences", "predictions"):
            tensor_feats[k] = v

    cue_track_count = parse_cue_track_count(meta)

    if cue_track_count > 0:
        # CUE ありの場合: トラックごとの CUE_TRACKXX_ タグ、または全体タグを生成
        for tr_idx in range(1, cue_track_count + 1):
            prefix = f"CUE_TRACK{tr_idx:02d}"
            
            # CUE トラックごとの個別 scalars / predictions が存在するかチェック
            tr_librosa = {"scalars": scalars}
            tr_essentia = {"predictions": predictions}
            
            # meta 内の cue_track01_* 個別情報を吸い出す
            track_prefix = f"cue_track{tr_idx:02d}_"
            for mk, mv in meta.items():
                if mk.lower().startswith(track_prefix):
                    raw_sub_key = mk[len(track_prefix):]
                    if raw_sub_key.lower() == "bpm":
                        try:
                            tr_librosa["scalars"]["bpm"] = float(mv)
                        except (ValueError, TypeError):
                            pass

            tr_tags = build_flac_tags(tr_librosa, tr_essentia, tensor_feats, prefix=prefix)
            expected_tags.update(tr_tags)
    else:
        # CUE なし（単体 FLAC）の場合: プレフィックスなし
        librosa_data = {"scalars": scalars}
        essentia_data = {"predictions": predictions}
        expected_tags = build_flac_tags(librosa_data, essentia_data, tensor_feats, prefix="")

    return expected_tags

def inspect_and_repair_record(row: tuple, dry_run: bool = False, force: bool = False, retry_count: int = 5, retry_delay: float = 3.0) -> bool:
    rec_id, filepath, audio_hash, meta, features = row
    logger = logging.getLogger("RepairZig")

    if not os.path.exists(filepath):
        logger.warning(f"[ID: {rec_id}] ファイルが見つかりません (スキップ): {filepath}")
        return False

    try:
        audio = FLAC(filepath)
        existing_tags = {k.upper(): v for k, v in audio.items()}
    except Exception as e:
        logger.error(f"[ID: {rec_id}] FLAC タグの読込に失敗いたしました: {filepath} -> {e}")
        return False

    expected_tags = build_tags_for_record(meta, features)
    if not expected_tags:
        logger.info(f"[ID: {rec_id}] 書き込むべき特徴量データが DB 内にございません。")
        return False

    missing_tags: dict[str, str] = {}
    for k, v in expected_tags.items():
        k_upper = k.upper()
        if force or k_upper not in existing_tags or not existing_tags[k_upper]:
            missing_tags[k] = v

    if not missing_tags:
        logger.info(f"[ID: {rec_id}] すべてのタグが既に完璧に焼き込まれておりますわ！: {os.path.basename(filepath)}")
        return True

    logger.info(f"[ID: {rec_id}] 不足タグを {len(missing_tags)} 件検出いたしましたわ！ (ファイル: {os.path.basename(filepath)})")
    if dry_run:
        print(f"\n--- Dry-Run: Inspected {os.path.basename(filepath)} ---")
        for mk, mv in sorted(missing_tags.items())[:10]:
            print(f"  + Missing Tag: {mk} = {mv}")
        if len(missing_tags) > 10:
            print(f"  ... and {len(missing_tags) - 10} more missing tags.")
        return True

    # 実際の焼き込み実行
    try:
        write_flac_tags_with_retry(filepath, missing_tags, retry_count=retry_count, retry_delay=retry_delay)
        logger.info(f"[ID: {rec_id}] 不足タグの再焼き込みが正常完了いたしましたわ！")
        return True
    except Exception as e:
        logger.error(f"[ID: {rec_id}] タグ焼き込み中にエラーが発生いたしました: {e}")
        return False

def main():
    setup_logger()
    logger = logging.getLogger("RepairZig")

    parser = argparse.ArgumentParser(description="FLAC DB Tag Repair Tool")
    parser.add_argument("--dry-run", action="store_true", help="Preview missing tags without modifying FLAC files")
    parser.add_argument("--limit", type=int, default=0, help="Limit maximum number of records to process (0 = unlimited)")
    parser.add_argument("--dir", type=str, default="", help="Filter files under specific directory path")
    parser.add_argument("--force", action="store_true", help="Force overwrite all tags even if present")
    args = parser.parse_args()

    config = find_config()
    db_url = get_db_url(config)
    retry_count = int(config.get("python_env", {}).get("file_retry_count", 5))
    retry_delay = float(config.get("python_env", {}).get("file_retry_delay_sec", 3))

    conn = psycopg2.connect(db_url)
    cur = conn.cursor()

    query = "SELECT id, filepath, audio_hash, meta, features FROM raw.library_flac WHERE features IS NOT NULL"
    params = []
    if args.dir:
        query += " AND filepath LIKE %s"
        params.append(f"{args.dir}%")
    query += " ORDER BY id DESC"
    if args.limit > 0:
        query += " LIMIT %s"
        params.append(args.limit)

    cur.execute(query, params)
    rows = cur.fetchall()
    logger.info(f"DB から処理対象レコードを {len(rows)} 件取得いたしましたわ！")

    processed = 0
    repaired = 0
    for row in rows:
        processed += 1
        success = inspect_and_repair_record(row, dry_run=args.dry_run, force=args.force, retry_count=retry_count, retry_delay=retry_delay)
        if success:
            repaired += 1

    cur.close()
    conn.close()

    logger.info(f"【処理完了】 総処理対象: {processed} 件 / 正常・補完完了: {repaired} 件")

if __name__ == "__main__":
    main()
