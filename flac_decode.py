import os
import struct
import subprocess
import hashlib
import numpy as np
import soxr
from dataclasses import dataclass
from mutagen.flac import FLAC

@dataclass(frozen=True)
class TrackSlice:
    track_number: int
    start_sample: int
    end_sample: int
    title: str
    artist: str
    composer: str | None = None

@dataclass(frozen=True)
class FlacHandle:
    filepath: str
    sample_rate: int
    channels: int
    bits_per_sample: int
    total_samples: int
    tags: dict
    slices: list[TrackSlice]

def build_flac_handle(filepath: str) -> FlacHandle:
    """mutagen を使用して FLAC ファイルのメタデータと CUE スライス情報を構築しますの"""
    filepath = os.path.abspath(filepath)
    meta = FLAC(filepath)
    sr = meta.info.sample_rate
    total_samples = meta.info.total_samples
    channels = meta.info.channels
    bits_per_sample = meta.info.bits_per_sample
    
    # Vorbis comments を辞書化しますわ
    raw_tags = {}
    for k, v in meta.items():
        val_list = [str(x) for x in v]
        key_lower = k.lower()
        if len(val_list) == 1:
            raw_tags[key_lower] = val_list[0]
        elif len(val_list) == 0:
            raw_tags[key_lower] = ""
        else:
            raw_tags[key_lower] = val_list
            
    slices = []
    
    # A) Vorbis comment に "cuesheet" がテキストで埋め込まれているか確認しますわ
    cue_text = None
    if "cuesheet" in raw_tags:
        cue_text = raw_tags["cuesheet"]
        if isinstance(cue_text, list):
            cue_text = cue_text[0]
            
    # B) mutagen の cuesheet メタデータブロックがあるか確認しますの
    cue_block = None
    if meta.metadata_blocks:
        for block in meta.metadata_blocks:
            if "cuesheet" in type(block).__name__.lower():
                cue_block = block
                break
                
    if cue_text:
        # CUE テキストがある場合はパースします（共通のインデックス境界計算ロジック）
        cue_slices, global_tags = parse_cue_text_to_slices(cue_text, total_samples, sr)
        slices = cue_slices
        # マージ
        if global_tags.get("title") and "album" not in raw_tags:
            raw_tags["album"] = global_tags["title"]
        if global_tags.get("performer"):
            if "albumartist" not in raw_tags and "album artist" not in raw_tags:
                raw_tags["albumartist"] = global_tags["performer"]
            if "artist" not in raw_tags:
                raw_tags["artist"] = global_tags["performer"]
    elif cue_block:
        # CUE ブロックがある場合
        raw_tracks = []
        for t in cue_block.tracks:
            if t.type == 0:  # audio track
                raw_tracks.append({
                    "track": t.track_number,
                    "start": t.start_offset,
                })
        raw_tracks.sort(key=lambda x: x["track"])
        for i, t in enumerate(raw_tracks):
            start = t["start"]
            end = raw_tracks[i+1]["start"] if i+1 < len(raw_tracks) else total_samples
            slices.append(TrackSlice(
                track_number=t["track"],
                start_sample=start,
                end_sample=int(min(end, total_samples)),
                title=f"Track {t['track']}",
                artist=raw_tags.get("artist", "Unknown")
            ))
            
    # C) どちらもない場合は、単一の曲全体を1つのスライスにしますわ
    if not slices:
        slices.append(TrackSlice(
            track_number=1,
            start_sample=0,
            end_sample=total_samples,
            title=raw_tags.get("title", "Unknown"),
            artist=raw_tags.get("artist", "Unknown")
        ))
        
    return FlacHandle(
        filepath=filepath,
        sample_rate=sr,
        channels=channels,
        bits_per_sample=bits_per_sample,
        total_samples=total_samples,
        tags=raw_tags,
        slices=slices
    )

def parse_cue_text_to_slices(cue_text: str, total_samples: int, sr: int) -> tuple[list[TrackSlice], dict[str, str]]:
    """CUEテキストから INDEX 01 境界を抽出し、サンプル単位のスライスリストとグローバルメタデータを構築しますわ"""
    import re
    lines = cue_text.splitlines()
    tracks = []
    
    current_track = None
    track_title = ""
    track_artist = ""
    track_index_sample = -1
    
    global_title = ""
    global_performer = ""
    
    # INDEX 01 MM:SS:FF をサンプル数に変換するヘルパー
    def time_to_samples(m, s, f) -> int:
        # FF = 75 frames/second
        total_seconds = m * 60 + s + f / 75.0
        return int(total_seconds * sr)
        
    for line in lines:
        line_strip = line.strip()
        if not line_strip:
            continue
            
        if current_track is None:
            # グローバル（TRACK登場前）のメタデータを拾う
            title_match = re.match(r"^TITLE\s+[\"']?(.+?)[\"']?$", line_strip, re.IGNORECASE)
            if title_match:
                global_title = title_match.group(1)
                continue
            perf_match = re.match(r"^PERFORMER\s+[\"']?(.+?)[\"']?$", line_strip, re.IGNORECASE)
            if perf_match:
                global_performer = perf_match.group(1)
                continue
            
        # TRACK 01 AUDIO
        track_match = re.match(r"^TRACK\s+(\d+)\s+AUDIO", line_strip, re.IGNORECASE)
        if track_match:
            # 以前のトラック情報があれば保存しますの
            if current_track is not None and track_index_sample >= 0:
                tracks.append({
                    "track": current_track,
                    "start": track_index_sample,
                    "title": track_title,
                    "artist": track_artist,
                })
            current_track = int(track_match.group(1))
            track_title = ""
            track_artist = ""
            track_index_sample = -1
            continue
            
        if current_track is not None:
            # TITLE "..."
            title_match = re.match(r"^TITLE\s+[\"']?(.+?)[\"']?$", line_strip, re.IGNORECASE)
            if title_match:
                track_title = title_match.group(1)
                continue
            # PERFORMER "..."
            performer_match = re.match(r"^PERFORMER\s+[\"']?(.+?)[\"']?$", line_strip, re.IGNORECASE)
            if performer_match:
                track_artist = performer_match.group(1)
                continue
            # INDEX 01 MM:SS:FF
            index_match = re.match(r"^INDEX\s+01\s+(\d+):(\d+):(\d+)", line_strip, re.IGNORECASE)
            if index_match:
                m = int(index_match.group(1))
                s = int(index_match.group(2))
                f = int(index_match.group(3))
                track_index_sample = time_to_samples(m, s, f)
                continue
                
    # 最後のトラックを保存
    if current_track is not None and track_index_sample >= 0:
        tracks.append({
            "track": current_track,
            "start": track_index_sample,
            "title": track_title,
            "artist": track_artist,
        })
        
    tracks.sort(key=lambda x: x["track"])
    slices = []
    for i, t in enumerate(tracks):
        start = t["start"]
        end = tracks[i+1]["start"] if i+1 < len(tracks) else total_samples
        slices.append(TrackSlice(
            track_number=t["track"],
            start_sample=start,
            end_sample=int(min(end, total_samples)),
            title=t["title"],
            artist=t["artist"]
        ))
    return slices, {"title": global_title, "performer": global_performer}

def parse_wav_header(wav_bytes: bytes) -> tuple[int, int, int, int, int, int]:
    """WAVバイト列からヘッダ情報をパースしますわ"""
    if len(wav_bytes) < 44 or wav_bytes[0:4] != b'RIFF' or wav_bytes[8:12] != b'WAVE':
        raise ValueError("Invalid WAV format (no RIFF/WAVE header)")
        
    offset = 12
    limit = len(wav_bytes)
    
    wFormatTag, numChannels, sampleRate, bitsPerSample = 0, 0, 0, 0
    data_offset, data_size = 0, 0
    
    while offset + 8 <= limit:
        chunk_id = wav_bytes[offset:offset+4]
        chunk_size = struct.unpack_from('<I', wav_bytes, offset+4)[0]
        offset += 8
        
        if chunk_id == b'fmt ':
            wFormatTag, numChannels, sampleRate, _, _, bitsPerSample = struct.unpack_from(
                '<H H I I H H', wav_bytes, offset
            )
            # WAVE_FORMAT_EXTENSIBLE の拡張部パース
            if wFormatTag == 0xFFFE:
                cbSize = struct.unpack_from('<H', wav_bytes, offset + 16)[0]
                if cbSize >= 22:
                    wValidBitsPerSample, dwChannelMask, subformat_guid = struct.unpack_from(
                        '<H I 16s', wav_bytes, offset + 18
                    )
                    real_tag = struct.unpack_from('<H', subformat_guid, 0)[0]
                    wFormatTag = real_tag
                    if wValidBitsPerSample > 0:
                        bitsPerSample = wValidBitsPerSample
                        
        elif chunk_id == b'data':
            data_offset = offset
            data_size = chunk_size
            break
            
        offset += chunk_size
        if chunk_size % 2 == 1:
            offset += 1
            
    if data_offset == 0:
        raise ValueError("WAV data chunk not found in buffer")
        
    return wFormatTag, numChannels, sampleRate, bitsPerSample, data_offset, data_size

def decode_flac_range(filepath: str, start_sample: int, end_sample: int) -> tuple[bytes, int, int, int, int]:
    """指定されたサンプル範囲のみを flac CLI でデコードし、生PCMデータとフォーマット情報を返しますの"""
    cmd = [
        'flac', '-d', '-c',
        f'--skip={start_sample}',
        f'--until={end_sample}',
        '--totally-silent',
        filepath
    ]
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL
    )
    wav_bytes = proc.stdout.read()
    rc = proc.wait()
    if rc != 0:
        raise RuntimeError(f"flac range decode failed: rc={rc}, cmd={cmd}")
        
    wFormatTag, numChannels, sampleRate, bitsPerSample, data_offset, data_size = parse_wav_header(wav_bytes)
    raw_pcm = wav_bytes[data_offset : data_offset + data_size]
    
    return raw_pcm, wFormatTag, numChannels, sampleRate, bitsPerSample

def pcm_bytes_to_float32(
    pcm_bytes: bytes, 
    wFormatTag: int, 
    bits_per_sample: int, 
    channels: int
) -> np.ndarray:
    """生PCMバイト列を float32 [-1.0, 1.0] 配列へ正規化変換しますわ"""
    if wFormatTag == 1:  # PCM Signed Integer
        if bits_per_sample == 16:
            samples = np.frombuffer(pcm_bytes, dtype=np.int16)
            float_audio = samples.astype(np.float32) / 32768.0
        elif bits_per_sample == 24:
            # 24bit: 3 bytes/sample -> uint8 を reshape して手動 int32 組み立て
            raw = np.frombuffer(pcm_bytes, dtype=np.uint8).reshape(-1, 3)
            i32 = (raw[:, 0].astype(np.int32)
                 | (raw[:, 1].astype(np.int32) << 8)
                 | (raw[:, 2].astype(np.int32) << 16))
            # 符号拡張 (24bit signed -> 32bit signed)
            i32[i32 >= 0x800000] -= 0x1000000
            float_audio = i32.astype(np.float32) / 8388608.0
        elif bits_per_sample == 32:
            samples = np.frombuffer(pcm_bytes, dtype=np.int32)
            float_audio = samples.astype(np.float32) / 2147483648.0
        else:
            raise ValueError(f"Unsupported integer bits_per_sample: {bits_per_sample}")
            
    elif wFormatTag == 3:  # IEEE Float
        if bits_per_sample == 32:
            float_audio = np.frombuffer(pcm_bytes, dtype=np.float32).copy()
        elif bits_per_sample == 64:
            float_audio = np.frombuffer(pcm_bytes, dtype=np.float64).astype(np.float32)
        else:
            raise ValueError(f"Unsupported float bits_per_sample: {bits_per_sample}")
    else:
        raise ValueError(f"Unsupported wFormatTag: {wFormatTag}")
        
    return float_audio.reshape(-1, channels)

def process_slice_with_seq_safety(
    filepath: str, 
    start_sample: int, 
    end_sample: int, 
    sample_rate: int,
    channels: int
) -> tuple[np.ndarray, str]:
    """1トラックまたは1ファイルの連続した音声データを安全に取得し、44.1kHzにリサンプリングしてハッシュと返しますの"""
    total_samples = end_sample - start_sample
    duration_sec = total_samples / sample_rate
    
    # 10分未満なら一括デコードして処理 (Simple & Fast)
    if duration_sec < 600.0:
        raw_pcm, wFormatTag, numChannels, raw_sr, bitsPerSample = decode_flac_range(
            filepath, start_sample, end_sample
        )
        md5_hash = hashlib.md5(raw_pcm).hexdigest()
        chunk_float = pcm_bytes_to_float32(raw_pcm, wFormatTag, bitsPerSample, numChannels)
        if raw_sr != 44100:
            audio_44100 = soxr.resample(chunk_float, raw_sr, 44100)
        else:
            audio_44100 = chunk_float
        return audio_44100, md5_hash

    # 10分以上の長尺（DJミックスなど）の場合は、ストリーミングで読み出しながらその場でダウンサンプリング
    cmd = [
        'flac', '-d', '-c',
        f'--skip={start_sample}',
        f'--until={end_sample}',
        '--totally-silent',
        filepath
    ]
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL
    )
    
    # ヘッダ情報が完全に読めるまで最初の4096バイトをバッファリングしますわ
    header_buffer = proc.stdout.read(4096)
    wFormatTag, numChannels, sampleRate, bitsPerSample, data_offset, data_size = parse_wav_header(header_buffer)
    
    md5_engine = hashlib.md5()
    
    # ヘッダバッファ内の余剰PCMデータを処理
    initial_pcm = header_buffer[data_offset:]
    if len(initial_pcm) > 0:
        md5_engine.update(initial_pcm)
        
    # ストリームから残りの生PCMブロックを順次読み込みますの
    # 384kHz, 2ch, 32bit の 1秒分 = 3,072,000 bytes
    bytes_per_sample = bitsPerSample // 8
    frame_size = numChannels * bytes_per_sample
    block_size = int(sampleRate * 2.0 * frame_size)  # 2秒分バッファ
    
    pcm_chunks = [initial_pcm]
    
    while True:
        block = proc.stdout.read(block_size)
        if not block:
            break
        md5_engine.update(block)
        pcm_chunks.append(block)
        
    proc.wait()
    
    # すべてのPCMバイトをマージ
    all_pcm = b"".join(pcm_chunks)
    final_md5 = md5_engine.hexdigest()
    
    # 一括で float32 変換と 44.1kHz リサンプリングを実行
    # (ディスクキャッシュ退避方式のため、ダウンサンプリング処理自体のRAM蓄積は44.1kHzへ変換後に行われます)
    chunk_float = pcm_bytes_to_float32(all_pcm, wFormatTag, bitsPerSample, numChannels)
    if sampleRate != 44100:
        audio_44100 = soxr.resample(chunk_float, sampleRate, 44100)
    else:
        audio_44100 = chunk_float
        
    return audio_44100, final_md5
