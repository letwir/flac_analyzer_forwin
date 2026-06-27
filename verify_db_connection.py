import os

import psycopg2


def check_connection():
    candidate_dbs = ["postgres", "library_flac", "library_flac_historty"]
    db_uri_env = os.getenv("INGESTER_DATABASE_URL")
    print(db_uri_env)

    def decode_error(e):
        try:
            if isinstance(e, UnicodeDecodeError):
                # デコードに失敗した元のバイト列を直接 cp932 でデコードしますわ！
                return e.object.decode("cp932", errors="replace")

            pgerror = getattr(e, "pgerror", None)
            if pgerror:
                if isinstance(pgerror, bytes):
                    return pgerror.decode("cp932", errors="replace")
                else:
                    return pgerror.encode("latin1", errors="ignore").decode(
                        "cp932", errors="replace"
                    )

            if e.args:
                val = e.args[0]
                if isinstance(val, bytes):
                    return val.decode("cp932", errors="replace")
                else:
                    return (
                        str(val)
                        .encode("latin-1", errors="ignore")
                        .decode("cp932", errors="replace")
                    )
            return repr(e)
        except Exception as ex:
            return f"{repr(e)} (デコード失敗: {repr(ex)})"

    if db_uri_env:
        print(f"環境変数から PostgreSQL への接続を試みますわ... (URI: {db_uri_env})")
        try:
            conn = psycopg2.connect(db_uri_env)
            cursor = conn.cursor()
            cursor.execute("SELECT version(), current_user, current_database();")
            version, user, db = cursor.fetchone()
            print("\n[SUCCESS] 接続成功いたしましたわ！")
            print(f"  PostgreSQL バージョン: {version}")
            print(f"  現在のユーザー: {user}")
            print(f"  現在のデータベース: {db}")
            cursor.close()
            conn.close()
            return
        except Exception as e:
            print(f"[FAILURE] 環境変数での接続失敗いたしましたわ: {decode_error(e)}")
            print("続いてローカル設定でのフォールバック試行を行いますわよ！\n")

    # ローカルフォールバック接続
    success = False
    for db_name in candidate_dbs:
        uri = f"postgresql://ingester:ingester_8852@127.0.0.1:5432/{db_name}"
        print(f"試行中: {uri}")
        try:
            conn = psycopg2.connect(uri)
            cursor = conn.cursor()
            cursor.execute("SELECT version(), current_user, current_database();")
            version, user, db = cursor.fetchone()
            print("\n[SUCCESS] 接続成功いたしましたわ！")
            print(f"  PostgreSQL バージョン: {version}")
            print(f"  現在のユーザー: {user}")
            print(f"  現在のデータベース: {db}")
            cursor.close()
            conn.close()
            success = True
            break
        except Exception as e:
            print(f"  -> 失敗いたしましたわ: {decode_error(e)}\n")

    if not success:
        print(
            "[FAILURE] すべての接続試行に失敗いたしましたわ。ホストの起動状況やポート(5432)をご確認くださいませ。"
        )


if __name__ == "__main__":
    check_connection()
