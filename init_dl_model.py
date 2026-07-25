"""
Initial Setup and Deep Learning Model Downloader
===============================================
Essentia ONNX/PBモデルのダウンロード、TensorFlowモデルのONNX自動変換、
および仮想環境の構築を自動化しますの。
"""

import os
import sys
import urllib.request
import subprocess

MODELS_DIR = "models"
VENV_DIR = ".venv"

URLS = [
    # classification-heads
    "https://essentia.upf.edu/models/classification-heads/approachability/approachability_3c-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/approachability/approachability_3c-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/danceability/danceability-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/danceability/danceability-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/engagement/engagement_3c-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/engagement/engagement_3c-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/gender/gender-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/gender/gender-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/genre_dortmund/genre_dortmund-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/genre_dortmund/genre_dortmund-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/genre_electronic/genre_electronic-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/genre_electronic/genre_electronic-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/genre_rosamerica/genre_rosamerica-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/genre_rosamerica/genre_rosamerica-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/genre_tzanetakis/genre_tzanetakis-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/genre_tzanetakis/genre_tzanetakis-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/mood_acoustic/mood_acoustic-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/mood_acoustic/mood_acoustic-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/mood_aggressive/mood_aggressive-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/mood_aggressive/mood_aggressive-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/mood_electronic/mood_electronic-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/mood_electronic/mood_electronic-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/mood_happy/mood_happy-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/mood_happy/mood_happy-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/mood_party/mood_party-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/mood_party/mood_party-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/mood_relaxed/mood_relaxed-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/mood_relaxed/mood_relaxed-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/mood_sad/mood_sad-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/mood_sad/mood_sad-discogs-effnet-1.onnx",
    "https://essentia.upf.edu/models/classification-heads/voice_instrumental/voice_instrumental-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/voice_instrumental/voice_instrumental-discogs-effnet-1.onnx",

    # Feature extractors (Backbones)
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs-effnet-bs64-1.json",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs-effnet-bs64-1.onnx",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs-effnet-bs64-1.pb",

    # Embeddings
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_artist_embeddings-effnet-bs64-1.json",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_artist_embeddings-effnet-bs64-1.onnx",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_label_embeddings-effnet-bs64-1.json",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_label_embeddings-effnet-bs64-1.onnx",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_multi_embeddings-effnet-bs64-1.json",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_multi_embeddings-effnet-bs64-1.onnx",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_release_embeddings-effnet-bs64-1.json",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_release_embeddings-effnet-bs64-1.onnx",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_track_embeddings-effnet-bs64-1.json",
    "https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_track_embeddings-effnet-bs64-1.onnx",

    # MAEST
    "https://essentia.upf.edu/models/feature-extractors/maest/discogs-maest-30s-pw-519l-2.json",
    "https://essentia.upf.edu/models/feature-extractors/maest/discogs-maest-30s-pw-519l-2.onnx",

    # Genre Discogs400
    "https://essentia.upf.edu/models/classification-heads/genre_discogs400/genre_discogs400-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/genre_discogs400/genre_discogs400-discogs-effnet-1.pb",
]


def _download_progress(count, block_size, total_size):
    """シンプルな進捗表示コールバックですの。"""
    percent = int(count * block_size * 100 / total_size)
    percent = min(100, percent)
    sys.stdout.write(f"\r  └─ ダウンロード中... {percent}%")
    sys.stdout.flush()


def download_models():
    """Essentia ONNX/PBモデルをダウンロードしますわ。"""
    print("=== 1. Essentia Deep Learning モデルのダウンロードを開始しますわ ===")
    if not os.path.exists(MODELS_DIR):
        os.makedirs(MODELS_DIR)
        print(f"ディレクトリ `{MODELS_DIR}` を作成いたしましたの。")

    for url in URLS:
        fname = os.path.basename(url)
        dest = os.path.join(MODELS_DIR, fname)
        if os.path.exists(dest):
            if os.path.getsize(dest) > 0:
                print(f" [SKIP] {fname} (既に存在しておりますわ)")
                continue

        print(f" [DOWNLOAD] {url}")
        try:
            urllib.request.urlretrieve(url, dest, reporthook=_download_progress)
            print("\n  └─ 完了いたしましたわ！")
        except Exception as e:
            print(f"\n  └─ 警告: ダウンロード失敗いたしましたわ: {e}")


def transform_pb_to_onnx():
    """TensorFlowの .pb モデルを ONNX に自動変換（Transform）し、不要なモジュールをアンインストールしますわ。"""
    pb_file = os.path.join(MODELS_DIR, "genre_discogs400-discogs-effnet-1.pb")
    onnx_file = os.path.join(MODELS_DIR, "genre_discogs400-discogs-effnet-1.onnx")

    if not os.path.exists(pb_file):
        print("\n[TRANSFORM] 警告: 変換元の .pb ファイルが見つかりませんわ。")
        return

    if os.path.exists(onnx_file) and os.path.getsize(onnx_file) > 0:
        print("\n[TRANSFORM] [SKIP] genre_discogs400 ONNXモデルは既に存在しておりますわ。")
        return

    print("\n=== 2. TensorFlow pbモデルから ONNXモデルへの自己変換（Transform）を開始しますわ ===")

    pip_exe = os.path.join(VENV_DIR, "Scripts", "pip.exe")
    python_exe = os.path.join(VENV_DIR, "Scripts", "python.exe")
    if not os.path.exists(pip_exe):
        pip_exe = os.path.join(VENV_DIR, "bin", "pip")
        python_exe = os.path.join(VENV_DIR, "bin", "python")

    # A. 一時的に tensorflow と tf2onnx をインストール
    print("変換用モジュール (tensorflow, tf2onnx) を一時的にインストールしますわ...")
    try:
        subprocess.run([pip_exe, "install", "tensorflow", "tf2onnx"], check=True)
    except Exception as e:
        print(f"  └─ エラー: 変換用モジュールのインストールに失敗しましたの: {e}")
        return

    # B. tf2onnx を用いて変換を実行
    print("TF2ONNX 変換を実行しておりますわ...")
    try:
        subprocess.run(
            [
                python_exe, "-m", "tf2onnx.convert",
                "--input", pb_file,
                "--inputs", "serving_default_model_Placeholder:0",
                "--outputs", "PartitionedCall:0",
                "--output", onnx_file,
                "--opset", "15"
            ],
            check=True
        )
        print("  └─ ONNX への自己変換が大成功いたしましたわ！")
    except Exception as e:
        print(f"  └─ エラー: ONNX 変換に失敗しましたの: {e}")

    # C. 不要なモジュールをアンインストールして環境をクリーンアップ
    print("一時モジュール (tensorflow, tf2onnx) をアンインストールして環境をクリーンアップしますわ...")
    try:
        subprocess.run([pip_exe, "uninstall", "-y", "tensorflow", "tf2onnx"], check=True)
        print("  └─ クリーンアップが完了いたしましたわ！")
    except Exception as e:
        print(f"  └─ 警告: アンインストール中に警告が発生しましたの: {e}")


def setup_environment():
    """Python 仮想環境の構築と依存モジュールのインストールを実行しますわ。"""
    print("\n=== 3. Python仮想環境と依存モジュールのセットアップを開始しますわ ===")

    # 1. 仮想環境 作成
    if not os.path.exists(VENV_DIR):
        print("仮想環境を作成しておりますわ...")
        try:
            subprocess.run([sys.executable, "-m", "venv", VENV_DIR], check=True)
            print("  └─ 仮想環境が正常に作成されましたわ！")
        except Exception as e:
            print(f"  └─ エラー: 仮想環境の作成に失敗しましたの: {e}")
            return
    else:
        print("仮想環境は既に存在しておりますの。")

    # 2. pip を介した requirements.txt のインストール
    pip_exe = os.path.join(VENV_DIR, "Scripts", "pip.exe")
    if not os.path.exists(pip_exe):
        pip_exe = os.path.join(VENV_DIR, "bin", "pip")

    if os.path.exists(pip_exe):
        print("pip をアップグレードしておりますわ...")
        try:
            subprocess.run([pip_exe, "install", "--upgrade", "pip"], check=True)
        except Exception as e:
            print(f"  └─ pipのアップグレード中に警告が発生しましたの: {e}")

        req_file = "requirements.txt"
        if os.path.exists(req_file):
            print(f"{req_file} に基づいて依存ライブラリをインストールしておりますわ...")
            try:
                subprocess.run([pip_exe, "install", "-r", req_file], check=True)
                print("  └─ ライブラリのインストールが完了いたしましたわ！")
            except Exception as e:
                print(f"  └─ エラー: 依存ライブラリのインストールに失敗しましたの: {e}")
        else:
            print(f"  └─ 警告: `{req_file}` が見つかりませんわ。")
    else:
        print(f"  └─ エラー: `{pip_exe}` が見つかりませんの。")


if __name__ == "__main__":
    download_models()
    setup_environment()
    transform_pb_to_onnx()
    print("\nすべての初期セットアップと変換処理が完了いたしましたわ！おーほほほほ！")
