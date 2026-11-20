---
name: land
description: >-
  asagao の作業ブランチ (.worktrees/<branch>) を scripts/land.sh で main に --no-ff で取り込み、build/vet/test -race で検証して片付ける。ユーザーが PR 作成を依頼したときに push・PR 作成・CI 待ちを行う。「main に取り込んで」「PR 作って」のときに使う。
---

# land

取り込み・PR 作成には必ず `scripts/land.sh` を使う（手動でコマンドを 1 つずつ打たない）。

## 取り込み (merge)

前提: 対象ブランチが `asagao-reviewer` の承認を得ており、`asagao-tester` の確認および関連パッケージの `go test -race` も通過していること（全パッケージの `-race` は `scripts/land.sh merge` がまとめて実行する）。

```sh
LAND_TRAILERS='Co-Authored-By: Gemini (Antigravity) <noreply@google.com>' scripts/land.sh merge <branch> [<branch>...]
```

- `LAND_TRAILERS` に `Co-Authored-By: Gemini (Antigravity) <noreply@google.com>` を渡し、マージコミットにも Gemini 経由であることを残す。
- マイグレーションを含むブランチがある場合は、番号がぶつからない順番で取り込む。
- 途中でコンフリクトした場合はスクリプトが止まる。解決してコミットしたら、残りのブランチで再実行する。
- `scripts/land.sh merge` はローカル `main` へのマージ・検証・worktree/ブランチ削除まで行い、`push` はしない。

## PR 作成 (ユーザーに頼まれたときだけ)

1. PR 本文ファイルをスクラッチディレクトリに書く:
   - Issue ごとに 1 行の `Closes #n: 何をどう直したか`
   - 別 Issue として切り出した残課題
   - テスト結果 (`go build`, `go vet`, `go test -race`, `spec:validate`)
   - 末尾に署名フッター: `🤖 Generated with Gemini (Antigravity)`
2. 実行する:
   ```sh
   scripts/land.sh pr <新しいブランチ名> "<英語のタイトル>" <本文のファイル>
   ```
3. 表示された PR URL と CI の結果を、ユーザーに 3 行以内で伝える。

ローカルの `main` は戻さない (`reset --hard` は使わない)。GitHub 上でマージしたあとに `git pull --ff-only` で揃える。
