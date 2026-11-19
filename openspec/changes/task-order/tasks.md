# タスク

- [x] store: マイグレーション 24 (`position`・`subtask_position`)、`SetProjectTaskOrder` / `SetSectionTaskOrder` / `SetSubtaskOrder`、取り込みで位置を保つ、`ListTasks`・`GetTask` の並べ方
- [x] store: `MoveTaskAtLocal`・`SetParentLocal` の位置、取り込みと並びの取り直しのあとの再適用、取り消しで元の位置に戻す
- [x] asana: `SectionTaskOrder`
- [x] sync: 全件の取得で位置を保存、一部の取得・Events・完了済みの取得でセクションを取り直す、サブタスクの順、Events の挙げたサブタスクの親を取り直す
- [x] README・CLI のヘルプ・MCP の説明
- [x] `go build ./... && go vet ./... && go test -race ./...`
