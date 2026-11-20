# プロジェクトのタスクは開いたときに取る・未完了を先に取る (Issue #54)

振る舞いを変える変更。

## なぜ

同期は、参加しているプロジェクト (`all_projects` なら全部) ごとに、セクション・ステータス・タスク (未完了と `completed_days` 日以内の完了)・サブタスクを取っていた。プロジェクトが多いと、最初の同期と Events のトークンが切れたときに時間がかかり、無料プランの 150 回/分にも当たりやすい。完了を消さない使い方では、完了済みが多くて重い。

## 何を変えるか

- 設定: `sync.lazy_projects` (既定 true) を足す。false なら同期は前のまま (参加しているプロジェクトを最初から、完了済みも同時に取る)。`all_projects = true` は、どちらでも全部のプロジェクトを最初から取る。`completed_days` が負なら 0 として扱う。
- sync: 同期はプロジェクトの一覧を全部取るが、タスクを取るのは「同期するプロジェクト」だけにする。同期するプロジェクトは、一度開いたもの (`project_tasks:<gid>`)、お気に入り、前の版で取っていたもの (Events のトークンがある)、`all_projects` なら全部、`lazy_projects = false` なら参加しているもの。Events はこのプロジェクトのものだけを読む。マイタスクは、プロジェクトに関係なくいつも取る。
- sync: 初めて取るもの (プロジェクトとマイタスク) は、未完了のタスクだけを先に取り (`completed_since` = 今)、全部の未完了が済んでから、完了済み (`completed_since` と `modified_since` = `completed_days` 日前) を取る。取れたところまでを `project_tasks:<gid>` / `my_tasks:<workspace>` に `open` / `all` として覚える。
- sync: `Syncer.OpenProject` (プロジェクトを開いたとき。記録して、未取得ならその場で取る)、`Syncer.FetchCompleted` (`DeferCompleted` の同期が残した完了済みを取る)。どちらも取り込むだけで、push のロックを取る (ほかの同期の最中は ErrPushBusy、記録は残るので次の同期で取る)。
- sync: `Result` に `listed` (一覧だけのプロジェクトの数)、`completed_pending`、`requests` (Asana へのリクエスト数) を足す。asana: `Client.Requests`。
- store: 取得の状態の読み書き (`ProjectTaskStates`、`WantProject`、`CountUnfetchedProjects` など)。`ProjectView.unfetched`。状態のキーはプロジェクト・ワークスペースと一緒に消す。マイグレーションはない (sync_state に入れる)。
- TUI: 未取得のプロジェクトをサイドバーで `◌` で示し、一覧の頭に注記を出す。カーソルが 0.4 秒とどまったら取る (未完了を出してから完了済み)。通り過ぎるだけでは取らない。同期は完了済みをあとに回し (`DeferCompleted`)、未完了を出してから `FetchCompleted` で取る。オフラインなら記録だけして、次の同期で取る。
- CLI: `task list --project` と `search --project` は未取得ならその場で取る。Asana に届かない・`--offline` なら標準エラーに「まだ取得していません」と出す。`project list` に `[タスク未取得]`、`search` (プロジェクト指定なし) に未取得のプロジェクトの数、`sync` にリクエスト数を出す。
- MCP: `list_tasks` と `search_tasks` はプロジェクトの指定があれば同じように取り、取れなければ `note` に書く。`list_projects` に `unfetched`、`search_tasks` の `note` に未取得のプロジェクトの数。

## やらないこと

- 長く開いていないプロジェクトを同期の対象から外すこと。一度開いたプロジェクトは、Asana から消えるまで同期する。
- 開いたプロジェクトを同期の対象から外す操作。
- 完了済みだけを取る問い合わせ (GET /tasks にない)。代わりに `modified_since` で、変わっていない未完了を送り直さない。
- 検索で未取得のプロジェクトを取りにいくこと (数を添えるだけ。Asana で探すのは `--remote`)。
- TUI の受信トレイ・ゴールから開いたときの「同期していません」の扱い (サイドバーにない、参加していないプロジェクト) は変えない。
