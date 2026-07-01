import psycopg2
import json

conn = psycopg2.connect('postgres://ingester:ingester_8852@db.tigris-tailor.ts.net:5432/db')
cur = conn.cursor()
cur.execute("SELECT features FROM raw.library_flac WHERE audio_hash='fbb87ac6e19b1d795f4a6a7cc82cffe9'")
res = cur.fetchone()
if res:
    print('DB Keys:', list(res[0].keys()))
else:
    print('No Row')
