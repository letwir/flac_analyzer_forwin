"""
migrate_hnr.py
==============
稼働中・計測中の環境に対応した HNR (Harmonic-to-Noise Ratio) 変換・マイグレーション治具スクリプトですわ！

機能:
1. PostgreSQL (raw.library_flac) 内の features JSONB に含まれる旧 NAP 値 (0.0〜1.0) を検知し、
   "nap", "hnr_db", "hnr" (dB値) へ安全に一括マイグレーション。
2. --fix-tags 指定時、実 FLAC ファイルの VorbisComment タグ (LIBROSA_NAP, LIBROSA_HNR_DB, LIBROSA_HNR) を
   Windows タイムスタンプ保護付きで同期書き戻し。
3. 単体値の双方向変換 CLI (--calc-db, --calc-nap)。

使い方:
    # 単体値の確認
    python migrate_hnr.py --calc-db 0.85
    python migrate_hnr.py --calc-nap 7.54

    # DB マイグレーションのドライラン (確認のみ)
    python migrate_hnr.py --dry-run

    # 実際の DB 更新実行 (バッチコミット)
    python migrate_hnr.py --batch-size 500

    # DB 更新 ＋ FLAC タグ同期書き戻し
    python migrate_hnr.py --fix-tags --batch-size 500
"""

import argparse
import ctypes
from ctypes import wintypes
import json
import logging
import math
import os
import sys
import tomllib
from typing import Any

import psycopg2
import psycopg2.extras

# ─────────────────────────────────────────────
# ロギング設定 (緑色ログ)
# ─────────────────────────────────────────────
GREEN = "\033[32m"
YELLOW = "\033[33m"
CYAN = "\033[36m"
RESET = "\033[0m"


def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format=f"{GREEN}[%(asctime)s] [%(levelname)s] [HnrMigrator] %(message)s{RESET}",
        handlers=[logging.StreamHandler(sys.stderr)],
    )


# ─────────────────────────────────────────────
# 数学変換関数 (完全可逆)
# ─────────────────────────────────────────────
def calc_hnr_db(nap: float, min_nap: float = 1e-4, max_nap: float = 1.0 - 1e-4) -> float:
    """NAP (0.0〜1.0) を Harmonic-to-Noise Ratio (dB, -40.0〜+40.0 dB) へ Logit変換しますわ！"""
    if nap <= 0.0:
        return -40.0
    clamped = max(min_nap, min(max_nap, float(nap)))
    return float(10.0 * math.log10(clamped / (1.0 - clamped)))


def calc_nap_from_hnr_db(hnr_db: float) -> float:
    """HNR (dB) を NAP (0.0〜1.0) へ逆変換（ロジスティック・シグモイド）しますわ！"""
    try:
        val = float(hnr_db) / 10.0
        if val > 30.0:
            return 1.0
        if val < -30.0:
            return 0.0
        exp_val = 10.0**val
        return float(exp_val / (1.0 + exp_val))
    except (OverflowError, ValueError):
        return 0.0


# ─────────────────────────────────────────────
# Windows タイムスタンプ保護
# ─────────────────────────────────────────────
def set_win_timestamps(
    filepath: str, atime: float, mtime: float, ctime: float | None = None
):
    try:
        os.utime(filepath, (atime, mtime))
    except Exception as e:
        logging.warning(f"utime 復元中に失敗いたしましたわ: {e}")

    if sys.platform == "win32" and ctime is not None:
        try:
            FILE_WRITE_ATTRIBUTES = 0x0100
            FILE_SHARE_READ = 0x00000001
            FILE_SHARE_WRITE = 0x00000002
            FILE_SHARE_DELETE = 0x00000004
            OPEN_EXISTING = 3
            FILE_FLAG_BACKUP_SEMANTICS = 0x02000000

            handle = ctypes.windll.kernel32.CreateFileW(
                filepath,
                FILE_WRITE_ATTRIBUTES,
                FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                None,
                OPEN_EXISTING,
                FILE_FLAG_BACKUP_SEMANTICS,
                None,
            )
            if handle != -1 and handle != 0:
                c_time_hnsec = int((ctime + 11644473600) * 10000000)
                ft_low = c_time_hnsec & 0xFFFFFFFF
                ft_high = (c_time_hnsec >> 32) & 0xFFFFFFFF

                class FILETIME(ctypes.Structure):
                    _fields_ = [
                        ("dwLowDateTime", wintypes.DWORD),
                        ("dwHighDateTime", wintypes.DWORD),
                    ]

                ft_create = FILETIME(ft_low, ft_high)
                ctypes.windll.kernel32.SetFileTime(
                    handle, ctypes.byref(ft_create), None, None
                )
                ctypes.windll.kernel32.CloseHandle(handle)
        except Exception as e:
            logging.warning(
                f"Windows ctime (CreationTime) 復元中に失敗いたしましたわ: {e}"
            )


# ─────────────────────────────────────────────
# DB 接続ヘルパー
# ─────────────────────────────────────────────
def get_db_url() -> str | None:
    config_path = os.path.join(os.path.dirname(__file__), "config.toml")
    db_url = None
    if os.path.exists(config_path):
        try:
            with open(config_path, "rb") as f:
                config = tomllib.load(f)
            db_url = config.get("database", {}).get("url", "")
        except Exception as e:
            logging.warning(f"config.toml からの DB URL 読込に失敗いたしましたわ: {e}")

    if not db_url:
        db_url = os.environ.get("FLAC_DB_URL") or os.environ.get("DATABASE_URL")
    return db_url


# ─────────────────────────────────────────────
# FLAC タグ更新処理
# ─────────────────────────────────────────────
def update_flac_hnr_tags(filepath: str, nap: float, hnr_db: float) -> bool:
    try:
        from mutagen.flac import FLAC
    except ImportError:
        logging.warning("mutagen が見つからないため FLAC タグの更新をスキップいたしますわ")
        return False

    if not os.path.exists(filepath):
        return False

    try:
        stat_info = os.stat(filepath)
        atime = stat_info.st_atime
        mtime = stat_info.st_mtime
        ctime = getattr(stat_info, "st_birthtime", stat_info.st_ctime)

        audio = FLAC(filepath)
        audio["LIBROSA_NAP"] = f"{nap:.6f}"
        audio["LIBROSA_HNR_DB"] = f"{hnr_db:.4f}"
        audio["LIBROSA_HNR"] = f"{hnr_db:.4f}"
        audio.save()

        set_win_timestamps(filepath, atime, mtime, ctime)
        return True
    except Exception as e:
        logging.warning(f"FLAC タグ更新に失敗いたしましたわ ({filepath}): {e}")
        return False


# ─────────────────────────────────────────────
# メイン処理
# ─────────────────────────────────────────────
def migrate_stem_scalars(scalars: dict[str, Any]) -> bool:
    """単一ステムの scalars 辞書をマイグレーションします。変更があった場合は True を返します。"""
    modified = False
    old_hnr = scalars.get("hnr")
    has_nap = "nap" in scalars
    has_hnr_db = "hnr_db" in scalars

    if old_hnr is not None:
        try:
            val = float(old_hnr)
            # 0.0 <= val <= 1.0 かつ (nap 未設定 または hnr_db 未設定) の場合は旧 NAP 値と判定
            if 0.0 <= val <= 1.0 and (not has_nap or not has_hnr_db):
                nap_val = val
                hnr_db_val = calc_hnr_db(nap_val)
                scalars["nap"] = nap_val
                scalars["hnr_db"] = hnr_db_val
                scalars["hnr"] = hnr_db_val
                modified = True
            elif not has_nap and (val < 0.0 or val > 1.0):
                # 既に dB 値が入っている場合、NAP を逆算補完
                hnr_db_val = val
                nap_val = calc_nap_from_hnr_db(hnr_db_val)
                scalars["nap"] = nap_val
                scalars["hnr_db"] = hnr_db_val
                modified = True
        except (ValueError, TypeError):
            pass

    return modified


def migrate_record_features(features: dict[str, Any]) -> bool:
    """features JSONB 全体を再帰走査してマイグレーションします。"""
    if not isinstance(features, dict):
        return False

    modified = False

    # 1. mix ステム
    if "mix" in features and isinstance(features["mix"], dict):
        scalars = features["mix"].get("scalars")
        if isinstance(scalars, dict):
            if migrate_stem_scalars(scalars):
                modified = True

    # 2. demucs ステム群
    if "demucs" in features and isinstance(features["demucs"], dict):
        for stem_name, stem_data in features["demucs"].items():
            if isinstance(stem_data, dict):
                scalars = stem_data.get("scalars")
                if isinstance(scalars, dict):
                    if migrate_stem_scalars(scalars):
                        modified = True

    # 3. 直下にステムがある構造 (フラット構造互換)
    for stem_name in ("vocals", "drums", "bass", "other", "guitar", "piano"):
        if stem_name in features and isinstance(features[stem_name], dict):
            scalars = features[stem_name].get("scalars")
            if isinstance(scalars, dict):
                if migrate_stem_scalars(scalars):
                    modified = True

    return modified


def main():
    setup_logger()
    logger = logging.getLogger("HnrMigrator")

    parser = argparse.ArgumentParser(
        description="HNR/NAP Migration Tool for PostgreSQL raw.library_flac & FLAC tags"
    )
    parser.add_argument(
        "--calc-db",
        type=float,
        metavar="NAP",
        help="NAP 値 (0.0〜1.0) を HNR (dB) に変換して表示しますわ",
    )
    parser.add_argument(
        "--calc-nap",
        type=float,
        metavar="HNR_DB",
        help="HNR (dB) 値を NAP (0.0〜1.0) に逆変換して表示しますわ",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="実際の DB 更新を行わずに修正対象件数とプレビューのみ表示しますわ",
    )
    parser.add_argument(
        "--fix-tags",
        action="store_true",
        help="実 FLAC ファイルの VorbisComment タグも同期更新しますわ",
    )
    parser.add_argument(
        "--batch-size",
        type=int,
        default=500,
        help="コミット単位の件数 (デフォルト: 500)",
    )
    parser.add_argument(
        "--limit",
        type=int,
        default=0,
        help="処理する最大レコード数 (0 の場合は全件)",
    )
    args = parser.parse_args()

    # 単体計算モード
    if args.calc_db is not None:
        db_val = calc_hnr_db(args.calc_db)
        inv_nap = calc_nap_from_hnr_db(db_val)
        print(f"\n{CYAN}=== HNR / NAP 単体変換結果 ==={RESET}")
        print(f"入力 NAP: {args.calc_db:.6f}")
        print(f"変換後 HNR: {db_val:.4f} dB")
        print(f"逆変換 NAP: {inv_nap:.6f} (完全可逆検証: 差分 {abs(inv_nap - args.calc_db):.2e})\n")
        return

    if args.calc_nap is not None:
        nap_val = calc_nap_from_hnr_db(args.calc_nap)
        inv_db = calc_hnr_db(nap_val)
        print(f"\n{CYAN}=== HNR / NAP 単体変換結果 ==={RESET}")
        print(f"入力 HNR: {args.calc_nap:.4f} dB")
        print(f"変換後 NAP: {nap_val:.6f}")
        print(f"逆変換 HNR: {inv_db:.4f} dB (完全可逆検証: 差分 {abs(inv_db - args.calc_nap):.2e})\n")
        return

    # DB マイグレーションモード
    db_url = get_db_url()
    if not db_url:
        logger.error(
            "DB URL が取得できませんでしたわ！ config.toml または環境変数 (FLAC_DB_URL/DATABASE_URL) をご確認くださいませ。"
        )
        sys.exit(1)

    logger.info("PostgreSQL へ接続しておりますわ...")
    try:
        conn = psycopg2.connect(db_url)
        cur = conn.cursor(cursor_factory=psycopg2.extras.DictCursor)
    except Exception as e:
        logger.exception(f"PostgreSQL への接続に失敗いたしましたわ: {e}")
        sys.exit(1)

    # features が NULL でないレコードを取得
    query = """
        SELECT id, audio_hash, filepath, title, artist, features
        FROM raw.library_flac
        WHERE features IS NOT NULL AND features != '{}'::jsonb
        ORDER BY id ASC
    """
    if args.limit > 0:
        query += f" LIMIT {args.limit}"

    logger.info("features カラムが存在する対象レコードをスキャンしておりますわ...")
    cur.execute(query)
    rows = cur.fetchall()
    total_scanned = len(rows)
    logger.info(f"スキャン対象: {total_scanned} 件 をロードいたしましたわ！")

    update_targets = []
    sample_preview = []

    for row in rows:
        rec_id = row["id"]
        filepath = row["filepath"]
        features = row["features"]

        if isinstance(features, str):
            try:
                features = json.loads(features)
            except Exception:
                continue

        if not isinstance(features, dict):
            continue

        # ディープコピーで変更を検知
        feat_copy = json.loads(json.dumps(features))
        if migrate_record_features(feat_copy):
            update_targets.append((rec_id, filepath, feat_copy))
            if len(sample_preview) < 3:
                sample_preview.append(
                    {
                        "id": rec_id,
                        "title": row["title"],
                        "artist": row["artist"],
                        "filepath": filepath,
                        "old_mix_hnr": features.get("mix", {})
                        .get("scalars", {})
                        .get("hnr"),
                        "new_mix_nap": feat_copy.get("mix", {})
                        .get("scalars", {})
                        .get("nap"),
                        "new_mix_hnr_db": feat_copy.get("mix", {})
                        .get("scalars", {})
                        .get("hnr_db"),
                    }
                )

    logger.info(f"マイグレーション対象レコード: {len(update_targets)} / {total_scanned} 件")

    if sample_preview:
        logger.info("【プレビュー サンプル (最大3件)】:")
        for sp in sample_preview:
            logger.info(
                f"  - ID: {sp['id']} | {sp['artist']} - {sp['title']} | 旧HNR: {sp['old_mix_hnr']} ➔ 新NAP: {sp['new_mix_nap']:.4f}, 新HNR_dB: {sp['new_mix_hnr_db']:.2f} dB"
            )

    if args.dry_run:
        logger.info(
            f"{YELLOW}[DryRun] 実際の更新はスキップされましたわ。{len(update_targets)} 件のレコードが変換可能ですの！{RESET}"
        )
        cur.close()
        conn.close()
        return

    if not update_targets:
        logger.info("更新が必要な旧形式レコードは存在いたしませんでしたわ。完璧ですの！")
        cur.close()
        conn.close()
        return

    # バッチ UPDATE 実行
    logger.info("DB レコードの更新を実行いたしますわ...")
    update_sql = """
        UPDATE raw.library_flac
        SET features = %s::jsonb,
            updated_at = NOW()
        WHERE id = %s
    """

    updated_count = 0
    flac_tags_updated = 0

    for i, (rec_id, filepath, new_features) in enumerate(update_targets, 1):
        cur.execute(update_sql, (json.dumps(new_features), rec_id))
        updated_count += 1

        if args.fix_tags and filepath and os.path.exists(filepath):
            mix_scalars = new_features.get("mix", {}).get("scalars", {})
            nap_val = mix_scalars.get("nap", 0.0)
            hnr_db_val = mix_scalars.get("hnr_db", 0.0)
            if update_flac_hnr_tags(filepath, nap_val, hnr_db_val):
                flac_tags_updated += 1

        if i % args.batch_size == 0:
            conn.commit()
            logger.info(f"進捗: {i}/{len(update_targets)} 件コミット完了...")

    conn.commit()
    logger.info(
        f"全 {updated_count} 件の DB マイグレーションが完了いたしましたわ！"
    )
    if args.fix_tags:
        logger.info(f"FLAC タグ更新完了件数: {flac_tags_updated} 件")

    cur.close()
    conn.close()


if __name__ == "__main__":
    main()
