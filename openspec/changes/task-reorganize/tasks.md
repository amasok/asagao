# タスク

- [x] asana: `DeleteTask`・`SetParent`・`InsertTaskInSection` (位置つきの addTask)
- [x] store: `DeleteTaskLocal` / `PlanDeleteTask` / `CompleteDelete`、`SetParentLocal` (畳み込み・取り込み後の再適用・取り消し)、`MoveTaskAtLocal`、削除を積んだタスクを一覧から隠す
- [x] sync: `pushDelete` (404 は済み)、`pushSetParent`、`pushMove` と batch の位置
- [x] app: `TaskOps`、`Sections.MoveAt`
- [x] CLI (`task rm`・`task parent`・`task move --before/--after`)、MCP (`delete_task`・`set_parent`・`move_task`)、TUI (`space Z`・`space p`)
- [x] README
- [x] `go build ./... && go vet ./... && go test -race ./...`
