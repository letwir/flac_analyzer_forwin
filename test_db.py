import psycopg2
import json

conn = psycopg2.connect('postgres://ingester:ingester_8852@db.tigris-tailor.ts.net:5432/db')
cur = conn.cursor()
cur.execute('SELECT features FROM raw.library_flac LIMIT 1;')
res = cur.fetchone()
features = res[0]
print("Keys in features:", list(features.keys()))
if 'demucs' in features:
    print("Keys in demucs:", list(features['demucs'].keys()))
