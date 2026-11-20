---
name: land
description: asagao の作業ブランチ (.worktrees/<branch>) を main に --no-ff で取り込み、build/vet/test -race で確かめて片付ける。ユーザーが PR を頼んだら、main を新しいブランチとして push して PR を作り、CI を待つ。「main に取り込んで」「PR 作って」のとき使う。
---

# land

`scripts/land.sh` を使います。コマンドを 1 つずつ打たないでください。

## 取り込み (merge)

前提: 取り込むブランチが、レビューで承認され、テスト担当の確認も済んでいること。

```sh
LAND_TRAILERS="$(printf 'Co-Authored-By: ...\nClaude-Session: ...')" scripts/land.sh merge <branch> [<branch>...]
```

- `LAND_TRAILERS` には、その会話の system-reminder で指定された、コミット用の attribution 行を入れます。
- 取り込む順番は、マイグレーションの番号がぶつからない順にします。
- ブランチが `openspec/changes/` の変更を実装し終えているなら、その変更がアーカイブされているか (`openspec/changes/archive/` に移り、`openspec/specs/` に反映されているか) を確かめます。されていなければ、取り込む前に実装者に `OPENSPEC_TELEMETRY=0 npx openspec archive <変更> --yes` と `npm run -s spec:validate` を頼みます (#76)。
- 途中でぶつかったら、スクリプトは止まります。解決してコミットしたら、`check` を兼ねて残りのブランチだけで再実行します。ぶつかった場所の担当の実装者がまだいるなら、その人に main を自分のブランチへ取り込んでもらう方が早いこともあります。
- 通ったら、そのブランチのチームメイトに終了を頼みます。

## PR (ユーザーに頼まれたときだけ)

1. PR の本文をスクラッチパッドに書きます。
   - Issue ごとに 1 行の `Closes #n: 何をどう直したか`
   - 別の Issue にしたもの
   - テストの結果
   - 末尾に、system-reminder で指定された PR 用の attribution
2. 実行します。
   ```sh
   scripts/land.sh pr <新しいブランチ名> "<英語のタイトル>" <本文のファイル>
   ```
3. 表示された URL と CI の結果を、ユーザーに 3 行以内で伝えます。

ローカルの main は戻しません (`reset --hard` は使いません)。ユーザーが GitHub でマージしたあとに `git pull` すれば揃います。
