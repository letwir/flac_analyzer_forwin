import sys
import re

with open("pipeline.py", "r", encoding="utf-8") as f:
    content = f.read()

# 1. Remove dummy import at the top
content = re.sub(r"from db import insert_to_postgres_dummy\n", "", content)

# 2. Add SafeAudioJSONEncoder and imports at the top
encoder_code = """import json
import math

class SafeAudioJSONEncoder(json.JSONEncoder):
    def default(self, o):
        try:
            import numpy as np
            if isinstance(o, (np.floating, np.integer)):
                return o.item()
            if isinstance(o, np.ndarray):
                return o.tolist()
        except ImportError:
            pass
        return super().default(o)

    def iterencode(self, o, _one_shot=False):
        try:
            import numpy as np
            has_numpy = True
        except ImportError:
            has_numpy = False

        def sanitize(obj):
            if isinstance(obj, dict):
                return {k: sanitize(v) for k, v in obj.items()}
            elif isinstance(obj, (list, tuple)):
                return [sanitize(x) for x in obj]
            elif has_numpy and isinstance(obj, (float, np.floating)):
                if np.isinf(obj) or np.isnan(obj):
                    return None
                return float(obj)
            elif not has_numpy and isinstance(obj, float):
                if math.isinf(obj) or math.isnan(obj):
                    return None
                return obj
            elif has_numpy and isinstance(obj, (int, np.integer)):
                return int(obj)
            elif has_numpy and isinstance(obj, np.ndarray):
                return [sanitize(x) for x in obj.tolist()]
            return obj

        return super().iterencode(sanitize(o), _one_shot=_one_shot)

"""
if "class SafeAudioJSONEncoder" not in content:
    content = content.replace("from concurrent.futures import ProcessPoolExecutor, as_completed\n", "from concurrent.futures import ProcessPoolExecutor, as_completed\n" + encoder_code)

# 3. process_flac_file modifications
# remove db imports
content = re.sub(r"\s+import psycopg2\s+from db import upsert_flac", "", content)

# replace the DB insert part in process_flac_file
db_insert_pattern = re.compile(
    r"\s+# 3\. DBインサート.*?conn\.commit\(\).*?t_db:\.4f}s\)\n\s+\)",
    re.DOTALL
)

# wait, we can just replace upsert_flac in pipeline.py universally.
def replacer(match):
    return """
            # 3. 解析結果を JSON Lines として標準出力へ
            features_payload = {}
            mix_feat = track_features.get("mix")
            if mix_feat:
                dict_mix = mix_feat.to_postgres_dict(track_id="mix")
                features_payload["mix"] = {
                    "scalars": dict_mix["scalars"],
                    "sequences": dict_mix["sequences"]
                }
            if demucs_feats:
                features_payload["demucs"] = demucs_feats.to_postgres_dict()

            predictions_payload = {}
            if essentia_feats:
                predictions_payload = essentia_feats.to_postgres_dict()

            output_data = {
                "audio_hash": mix_hash,
                "filepath": fp,
                "track_number": track_number,
                "metadata_tags": metadata_tags,
                "features": features_payload,
                "predictions": predictions_payload
            }
            print(json.dumps(output_data, ensure_ascii=False, cls=SafeAudioJSONEncoder))
            sys.stdout.flush()
"""

# replace upsert_flac block in process_flac_file
content = re.sub(
    r"\s+# 3\. DBインサート.*?upsert_flac\([^)]+\)\n\s+conn\.commit\(\).*?t_db:\.4f}s\)\n\s+\)",
    replacer,
    content,
    flags=re.DOTALL
)

# 4. In process_single_flac_file_directly
# remove def process_single_flac_file_directly( ..., dsn: str | None = None, resume: bool = False, rough: bool = False, ...
content = re.sub(
    r"def process_single_flac_file_directly\(\n    filepath: str,\n    essentia_models: dict,\n    dsn: str \| None = None,\n    resume: bool = False,\n    rough: bool = False,\n    use_dml: bool = False,\n\) -> str:",
    "def process_single_flac_file_directly(\n    filepath: str,\n    essentia_models: dict,\n    use_dml: bool = False,\n) -> str:",
    content
)

# remove import psycopg2 and db in process_single_flac_file_directly
content = re.sub(
    r"\s+import psycopg2\s+from db import upsert_flac",
    "",
    content
)

# remove resume and rough logic
content = re.sub(
    r"\s+# 1\. 重複スキップ用のデータ取得 \(Roughモード用\)\n.*?# 2\. FLAC ハンドルの構築",
    "\n    # 2. FLAC ハンドルの構築",
    content,
    flags=re.DOTALL
)

# remove rough and skip_tracks references
content = re.sub(
    r"\s+# Rough スキップ判定\n\s+if rough and num in skip_tracks:.*?(?=# 部分デコード)",
    "",
    content,
    flags=re.DOTALL
)
content = re.sub(
    r"\s+# 通常モードのハッシュ重複チェック\n\s+if not rough and md5_hash in skip_hashes:.*?(?=# スライスが短すぎる場合)",
    "",
    content,
    flags=re.DOTALL
)

# remove db connection
content = re.sub(
    r"\s+# DB 接続\n\s+conn = None\n\s+if dsn:\n\s+try:\n\s+conn = psycopg2\.connect\(dsn\)\n\s+conn\.autocommit = False\n\s+except Exception as e_db:\n\s+logging\.error\(f\"\[Direct-Process\] DB接続失敗: \{e_db\}\"\)\n\s+return f\"NG: DB connection error: \{e_db\}\"\n",
    "",
    content
)

# replace upsert_flac in process_single_flac_file_directly
def replacer_direct(match):
    return """
            # DB 書き込みの代わりに JSON Lines で stdout へ出力しますわ
            features_payload = {}
            mix_feat = track_features.get("mix")
            if mix_feat:
                dict_mix = mix_feat.to_postgres_dict(track_id="mix")
                features_payload["mix"] = {
                    "scalars": dict_mix["scalars"],
                    "sequences": dict_mix["sequences"]
                }
            if demucs_feats:
                features_payload["demucs"] = demucs_feats.to_postgres_dict()

            predictions_payload = {}
            if essentia_feats:
                predictions_payload = essentia_feats.to_postgres_dict()

            output_data = {
                "audio_hash": mix_hash,
                "filepath": filepath_abs,
                "track_number": target_track_num,
                "metadata_tags": track_meta,
                "features": features_payload,
                "predictions": predictions_payload
            }
            print(json.dumps(output_data, ensure_ascii=False, cls=SafeAudioJSONEncoder))
            sys.stdout.flush()
"""

content = re.sub(
    r"\s+# DB 書き込み\n\s+if conn:.*?conn\.commit\(\).*?t_db:\.4f}s\)\"\n\s+\)\n\s+else:\n\s+insert_to_postgres_dummy\([^)]+\)",
    replacer_direct,
    content,
    flags=re.DOTALL
)

with open("pipeline.py", "w", encoding="utf-8") as f:
    f.write(content)
print("pipeline.py refactored")

# Refactor main.py
with open("main.py", "r", encoding="utf-8") as f:
    main_content = f.read()

# remove INGESTER_DATABASE_URL
main_content = re.sub(
    r'os\.environ\["INGESTER_DATABASE_URL"\] = \(\n\s+"postgres://.*?"\n\s+\)',
    '',
    main_content
)
# remove --dsn argument
main_content = re.sub(
    r'\s+p\.add_argument\(\n\s+"--dsn".*?\)',
    '',
    main_content,
    flags=re.DOTALL
)
# remove --resume argument
main_content = re.sub(
    r'\s+p\.add_argument\(\n\s+"--resume".*?\)',
    '',
    main_content,
    flags=re.DOTALL
)
# remove --rough argument
main_content = re.sub(
    r'\s+p\.add_argument\(\n\s+"--rough".*?\)',
    '',
    main_content,
    flags=re.DOTALL
)

main_content = re.sub(
    r'\s+if not args\.dsn:\n\s+logging\.error\("--dsn .*?"\)\n\s+sys\.exit\(1\)',
    '',
    main_content
)

main_content = re.sub(
    r'dsn=args\.dsn,\n\s+resume=args\.resume,\n\s+rough=args\.rough,\n\s+',
    '',
    main_content
)

with open("main.py", "w", encoding="utf-8") as f:
    f.write(main_content)
print("main.py refactored")
