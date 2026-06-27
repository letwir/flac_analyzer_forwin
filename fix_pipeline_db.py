import sys
import re

with open("pipeline.py", "r", encoding="utf-8") as f:
    content = f.read()

# Replace run_producer DB connection
content = re.sub(
    r"\s+if resume and dsn:\n\s+try:\n\s+import psycopg2\n\s+conn = psycopg2\.connect\(dsn\).*?\n\s+conn\.close\(\)\n\s+except Exception as e:\n\s+logging\.warning.*?\)\]",
    "\n    # Producer の DB スキップチェックは排除（Go側で行う想定ですわ）",
    content,
    flags=re.DOTALL
)

# Remove psycopg2 and upsert_flac from run_consumer
content = re.sub(
    r"\s+import psycopg2\n\s+import time\n\s+from db import upsert_flac",
    "\n    import time",
    content
)

content = re.sub(
    r"\s+conn = psycopg2\.connect\(dsn\)\n\s+conn\.autocommit = False",
    "",
    content
)

def replacer_consumer(match):
    return """
            # 3. 解析結果を JSON Lines として標準出力へダンプいたしますわ
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
            import json
            import sys
            print(json.dumps(output_data, ensure_ascii=False, cls=SafeAudioJSONEncoder))
            sys.stdout.flush()
"""

# Replace upsert_flac in run_consumer
content = re.sub(
    r"\s+# 3\. DBインサート.*?upsert_flac\([^)]+\)\n\s+conn\.commit\(\).*?t_db:\.4f}s\)\"\n\s+\)",
    replacer_consumer,
    content,
    flags=re.DOTALL
)

# Remove conn.rollback() in run_consumer
content = re.sub(
    r"\s+conn\.rollback\(\)\s+# エラー時はロールバック",
    "",
    content
)

# Verify if verify_db_connection.py uses psycopg2 (it does, but that's a standalone test script)
# Let's completely remove db.py and verify_db_connection.py since we don't need them.
# We will do this via git rm later.

with open("pipeline.py", "w", encoding="utf-8") as f:
    f.write(content)
print("run_consumer and run_producer DB removed")
