"""
init_dl_model.py (Root Forwarder to zig/init_dl_model.py)
"""
import sys
import os

PROJECT_ROOT = os.path.abspath(os.path.dirname(__file__))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from zig.init_dl_model import (
    download_models,
    setup_environment,
    transform_pb_to_onnx,
)

if __name__ == "__main__":
    download_models()
    setup_environment()
    transform_pb_to_onnx()
    print("\n✨ すべてのモデル取得・ONNX変換・環境セットアップが完了いたしましたわ！おーほほほほ！")
