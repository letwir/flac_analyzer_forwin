import sqlite3

con = sqlite3.connect('orchestrator/orchestrator.db')
cur = con.cursor()
print("Table summary:")
try:
    rows = cur.execute('SELECT status, count(*) FROM task_state GROUP BY status').fetchall()
    print(rows)
except Exception as e:
    print("Error querying task_state:", e)

try:
    running = cur.execute("SELECT file_path, track_number, status, updated_at FROM task_state WHERE status='RUNNING'").fetchall()
    print("Running tasks:", len(running))
    for r in running:
        print(" ", r)
except Exception as e:
    print("Error querying running:", e)

try:
    recent = cur.execute("SELECT file_path, track_number, status, updated_at, error_message FROM task_state ORDER BY updated_at DESC LIMIT 10").fetchall()
    print("Recent tasks:")
    for r in recent:
        print(" ", r)
except Exception as e:
    print("Error querying recent:", e)
