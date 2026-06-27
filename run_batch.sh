#!/usr/bin/env bash
# ==============================================================================
# FLAC Analyzer 用の Bash 順次実行バッチスクリプトですわ！
# GENRE-SUB フォルダ単位で python main.py を都度プロセス起動し、RAM断片化を防ぎますの。
# ==============================================================================

set -euo pipefail

# デフォルト設定値
MUSIC_ROOT="M:/Music/album"
TEST=false
DRY_RUN=false
SKIP=false

# スクリプトの絶対パスを解決しますわ
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ヘルプ表示関数
usage() {
    echo "Usage: $0 [options]"
    echo "Options:"
    echo "  --music-root PATH   音楽ライブラリのルートパス（デフォルト: $MUSIC_ROOT）"
    echo "  --test              テスト用のダミー構成を作成して動作確認テストを行いますわ"
    echo "  --dry-run           コマンドを実行せずに、実行予定のコマンドを表示するだけにとどめますわ"
    echo "  --skip              処理完了済みのフォルダ（ログ判定）をスキップしますわ"
    echo "  -h, --help          このヘルプを表示しますわ"
}

# 引数解析
while [[ $# -gt 0 ]]; do
    case "$1" in
        --music-root)
            MUSIC_ROOT="$2"
            shift 2
            ;;
        --test)
            TEST=true
            shift
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --skip)
            SKIP=true
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "エラー: 未知のパラメータ '$1' が指定されましたわ" >&2
            usage
            exit 1
            ;;
    esac
done

export PYTHONUTF8=1

# テストモードのセットアップ
TEMP_ROOT=""
DUMMY_PYTHON_SCRIPT=""
if [ "$TEST" = true ]; then
    echo -e "\e[33m[Test Mode] 一時ディレクトリにダミー構成を作成してテストを行いますわ！\e[0m"
    TEMP_ROOT="/tmp/flac_analyzer_test_root"
    if [ -d "$TEMP_ROOT" ]; then
        rm -rf "$TEMP_ROOT"
    fi
    mkdir -p "$TEMP_ROOT"

    # ダミーの GENRE-MAIN / GENRE-SUB 構成作成
    # フォルダ1: FLACあり
    sub1="$TEMP_ROOT/J-POP/Artist-A"
    mkdir -p "$sub1"
    echo "dummy flac content" > "$sub1/track1.flac"

    # フォルダ2: FLACあり
    sub2="$TEMP_ROOT/Rock/Artist-B"
    mkdir -p "$sub2"
    echo "dummy flac content" > "$sub2/track2.flac"

    # フォルダ3: FLACなし (スキップ対象)
    sub3="$TEMP_ROOT/Anime/Artist-C"
    mkdir -p "$sub3"

    MUSIC_ROOT="$TEMP_ROOT"
    echo -e "\e[33m[Test Mode] テスト用ルートディレクトリ: $MUSIC_ROOT\e[0m"

    # テスト用のダミー Python ターゲット作成
    DUMMY_PYTHON_SCRIPT="$SCRIPT_DIR/dummy_target.py"
    cat << 'EOF' > "$DUMMY_PYTHON_SCRIPT"
import sys
print("--------------------------------------------------")
print("[Dummy Target] Pythonが正常に起動されましたわ！")
print(f"引数: {sys.argv[1:]}")
print("--------------------------------------------------")
sys.exit(0)
EOF
    TARGET_SCRIPT="$DUMMY_PYTHON_SCRIPT"
else
    TARGET_SCRIPT="$SCRIPT_DIR/main.py"
    if [ ! -f "$TARGET_SCRIPT" ]; then
        echo "エラー: main.py がスクリプトと同じディレクトリに見つかりませんわ: $SCRIPT_DIR" >&2
        exit 1
    fi
fi

# ディレクトリ存在チェック
if [ ! -d "$MUSIC_ROOT" ]; then
    echo "エラー: 音楽ルートディレクトリが見つかりませんわ: $MUSIC_ROOT" >&2
    exit 1
fi

echo -e "\e[32m=========================================\e[0m"
echo -e "\e[32m FLAC Analyzer Batch Run Starting...\e[0m"
echo -e "\e[32m ルート: $MUSIC_ROOT\e[0m"
echo -e "\e[32m ターゲット: $TARGET_SCRIPT\e[0m"
echo -e "\e[32m=========================================\e[0m"

processed_count=0
skipped_count=0

# 空白スペース対策のために、ワイルドカードとループ処理を適切に行いますわ
# 1層目: GENRE-MAIN を走査
for genre_main in "$MUSIC_ROOT"/*/; do
    # ディレクトリが存在しない場合（ワイルドカードがマッチしなかった場合）はスキップ
    [ -d "$genre_main" ] || continue
    
    # 2層目: GENRE-SUB を走査
    for genre_sub in "${genre_main}"*/; do
        [ -d "$genre_sub" ] || continue
        
        # 末尾スラッシュを除去したパス名を作成しますわ
        genre_sub_clean="${genre_sub%/}"
        
        # .flac ファイルが配下に再帰的に存在するかチェックしますの
        # find結果の改行コード数をカウントします
        flac_count=$(find "$genre_sub_clean" -type f -name "*.flac" | wc -l)
        if [ "$flac_count" -eq 0 ]; then
            echo -e "  \e[90m[-] スキップ (FLACなし): $genre_sub_clean\e[0m"
            skipped_count=$((skipped_count + 1))
            continue
        fi

        # 解析対象のフォルダ名から、プロジェクト直下に置くユニークなログファイル名を生成しますわ
        # 例: J-POP/Artist-A -> log_J-POP__Artist-A.log
        genre_sub_name=$(basename "$genre_sub_clean")
        genre_main_clean="${genre_main%/}"
        genre_main_name=$(basename "$genre_main_clean")
        log_file_name="log_${genre_main_name}__${genre_sub_name}.log"

        # 完了ログ検知によるスキップ機能
        if [ "$SKIP" = true ]; then
            log_file_path="$SCRIPT_DIR/$log_file_name"
            if [ -f "$log_file_path" ]; then
                # ログ内に完了識別文字列があるか確認しますわ
                if grep -q "全ファイル処理完了！" "$log_file_path"; then
                    echo -e "  \e[33m[-] スキップ (処理完了済み): $genre_sub_clean\e[0m"
                    skipped_count=$((skipped_count + 1))
                    continue
                fi
            fi
        fi

        processed_count=$((processed_count + 1))
        echo ""
        echo -e "\e[36m==================================================\e[0m"
        echo -e "\e[36m[$processed_count] 処理開始: $genre_sub_clean\e[0m"
        echo -e "\e[36m    FLACファイル数: $flac_count 個\e[0m"
        echo -e "\e[36m==================================================\e[0m"

        if [ "$DRY_RUN" = true ]; then
            echo -e "\e[37m[DryRun] 実行予定コマンド: python \"$TARGET_SCRIPT\" \"$genre_sub_clean\" --workers 1 --resume\e[0m"
            continue
        fi

        # プロセス起動 (python を呼び出し)
        set +e
        python "$TARGET_SCRIPT" "$genre_sub_clean" --workers 1 --resume
        exit_code=$?
        set -e

        if [ $exit_code -ne 0 ]; then
            echo -e "\e[31m⚠️ プロセスがエラー終了いたしましたわ (ExitCode: $exit_code)\e[0m"
        else
            echo -e "\e[32m🟢 正常完了いたしましたわ: $genre_sub_clean\e[0m"
        fi
    done
done

echo ""
echo -e "\e[32m=========================================\e[0m"
echo -e "\e[32m バッチ処理が終了いたしましたわ！\e[0m"
echo -e "\e[32m 処理完了: $processed_count 件\e[0m"
echo -e "\e[32m スキップ: $skipped_count 件\e[0m"
echo -e "\e[32m=========================================\e[0m"

# テストモードのクリーンアップ
if [ "$TEST" = true ]; then
    if [ -d "$TEMP_ROOT" ]; then
        rm -rf "$TEMP_ROOT"
    fi
    if [ -f "$DUMMY_PYTHON_SCRIPT" ]; then
        rm -f "$DUMMY_PYTHON_SCRIPT"
    fi
fi
