# タスク

- [x] settings: `sync.lazy_projects` (既定 true)
- [x] asana: `Client.Requests`
- [x] store: 取得の状態 (`ProjectTaskStates`・`ProjectTaskState`・`WantProject`・`CountUnfetchedProjects`・`SyncStatesWithPrefix`・`ProjectRef`)、`ProjectView.Unfetched`、キーの片付け
- [x] sync: 同期するプロジェクトの選び方、未完了を先に取る、`OpenProject`、`FetchCompleted`、`Result.Listed` / `CompletedPending` / `Requests`
- [x] TUI: 未取得の印と注記、とどまったら取る、`DeferCompleted` と `FetchCompleted`
- [x] CLI: `task list` / `search` の `--project` で取る、`--offline`、`project list` の印、`search` の注記、`sync` のリクエスト数
- [x] MCP: `list_tasks` / `search_tasks` で取る、`note`、`list_projects` の `unfetched`
- [x] README
- [x] `go build ./... && go vet ./... && go test -race` (変えたパッケージ)
