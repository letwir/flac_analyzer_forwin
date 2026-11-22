"""
zig/init_dl_model.py
====================
Essentia ONNX/PBモデルのダウンロード、TensorFlowモデルのONNX自動変換、
および仮想環境の構築を自動化する治具スクリプトですわ！

使い方:
    python zig/init_dl_model.py
"""

import os
import re
import sys
import urllib.request
import subprocess

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
if hasattr(sys.stderr, "reconfigure"):
    sys.stderr.reconfigure(encoding="utf-8")

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

MODELS_DIR = os.path.join(PROJECT_ROOT, "models")
VENV_DIR = os.path.join(PROJECT_ROOT, ".venv")

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

    # Genre Discogs400 (.pb 形式のみ提供されているモデル)
    "https://essentia.upf.edu/models/classification-heads/genre_discogs400/genre_discogs400-discogs-effnet-1.json",
    "https://essentia.upf.edu/models/classification-heads/genre_discogs400/genre_discogs400-discogs-effnet-1.pb",
]


def _download_progress(count, block_size, total_size):
    """シンプルな進捗表示コールバックですの。"""
    if total_size <= 0:
        return
    percent = int(count * block_size * 100 / total_size)
    percent = min(100, percent)
    sys.stdout.write(f"\r  └─ ダウンロード中... {percent}%")
    sys.stdout.flush()


def download_models():
    """Essentia ONNX/PBモデルをダウンロードしますわ。"""
    print("\n=== 1. Essentia Deep Learning モデルのダウンロードを開始しますわ ===")
    if not os.path.exists(MODELS_DIR):
        os.makedirs(MODELS_DIR)
        print(f"ディレクトリ `{MODELS_DIR}` を作成いたしましたの。")

    for url in URLS:
        fname = os.path.basename(url)
        dest = os.path.join(MODELS_DIR, fname)
        if os.path.exists(dest) and os.path.getsize(dest) > 0:
            print(f" [SKIP] {fname} (既に存在しておりますわ)")
            continue

        print(f" [DOWNLOAD] {url}")
        try:
            req = urllib.request.Request(
                url,
                headers={'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)'}
            )
            with urllib.request.urlopen(req) as response, open(dest, 'wb') as out_file:
                total_size = int(response.headers.get('Content-Length', 0))
                block_size = 8192
                count = 0
                while True:
                    buffer = response.read(block_size)
                    if not buffer:
                        break
                    out_file.write(buffer)
                    count += 1
                    _download_progress(count, block_size, total_size)
            print("\n  └─ 完了いたしましたわ！")
        except Exception as e:
            print(f"\n  └─ 警告: ダウンロードに失敗いたしましたわ ({fname}): {e}")


PB_CONVERSIONS = (
    (
        "discogs-effnet-bs64-1.pb",
        "discogs-effnet-bs64-1.onnx",
        "serving_default_melspectrogram:0",
        "PartitionedCall:1",
    ),
    (
        "genre_discogs400-discogs-effnet-1.pb",
        "genre_discogs400-discogs-effnet-1.onnx",
        "serving_default_model_Placeholder:0",
        "PartitionedCall:0",
    ),
)

BLACKWELL_GPU_PATTERN = re.compile(
    r"\bRTX\s*50\d{2}\b|\bB[12]00\b|\bGB\d+\b|Blackwell",
    re.IGNORECASE,
)


def is_blackwell_gpu() -> bool:
    """nvidia-smi で Blackwell 世代 GPU か判定しますわ。"""
    try:
        res = subprocess.run(
            ["nvidia-smi", "--query-gpu=name", "--format=csv,noheader"],
            capture_output=True,
            text=True,
            timeout=5,
            check=False,
        )
    except (FileNotFoundError, subprocess.SubprocessError, OSError):
        return False
    return res.returncode == 0 and bool(BLACKWELL_GPU_PATTERN.search(res.stdout))


def select_requirements_file(project_root: str, blackwell: bool) -> str:
    """GPU 世代に応じて使用する requirements ファイルを返しますわ。"""
    blackwell_req = os.path.join(project_root, "requirements-blackwell.txt")
    if blackwell and os.path.exists(blackwell_req):
        return blackwell_req
    return os.path.join(project_root, "requirements.txt")


def _find_missing_modules(python_exe: str, modules: tuple[str, ...]) -> list[str]:
    check_code = (
        "import importlib.util, sys; "
        "missing = [m for m in sys.argv[1:] if importlib.util.find_spec(m) is None]; "
        "print(' '.join(missing))"
    )
    res = subprocess.run(
        [python_exe, "-c", check_code, *modules],
        capture_output=True,
        text=True,
        check=True,
    )
    return [m for m in res.stdout.strip().split() if m]


def transform_pb_to_onnx():
    """TensorFlowの .pb モデルを ONNX に自動変換（Transform）し、一時導入したモジュールのみクリーンアップしますわ。"""
    pending: list[tuple[str, str, str, str]] = []
    for pb_name, onnx_name, input_name, output_name in PB_CONVERSIONS:
        pb_file = os.path.join(MODELS_DIR, pb_name)
        onnx_file = os.path.join(MODELS_DIR, onnx_name)
        if not os.path.exists(pb_file):
            print(f"\n[TRANSFORM] 警告: 変換元の .pb ファイルが見つかりませんわ ({pb_name})。")
            continue
        if os.path.exists(onnx_file) and os.path.getsize(onnx_file) > 0:
            print(f"\n[TRANSFORM] [SKIP] {onnx_name} は既に存在しておりますわ。")
            continue
        pending.append((pb_file, onnx_file, input_name, output_name))

    if not pending:
        return

    print("\n=== 2. TensorFlow pbモデルから ONNXモデルへの自己変換（Transform）を開始しますわ ===")

    pip_exe = os.path.join(VENV_DIR, "Scripts", "pip.exe")
    python_exe = os.path.join(VENV_DIR, "Scripts", "python.exe")
    if not os.path.exists(pip_exe):
        pip_exe = sys.executable
        python_exe = sys.executable

    missing_modules = _find_missing_modules(python_exe, ("tensorflow", "tf2onnx"))
    temp_installed: list[str] = []
    try:
        if missing_modules:
            print(f"変換用モジュール ({', '.join(missing_modules)}) を一時的にインストールしますわ...")
            subprocess.run([pip_exe, "install", *missing_modules], check=True)
            temp_installed = missing_modules

        for pb_file, onnx_file, input_name, output_name in pending:
            print(f"TF2ONNX 変換を実行しておりますわ: {os.path.basename(pb_file)} -> {os.path.basename(onnx_file)}")
            subprocess.run(
                [
                    python_exe, "-m", "tf2onnx.convert",
                    "--input", pb_file,
                    "--inputs", input_name,
                    "--outputs", output_name,
                    "--output", onnx_file,
                    "--opset", "15",
                ],
                check=True,
            )
            if not os.path.exists(onnx_file) or os.path.getsize(onnx_file) == 0:
                raise RuntimeError(f"ONNX conversion produced empty or missing output: {onnx_file}")
            print(f"  └─ 完了いたしましたわ: {os.path.basename(onnx_file)}")
    finally:
        if temp_installed:
            print(f"一時モジュール ({', '.join(temp_installed)}) をアンインストールして環境をクリーンアップしますわ...")
            try:
                subprocess.run([pip_exe, "uninstall", "-y", *temp_installed], check=True)
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

    # 2. pip を介した requirements のインストール
    pip_exe = os.path.join(VENV_DIR, "Scripts", "pip.exe")
    if not os.path.exists(pip_exe):
        pip_exe = "pip"

    print("pip をアップグレードしておりますわ...")
    try:
        subprocess.run([pip_exe, "install", "--upgrade", "pip"], check=True)
    except Exception as e:
        print(f"  └─ pipのアップグレード中に警告が発生しましたの: {e}")

    blackwell = is_blackwell_gpu()
    req_file = select_requirements_file(PROJECT_ROOT, blackwell)
    if os.path.basename(req_file) == "requirements-blackwell.txt":
        print(f"💡 Blackwell (RTX 50xx) GPU を検出したため {req_file} を使用いたしますわ。")
    else:
        print(f"標準要件ファイル {req_file} を使用いたしますわ。")

    if os.path.exists(req_file):
        print(f"{req_file} に基づいて依存ライブラリをインストールしておりますわ...")
        try:
            subprocess.run([pip_exe, "install", "-r", req_file], check=True)
            print("  └─ ライブラリのインストールが完了いたしましたわ！")
        except Exception as e:
            print(f"  └─ エラー: 依存ライブラリのインストールに失敗しましたの: {e}")
    else:
        print(f"  └─ 警告: `{req_file}` が見つかりませんわ。")


if __name__ == "__main__":
    download_models()
    setup_environment()
    transform_pb_to_onnx()
    print("\n✨ すべてのモデル取得・ONNX変換・環境セットアップが完了いたしましたわ！おーほほほほ！")
