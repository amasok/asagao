---
name: mutation
description: >-
  asagao で追加したテストが本当に不具合を捕まえるかを、コードを意図的に壊して確かめる (mutation テスト)。共有の worktree を壊さないよう、専用の一時 worktree (またはコピー) の中だけで行う。テスト担当が仕上げに使う。
---

# mutation

## 守ること

- **壊すのは専用の一時 worktree の中だけ。** 共有の worktree (`.worktrees/<branch>`) やリポジトリのルートでは、絶対に書き換えも `git checkout -- .` もしない。
- **対象は今回変わった関数だけ** (`git diff main..<branch> --stat` で絞る)。1 回につき 5〜10 か所までにする。

## 手順

1. 一時 worktree を作る (`<scratch>` はスクラッチディレクトリ):
   ```sh
   git worktree add --detach <scratch>/mut-<branch> <branch>
   cd <scratch>/mut-<branch>
   ```
2. 1 か所ずつ壊して、関係するパッケージのテストだけを回す:
   ```sh
   # 例: 条件反転、エラー無視、境界値 ±1、保存順の入れ替え
   go test ./internal/<pkg>/ -run '<関係するテスト>' -count=1 >/dev/null 2>&1 && echo SURVIVED || echo killed
   git checkout -- .   # この一時 worktree の中だけ
   ```
3. テストを通ってしまった壊し方 (`SURVIVED`) があれば、それを捕まえるテストを共有 worktree 側 (`.worktrees/<branch>`) に追加する。
4. 終わったら必ず一時 worktree を削除する:
   ```sh
   cd - && git worktree remove --force <scratch>/mut-<branch> && git worktree prune
   ```
5. 報告は 1 行にする: `mutation: n 件中 n 件 killed (残ったもの: <場所>)`。
