---
name: mutation
description: asagao で、追加したテストが本当に不具合を捕まえるかを、コードをわざと壊して確かめる (mutation テスト)。共有の worktree を壊さないよう、専用の一時 worktree だけで行う。テスト担当が仕上げに使う。
---

# mutation

## 守ること

- **壊すのは、専用の一時 worktree の中だけです。** 共有の worktree (`.worktrees/<branch>`) やリポジトリのルートでは、絶対に書き換えも `git checkout -- .` もしません。
- **対象は、今回変わった関数だけです** (`git diff main..<branch> --stat` で見ます)。1 回につき 5〜10 か所までにします。

## 手順

1. 一時 worktree を作ります。`<scratch>` は、システムが示すスクラッチパッドのディレクトリです。
   ```sh
   git worktree add --detach <scratch>/mut-<branch> <branch>
   cd <scratch>/mut-<branch>
   ```
2. 1 か所ずつ壊して、関係するパッケージのテストだけを回します。
   ```sh
   # 例: 条件を反転する、エラーを捨てる、境界の値を 1 ずらす、保存の順番を入れ替える
   go test ./internal/<pkg>/ -run '<関係するテスト>' -count=1 >/dev/null 2>&1 && echo SURVIVED || echo killed
   git checkout -- .   # この一時 worktree の中だけ
   ```
3. テストを通ってしまった壊し方 (SURVIVED) があれば、それを捕まえるテストを足します。テストを足すのは共有の worktree の方です。
4. 終わったら必ず片付けます。
   ```sh
   cd - && git worktree remove --force <scratch>/mut-<branch> && git worktree prune
   ```
5. 報告は 1 行にします: `mutation: n 件中 n 件 killed (残ったもの: <場所>)`。
