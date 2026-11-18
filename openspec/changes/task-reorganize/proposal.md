# タスクの削除・親の付け替え・並べ替え (Issue #32)

振る舞いを足す変更。

## なぜ

asagao からタスクを消せず、サブタスクを別の親に移したり普通のタスクをサブタスクにしたりもできない。セクションの中の並び順も変えられない (`move` はセクションを変えるだけ)。整理の操作のたびに Web に戻る必要がある。

## 何を変えるか

- outbox の操作の種類に `delete_task` と `set_parent` を足す。`move_task` の payload に、任意の `insert_before` / `insert_after` (同じセクションのタスク) を足す。
- 削除: 一度も送っていないタスクは、その場で作成ごと消す。送ったことのあるタスクは削除を積み、送るまではタスクとサブタスクを一覧 (ListTasks・サブタスクの一覧) から隠す。積んだ削除を取り消すと元に戻る (そのタスクに積んであった変更も残る)。送れたら、タスク・サブタスク・それらに積んであった変更をキャッシュから消す。`DELETE /tasks/{gid}` の 404 は「もう消えている」として済んだ扱いにする (送る前に `checkPushAccount` でアカウントを確かめている)。何度送っても結果は同じなので、5xx のあとは照合の決まりどおり送り直す。
- 親の付け替え: `POST /tasks/{gid}/setParent`。同じタスクの続けての付け替えは 1 つにまとめ、最初の親を base に持つ。元の親に戻したら操作を消す。自分自身や自分のサブタスクの下には移せない。取り込み (pull) で上書きされても、送るまでは積んだ親に戻す。
- 並べ替え: `POST /sections/{gid}/addTask` の `insert_before` / `insert_after`。位置があれば、同じセクションの中でも積む。ローカルのキャッシュはセクションの中の順番を持たないので、順番は Asana にだけ反映する。
- CLI: `task rm <gid>` (`--yes` がないと消えるものを出して止まる。`--dry-run` に対応)、`task parent <gid> (<親> | --none) [--before|--after]`、`task move <gid> [--section] --before|--after <gid>`。
- MCP: `delete_task` (`dry_run` で消えるものだけを返す)、`set_parent`、`move_task` の `insert_before` / `insert_after`。
- TUI: `space K` で削除 (確認してから積む)、`space p` で親の付け替え (同じプロジェクトの未完了タスクから選ぶ・親から外す)。

## やらないこと

- 依存先 (dependents) の追加・削除は新しい種類を作らない。`add_dependency` / `remove_dependency` をタスクを入れ替えて使えば同じ関係になり、CLI の `task depend --add-dependent` と MCP の `update_task.add_dependents` で既にできる。
- TUI でのセクション内の並べ替え。TUI はセクションの中の順番を表示していない (期日順) ため。
- TUI の親の候補に検索はない (同じプロジェクト、またはプロジェクトがなければ自分の未完了タスクから選ぶ)。
- 削除したタスクを Asana のごみ箱から戻す操作。
