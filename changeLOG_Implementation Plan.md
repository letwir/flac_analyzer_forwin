# .gitignore 更新および search/ ディレクトリ履歴削除計画

## 概要
`.gitignore` に `demucs/` および `search/` を追加し、`git-filter-repo` によって過去のコミット履歴からも `search/` ディレクトリを完全削除します。

## 変更内容
1. `.gitignore`: `demucs/` および `search/` を除外リストへ追加。
2. Git 履歴: `git-filter-repo --path search --invert-paths --force` の実行。
