## ADDED Requirements

### Requirement: タスクの並びを取り込む
取り込みは、セクションの中のタスクの並びとサブタスクの並びを Asana の順で保存する (MUST)。プロジェクトを全部取ったときは返った順を、一部だけ取ったとき (Events、modified_since、完了済みの取得) は、取ったタスクのセクションを `GET /sections/{gid}/tasks` で取り直した順を、サブタスクは `GET /tasks/{gid}/subtasks` の順を使う。Events がサブタスクを挙げたら、その親のサブタスクを取り直す。
根拠: sync/lazy.go (pullProject, refreshSections, refreshParents, storeSubtasks, pullCompleted); store/order.go; TestPullKeepsAsanaOrder, TestPullCompletedTakeTheirPlace, TestOrderFromListings

#### Scenario: Events で並べ替えが届く
- **WHEN** 同じセクションの中でタスクが並べ替えられ、Events がそのタスクを挙げる
- **THEN** そのセクションの並びを取り直し、`list_tasks` と TUI がその順で並べる

### Requirement: 並びを取れないとき
セクションや親の並びが 404・403 なら、今の並びのまま同期を進める (MUST)。ほかの失敗は同期の失敗とし、Events の token を進めない。
根拠: sync/lazy.go (refreshSections, refreshParents); TestPullSectionOrderUnavailable

#### Scenario: セクションが消えていた
- **WHEN** 並びを取り直すセクションが 404 を返す
- **THEN** 同期は成功し、そのセクションの並びは前のまま

### Requirement: 並びの分からないタスク
並びの分からないタスクは、分かるタスクのあとに、今までの順 (一覧は完了・期日・名前、サブタスクは作った順) で並べる (MUST)。一覧は、セクション・プロジェクトで絞ったときはセクションの順、その中は位置の順。
根拠: store/read.go (ListTasks, GetTask); TestOrderUnknownKeepsOldOrder, TestOrderFromListings

#### Scenario: 並びが分からない
- **WHEN** #51 より前に取り込んだタスクを、全件の同期の前に一覧する
- **THEN** 今までの順 (完了・期日・名前) で並ぶ
