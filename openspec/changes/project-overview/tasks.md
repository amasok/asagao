# タスク

- [x] asana: `UpdateProject`・`TaskCounts`・`ProjectBrief`・`MyTeams`・`FavoriteProjects`、プロジェクトの owner・日付・html_notes
- [x] store: マイグレーション、`UpdateProjectLocal` (outbox `update_project`)、取り込みで未送信の編集を当て直す、チーム・お気に入り・概要の保存と読み出し、一覧の並び
- [x] sync: チームとお気に入りの取り込み、`FetchProjectOverview`、`update_project` の送信と競合
- [x] app: `Projects` (編集のユースケース)、`Query` のチームと概要
- [x] TUI: サイドバーの見出し、概要 (`O`)、エディタで編集、新規プロジェクトのチームの選択
- [x] CLI: `project list --team`・`teams`・`show`・`edit`・`create --team`
- [x] MCP: `list_projects` の `team`、`get_project` の概要、`update_project` の項目、`create_project` の `team`
- [x] `go build ./... && go vet ./... && go test -race ./...`
