# キャッシュにないタスク・ユーザー・タグを Asana で探す (Issue #36)

振る舞いを足す変更。

## なぜ

検索はキャッシュの中だけなので、同期していないプロジェクトのタスクや古い完了タスクは見つからない。クイック追加の `@担当` `#タグ` や `task assign` も、キャッシュにないユーザーやタグは名前から引けない。git 連携 (#8) でタスクの ID (custom ID、`ENG-123`) を書いても、タスクに結び付けられない。

## 何を変えるか

- asana: 検索 API (`GET /workspaces/{gid}/tasks/search`)、typeahead (`GET /workspaces/{gid}/typeahead`、task・user・tag)、custom ID でのタスクの取得 (`GET /workspaces/{gid}/tasks/custom_id/{id}`) を足す。
- sync: `Syncer.SearchTasks` は検索 API を呼び、402 なら `plan_unavailable:search` を覚えて、それからは typeahead (タスク名の先頭) を使う。typeahead も 402 なら `plan_unavailable:typeahead` を覚えて、Asana では探さない。見つかったタスクは `TasksByGID` で全部の欄を読んでからキャッシュに入れる (名前だけの記録でキャッシュを上書きしない)。`FindUsers` / `FindTags` は typeahead で探し、キャッシュにない人・タグだけを入れる (あるものは変えない。消して未送信のタグは消えたまま)。`TaskByCustomID` はタスクをキャッシュに入れ、gid を `custom_id:<ID>` に覚える。
- app: `SearchWithAsana` は、キャッシュの結果のあとに Asana で見つかったタスクを足す (ほかの絞り込みは両方に効く)。Asana に届かなければキャッシュの結果と理由を返す。クイック追加 (`ReadQuickAddFinding`) と担当の解決 (`Tasks.Names`) は、キャッシュに一致するものが**ない**ときだけ Asana に名前で聞き、見つかったら読み直す。git 連携は `asana#<custom ID>` の参照を、Asana で引いた gid (届かなければ前に覚えた gid) で扱う。
- store: `TaskFilter.GIDs`、`AddUsers`、`AddTags`。マイグレーションはない (覚えることは sync_state に入れる)。
- 入り口: CLI の `search --remote`、TUI の `space g` (`/` で語を入れてから)、MCP の `search_tasks` の `remote`。CLI の `task assign` / `task create` (`--quick` を含む)、TUI のクイック追加、MCP の `create_task` / `update_task` の担当で名前を Asana にも聞く。

## やらないこと

- 検索 API のページ送り (1 回で最大 100 件)。
- プロジェクトの検索 (`/projects/search`)。名前でのプロジェクトの解決も変えない。
- `asana#` のない `ENG-123` は読まない (`UTF-8` などと取り違えるため)。
- 402 の記録は、有料プランに変えても自動では消さない (いまの `plan_unavailable:` と同じ。`asagao reset` で消える)。
- 見つけたタスクを同期の対象にはしない (同期していないプロジェクトのタスクは、次の全件同期で消える)。
