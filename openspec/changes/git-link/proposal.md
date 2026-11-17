# git 連携 (Issue #8)

振る舞いを足す変更。

## なぜ

コミットしたことを Asana のタスクに残し、直したタスクは完了にしたい。いまは手で `comment add` と `task complete` を打つしかない。

## 何を変えるか

- CLI に `git scan [<コミット>|<範囲>...]` を足す。コミットメッセージのタスク参照 (`#<10 桁以上の数字>`、`asana#<数字>`) のタスクにコメントし、GitHub の閉じるキーワード (`fixes` など) が同じ行で直前にある参照のタスクは完了にする。`Revert "…"` のコミットは完了にしない。
- 同じコミットを 2 回処理しても何も足さない。コメントの末尾の `asagao-git: <コミットの ID>` で見分ける (マイグレーションはない)。オンラインなら、判断の前にタスクを Asana から取り直す。
- 書き込みは今までどおり outbox を通す。オンラインならすぐ送り、`--offline` なら積むだけ。`--dry-run` は何もしない。
- CLI に `git install-hook` / `git uninstall-hook` を足す。post-commit hook を入れる / 外す。ほかの hook は上書きも削除もしない。hook は `git commit` で作られたコミットだけを読み、rebase・cherry-pick・revert のコミットは飛ばす。amend は amend 前になかった参照だけを扱う。

## やらないこと

- Asana のタスクの URL は読まない。
- `fixes #A, #B` の B は完了にしない (GitHub と同じ)。
- 別のコンピューターで、まだ送っていないコメントは見えない (Asana に届いていれば取り直しで見える)。
