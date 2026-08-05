"""
worker_librosa.py
=================
Goから起動されるLibrosa専用ワーカーですわ。
Demucsから書き込まれ、Goによって Freeze (Read-Only化) された共有メモリをアタッチし、
Librosaによる特徴量抽出を行って結果をJSONで出力しますの。
"""

import argparse
import json
import logging
import sys
import time

# プロジェクト内のモジュール
import shm_interop
from analyzer import AudioContext, STEM_CONFIGS, librosa_extractor

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [%(name)s] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)] # GoにパースさせるJSONと混ざらないようstderrに出力
    )

def main():
    setup_logger()
    logger = logging.getLogger("LibrosaWorker")

    parser = argparse.ArgumentParser()
    parser.add_argument("--shm-metadata", required=True, help="JSON string containing sr and stems metadata from DemucsWorker")
    parser.add_argument("--track-hash", required=True, help="MD5 hash of the track for DB primary key")
    args = parser.parse_args()

    try:
        metadata = json.loads(args.shm_metadata)
        sr = metadata["sr"]
        stems_info = metadata["stems"]
    except Exception as e:
        logger.exception("--shm-metadata のパースに失敗いたしましたわ！")
        sys.exit(1)

    logger.info("共有メモリからの Librosa 特徴量抽出を開始いたしますわ！")
    t_start = time.perf_counter()
    
    extracted_features = {}

    for stem_name, info in stems_info.items():
        tag_name = info["shm_tag"]
        shape = tuple(info["shape"])
        dtype_name = info["dtype"]
        file_size = info.get("file_size", 0)
        spectro_path = info.get("spectro_path")
        
        logger.info(f"ステム [{stem_name}] のため共有メモリ [{tag_name}] (Read-Only) にアタッチいたしますわ")
        shm, y = shm_interop.attach_shm_read_only(tag_name, shape, dtype_name, file_size=file_size)
        
        try:
            # AudioContext の構築 (Zero-copy)
            ctx = AudioContext(y=y, sr=sr, source=stem_name, spectro_path=spectro_path)
            
            # config取得と Pre-warming
            config = STEM_CONFIGS.get(stem_name, STEM_CONFIGS["other"])
            for prop in config["warmup"]:
                try:
                    _ = getattr(ctx, prop)
                except Exception as e:
                    logger.warning(f"Pre-warming '{prop}' 実行中に問題が発生いたしましたわ: {e}")
            
            # Librosa 抽出実行
            logger.info(f"ステム [{stem_name}] の特徴量を抽出しておりますわ...")
            raw_features = librosa_extractor.run(ctx)
            
            # 結果を辞書に変換（Postgresへの格納用などにシリアライズ）
            if hasattr(raw_features, "to_postgres_dict"):
                extracted_features[stem_name] = raw_features.to_postgres_dict(track_id=args.track_hash)
            else:
                # 代替処理：dataclassならasdictなど
                import dataclasses
                if dataclasses.is_dataclass(raw_features):
                    extracted_features[stem_name] = dataclasses.asdict(raw_features)
                else:
                    extracted_features[stem_name] = str(raw_features)
                    
            # 明示的な解放
            ctx.clear()
            
        except Exception as e:
            logger.exception(f"ステム [{stem_name}] の処理中にエラーが発生いたしましたわ！")
            sys.exit(1)
        finally:
            shm.close()
            
    logger.info(f"全ステムの Librosa 特徴量抽出が無事に完了いたしましたわ (経過: {time.perf_counter() - t_start:.4f}s)")
    
    final_features = {"demucs": {}}
    for k, v in extracted_features.items():
        if k == "mix":
            final_features["mix"] = v
        else:
            final_features["demucs"][k] = v
            
    # 成功したら stdout に特徴量をJSONで吐き出して終了
    print(json.dumps({"status": "success", "features": final_features}))
    sys.exit(0)

if __name__ == "__main__":
    main()
