## MODIFIED Requirements

### Requirement: 操作の種類
outbox の操作の種類は 30 個 (MUST): `update_task`、`create_task`、`add_comment`、`update_story`、`delete_story`、`move_task`、`move_my_section`、`delete_task`、`set_parent`、`add_follower`、`remove_follower`、`add_project`、`remove_project`、`duplicate_task`、`upload_attachment`、`delete_attachment`、`set_custom_field`、`add_tag`、`remove_tag`、`update_tag`、`delete_tag`、`add_dependency`、`remove_dependency`、`rename_section`、`create_section`、`delete_section`、`insert_section`、`create_status`、`mark_notification`、`update_project`。
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

### Requirement: オフラインで作った親への付け替え
新しい親がオフラインで作ったタスクなら、`set_parent` はその作成が送れるまで待つ (MUST)。作成が失敗しても待ち続け、捨てない。その作成を取り消したとき、または Asana で消えていたとして捨てたときは、そのタスクを親にする `set_parent` も取り消し、子を元の親に戻す。
根拠: sync/push.go (waitsForCreate); store/taskops.go (undoSetParentsTo); store/outbox.go (discardLocalTask); store/discard.go (DiscardDeletedTask); TestPushSetParentWaitsForTheParentsCreate, TestParentCreatedOfflineGoesAway

#### Scenario: 親の作成が失敗する
- **WHEN** オフラインで作った親の下へ既存のタスクを移し、親の作成が 400 で失敗する
- **THEN** `set_parent` は送られず `pending` のまま残り、捨てた変更の記録も増えない

### Requirement: セクションの中の位置
`move_task` は任意で `insert_before` / `insert_after` (移す先のセクションにあるほかのタスク) を持ち、`addTask` にそのまま渡す (MUST)。位置があれば、今と同じセクションへの移動でも積む。位置付きの移動は、それまでの同じタスクの移動をまとめたうえで列の末尾に積み直し、batch に入れず 1 件ずつ送る。ローカルの gid の目印は送るときに本物の gid に引き、引けなければ位置を付けずに送る。
根拠: store/outbox.go (MoveTaskAtLocal); store/taskops.go (requeue); sync/push.go (pushMove, position); sync/batch.go; TestMoveTaskAtLocal, TestMoveToAPlaceGoesToTheEndOfTheQueue, TestPushMoveToAPlace

#### Scenario: 同じセクションで並べ替える
- **WHEN** タスクを、同じセクションのタスクの後ろに置く
- **THEN** `move_task` が積まれ、送ると `addTask` に `insert_after` が入る
