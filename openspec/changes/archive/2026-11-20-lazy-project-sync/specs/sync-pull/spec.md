## ADDED Requirements

### Requirement: タスクを取るのは同期するプロジェクトだけ
同期はワークスペースのプロジェクトの一覧を全部取り込むが、セクション・ステータス・タスク・Events を取るのは同期するプロジェクトだけにする (MUST)。同期するプロジェクトは、一度開いたもの、お気に入り、前の版で取っていたもの (Events のトークンがある)、`all_projects = true` なら全部、`lazy_projects = false` なら参加しているもの。マイタスク (自分の担当) は、どのプロジェクトのタスクでもいつも取る。
根拠: sync/lazy.go (toSync, pullMyTasks); sync/sync.go (run); TestLazySyncFetchesProjectListOnly, TestLazyStoreSyncedBefore, TestLazyOff

#### Scenario: 最初の同期
- **WHEN** `lazy_projects` が既定のまま、参加しているプロジェクト p1 (お気に入りでない) と、お気に入りの p2 がある状態で初めて同期する
- **THEN** p2 のタスクとマイタスクを取り、p1 には 1 回もリクエストしない (Events も読まない)。p1 は一覧に「未取得」として出る

#### Scenario: 前の版で同期していたストア
- **WHEN** 前の版で同期した (Events のトークンがある) プロジェクトがあるストアで同期する
- **THEN** そのプロジェクトは Events の差分で今までどおり取り込み、初めからは取り直さない

### Requirement: 初めて取るものは未完了を先に取る
`lazy_projects` のとき、初めて取るプロジェクトとマイタスクは、まず未完了のタスクだけを取る (`completed_since` = 同期を始めた時刻) (MUST)。全部の未完了とそのサブタスクを取り込んでから、完了済み (`completed_since` と `modified_since` = `completed_days` 日前) を取る。完了済みで新しく取るサブタスクは、完了したタスクのものだけ。
根拠: sync/lazy.go (pullProject, pullMyTasks, pullCompleted); TestLazyFetchesIncompleteFirst

#### Scenario: お気に入りを初めて取る
- **WHEN** 初めての同期でお気に入りの p2 を取る
- **THEN** マイタスクと p2 の未完了を取る問い合わせが全部済んでから、完了済みを取る問い合わせが 2 回 (マイタスクと p2) 出る

### Requirement: どこまで取れたかを覚える
取れたところまでを `project_tasks:<gid>` / `my_tasks:<workspace gid>` に `open` (未完了まで) / `all` として覚える (MUST)。`open` のものは、次の同期か `FetchCompleted` で完了済みを取る。`DeferCompleted` の同期 (TUI) は完了済みを取らずに `completed_pending` を返す。`completed_days` が 0 (負は 0) なら完了済みは取らず、すぐ `all` にする。
根拠: sync/lazy.go (finishPull, completedPending, FetchCompleted); TestLazyDeferCompleted, TestLazyCompletedDaysZero, TestTUIFetchesCompletedAfterSync

#### Scenario: TUI の最初の同期
- **WHEN** `DeferCompleted` で初めて同期する
- **THEN** 未完了だけを取って `completed_pending` を返し、続く `FetchCompleted` で完了済みを取る

### Requirement: 開いたときに取る
プロジェクトを開いたとき (TUI でカーソルがとどまる、CLI・MCP でタスクの一覧や検索にプロジェクトを指定する)、それを同期するプロジェクトとして記録し、タスクが未取得ならその場で取る (MUST)。取り込むだけで、未送信の変更は送らず、上書きもしない。TUI でカーソルが通り過ぎただけのプロジェクトは取らない。
根拠: sync/lazy.go (OpenProject); TestOpenProjectFetchesAndTracks, TestOpenProjectWithCompleted, TestTaskListFetchesUnfetchedProject, TestListTasksFetchesUnfetchedProject, TestTUIOpenProjectFetchesIncompleteThenCompleted, TestTUIPassingByFetchesNothing

#### Scenario: CLI で未取得のプロジェクトを一覧する
- **WHEN** オンラインで `asagao task list --project 仕事` を実行し、仕事のタスクがまだキャッシュにない
- **THEN** 仕事のタスクを Asana から取ってから一覧を出す。もう一度実行しても Asana には聞かない

### Requirement: 開いても取れないとき
開いたプロジェクトを取れないとき (オフライン、Asana に届かない、`--offline`、ほかの同期の最中で ErrPushBusy) は、記録だけを残して次の同期で取る (MUST)。CLI は標準エラー、MCP は `note`、TUI は一覧の注記で「まだ取得していません」と伝える。
根拠: sync/lazy.go (OpenProject); TestOpenProjectWhileBusy, TestTaskListUnfetchedProjectOffline, TestListTasksUnfetchedProjectOffline, TestTUIUnfetchedProjectOffline

#### Scenario: オフラインで開く
- **WHEN** `asagao task list --project 仕事 --offline` を実行する
- **THEN** Asana には聞かず、標準エラーに「まだ取得していません…次の同期で取得します」と出し、次の同期で仕事のタスクを取る

### Requirement: 未取得のプロジェクトを見せる
タスクが未取得のプロジェクトは、TUI のサイドバーで `◌`、`project list` で `[タスク未取得]`、MCP の `list_projects` で `unfetched: true` と示す (MUST)。キャッシュだけの検索 (プロジェクトの指定なし) は、参加している未取得のプロジェクトの数を添える (CLI は標準エラー、MCP は `note`)。`sync` は Asana へのリクエスト数を出す。
根拠: store/project_tasks.go (fetchedSQL, CountUnfetchedProjects); cli/lazy.go (noteUnfetched); cli/sync.go; TestProjectTaskStates, TestTaskListUnfetchedProjectOffline, TestListTasksUnfetchedProjectOffline, TestLazySyncFetchesProjectListOnly

#### Scenario: 検索
- **WHEN** 未取得のプロジェクトが 2 件ある状態で `asagao search 何か` を実行する
- **THEN** 標準エラーに「タスクをまだ取得していないプロジェクトが 2 件あり、そこは探していません」と出る
