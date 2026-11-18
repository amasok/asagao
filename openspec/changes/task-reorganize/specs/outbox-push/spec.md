## MODIFIED Requirements

### Requirement: 操作の種類
outbox の操作の種類は 20 個 (MUST): `update_task`、`create_task`、`add_comment`、`move_task`、`delete_task`、`set_parent`、`add_follower`、`remove_follower`、`add_project`、`remove_project`、`duplicate_task`、`upload_attachment`、`set_custom_field`、`add_tag`、`remove_tag`、`add_dependency`、`remove_dependency`、`rename_section`、`create_status`、`mark_notification`。
根拠: internal/domain/outbox/outbox.go; internal/store/outbox.go:28-33; taskops.go; relations.go:18-23; customfields.go:38-40; tags.go:15-16; dependencies.go:20-21; sections.go:16; projects.go:16; notifications.go:21

#### Scenario: タスクにタグを付ける
- **WHEN** 既にあるタグをタスクに付ける
- **THEN** `add_tag` の操作が積まれ、payload に `tag`・`tag_name`・`workspace` が入る

## ADDED Requirements

### Requirement: タスクの削除
タスクの削除は、一度も送っていないタスクならその場で作成ごと (サブタスクと積んだ変更も) 消し、送ったことのあるタスクなら `delete_task` を積む (MUST)。送るまでは、そのタスクとサブタスクを一覧とサブタスクの一覧から隠し、取り消せば元に戻す。同じタスクの削除を 2 回積まない。
根拠: store/taskops.go (DeleteTaskLocal, deletedTasks); store/read.go (ListTasks, GetTask); TestDeleteTaskLocalHidesUntilSent, TestDeleteLocalTaskIsImmediate

#### Scenario: 削除を積んで取り消す
- **WHEN** 送ったことのあるタスクを削除し、送る前に outbox の `delete_task` を取り消す
- **THEN** 削除を積んでいるあいだはタスクとサブタスクが一覧に出ず、取り消すと、サブタスクに積んであった変更ごと元に戻る

### Requirement: タスクの削除の送信
`delete_task` が送れたら、タスク・サブタスク・それらに積んであった変更をキャッシュから消す (MUST)。`DELETE /tasks/{gid}` が 404 なら、もう消えているとして済んだ扱いにし、捨てた変更として記録しない。
根拠: store/taskops.go (CompleteDelete); sync/push.go (pushDelete); TestCompleteDeleteRemovesTheTree, TestPushDeleteTask, TestPushDeleteTaskAfterA5xx

#### Scenario: 消えていたタスクの削除
- **WHEN** `delete_task` を送ったら Asana が 404 を返す
- **THEN** 操作は済んだ扱いで消え、タスクはキャッシュから消え、捨てた変更の記録は増えない

### Requirement: 親タスクの付け替え
親タスクの付け替えは `set_parent` を積む (MUST)。payload に `parent` (空なら親から外す)・`parent_name`、任意で `insert_before` / `insert_after` (新しい親のほかのサブタスク) を、base に元の親を持つ。同じタスクの続けての付け替えは 1 つにまとめ、元の親に位置なしで戻したら操作を消す。自分自身と自分のサブタスクの下には移さない。送るまでは、取り込みで親が上書きされても積んだ親に戻す。取り消すと元の親に戻す。
根拠: store/taskops.go (SetParentLocal, reapplySetParent); store/outbox.go (discardOpTx); sync/push.go (pushSetParent); TestSetParentLocal, TestSetParentLocalRefuses, TestPushSetParent

#### Scenario: 親を変えて取り込む
- **WHEN** サブタスクを別の親に移し、送る前に古い親のままの内容を取り込む
- **THEN** キャッシュでは新しい親の下のままで、送ると `setParent` に新しい親が入る

### Requirement: セクションの中の位置
`move_task` は任意で `insert_before` / `insert_after` (移す先のセクションにあるほかのタスク) を持ち、`addTask` にそのまま渡す (MUST)。位置があれば、今と同じセクションへの移動でも積む。続けての移動では最後の位置を使う。ローカルの gid の目印は送るときに本物の gid に引き、引けなければ位置を付けずに送る。
根拠: store/outbox.go (MoveTaskAtLocal); sync/push.go (pushMove, position); sync/batch.go; TestMoveTaskAtLocal, TestPushMoveToAPlace

#### Scenario: 同じセクションで並べ替える
- **WHEN** タスクを、同じセクションのタスクの後ろに置く
- **THEN** `move_task` が積まれ、送ると `addTask` に `insert_after` が入る
