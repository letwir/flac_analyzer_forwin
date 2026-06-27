import logging
import os
import sys
import warnings
import glob as glob_module

# Windowsの標準出力をUTF-8に変更しますの
sys.stdout.reconfigure(encoding='utf-8')

# librosaの警告をエラーにしてスタックトレースを捕捉しますの！
warnings.filterwarnings('error', category=UserWarning)

import main
import models
import pipeline


def run_test():
    logging.info("テスト開始しますわ！")

    # モデルのロード
    models_dir = "./models"
    essentia_models = models.build_essentia_models(models_dir)
    models.init_global_onnx_sessions(models_dir, essentia_models)
    models.init_global_demucs()

    # テスト対象ファイル: testFLAC/ ディレクトリ内の全.flacファイル
    test_files = glob_module.glob("testFLAC/*.flac") + glob_module.glob("testFLAC/**/*.flac", recursive=True)
    if not test_files:
        logging.error("テストファイル {test/*.flac} が見つかりませんわ！")
        return

    for test_file in sorted(test_files):
        logging.info(f"解析テスト対象ファイル: {test_file}")
        res = pipeline.process_single_flac_file(test_file, essentia_models)
        logging.info(f"結果: {res}")


if __name__ == "__main__":
    run_test()
