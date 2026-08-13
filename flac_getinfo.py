"""
flac_getinfo.py
===============
読取専有射 (Reader Morphism / Pure Inspection Container) モジュールですわ。

FLAC ファイルからの VorbisComment メタデータ、Embedded CUE シート、および
音声ストリーム基本情報 (SampleRate, Channels, BitsPerSample, TotalSamples, Duration)
の読み取り副作用を本モジュールに単一カプセル化いたします。
"""

import os
import sys
import json
import logging
from dataclasses import dataclass, asdict
from typing import Any, Dict, Optional
from mutagen.flac import FLAC, FLACNoHeaderError


@dataclass(frozen=True)
class FlacInfo:
    """FLAC ファイルの不変（Immutable）な情報カプセルですわ。"""

    filepath: str
    sample_rate: int
    channels: int
    bits_per_sample: int
    total_samples: int
    duration_sec: float
    bitrate: int
    file_size_bytes: int
    vorbis_comments: Dict[str, str]
    cuesheet_raw: Optional[str]
    has_embedded_cue: bool

    def to_dict(self) -> Dict[str, Any]:
        """辞書表現へ変換いたしますわ。"""
        return asdict(self)

    def to_json(self, indent: int = 2) -> str:
        """JSON 文字列表現へ変換いたしますわ。"""
        return json.dumps(self.to_dict(), ensure_ascii=False, indent=indent)


def get_flac_info(filepath: str) -> FlacInfo:
    """単一 FLAC ファイルから FlacInfo を一元取得する純粋関数（読取射）ですの。

    Args:
        filepath: 対象 FLAC ファイルのパス

    Returns:
        FlacInfo: 読み取り結果データ構造体

    Raises:
        FileNotFoundError: ファイルが存在しない場合
        ValueError: FLAC ヘッダー解析失敗・不正なファイルの場合
    """
    abs_path = os.path.abspath(filepath)
    if not os.path.isfile(abs_path):
        raise FileNotFoundError(f"FLAC ファイルが存在いたしませんわ: {abs_path}")

    file_size = os.path.getsize(abs_path)

    try:
        audio = FLAC(abs_path)
    except FLACNoHeaderError as e:
        raise ValueError(f"有効な FLAC ヘッダーが見つかりませんでしたわ ({abs_path}): {e}")
    except Exception as e:
        raise ValueError(f"FLAC メタデータ解析中にエラーが発生いたしましたわ ({abs_path}): {e}")

    info = audio.info
    sample_rate = int(getattr(info, "sample_rate", 44100))
    channels = int(getattr(info, "channels", 2))
    bits_per_sample = int(getattr(info, "bits_per_sample", 16))
    total_samples = int(getattr(info, "total_samples", 0))
    length = float(getattr(info, "length", 0.0))
    bitrate = int(getattr(info, "bitrate", 0))

    # VorbisComment を一元収集 (大文字キーに統一しつつ、値は文字列で格納)
    comments: Dict[str, str] = {}
    cuesheet_raw: Optional[str] = None

    if audio.tags:
        for key, values in audio.tags:
            val_str = values[0] if isinstance(values, list) and len(values) > 0 else str(values)
            comments[key.upper()] = val_str
            if key.upper() == "CUESHEET" and val_str.strip():
                cuesheet_raw = val_str.strip()

    # CUESHEET タグがない場合でも、audio.cuesheet ブロックがあれば抽出を試みます
    if not cuesheet_raw and hasattr(audio, "cuesheet") and audio.cuesheet:
        try:
            cuesheet_raw = str(audio.cuesheet)
        except Exception:
            pass

    has_embedded_cue = bool(cuesheet_raw and len(cuesheet_raw.strip()) > 0)

    return FlacInfo(
        filepath=abs_path,
        sample_rate=sample_rate,
        channels=channels,
        bits_per_sample=bits_per_sample,
        total_samples=total_samples,
        duration_sec=length,
        bitrate=bitrate,
        file_size_bytes=file_size,
        vorbis_comments=comments,
        cuesheet_raw=cuesheet_raw,
        has_embedded_cue=has_embedded_cue,
    )


def main():
    """インスペクタ CLI としてのメイン処理ですわ。"""
    if len(sys.argv) < 2:
        print("Usage: python flac_getinfo.py <path_to_flac_file>")
        sys.exit(1)

    target_path = sys.argv[1]
    try:
        info = get_flac_info(target_path)
        print(info.to_json())
    except Exception as e:
        logging.error(f"解析失敗: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()
