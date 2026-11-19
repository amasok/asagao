## ADDED Requirements

### Requirement: 並べ替えはキャッシュにもすぐ出す
`move_task` と `set_parent` を積んだら、Asana が置く場所にキャッシュでも置く (MUST)。`insert_before` / `insert_after` があればその隣、位置なしで別のセクションへ移したらその先頭、位置なしで親を変えたら末尾。送るまでは、並びの取り直しやタスクの取り込みのあとも、積んだ場所に戻す。
根拠: store/outbox.go (MoveTaskAtLocal, reapplyUpdates); store/taskops.go (SetParentLocal, reapplySetParent); store/order.go (placeInSection, placeSubtask, reapplyMoves, reapplyParentMoves); TestOrderOfUnsentMove, TestOrderOfMoveToOtherSection, TestOrderOfUnsentSetParent

#### Scenario: 前に置いて同期する
- **WHEN** `task move C --before A` を積み、送る前に Asana の古い並びを取り込む
- **THEN** キャッシュの一覧では C が A の前のまま

### Requirement: 並べ替えをやめたら元の場所に戻す
元の場所に位置なしで戻したときと、操作を取り消したときは、そのとき前にあったタスクのあとに戻す (MUST)。そのタスクがもうなければ、元の番号に戻す。
根拠: store/order.go (recordPlace, restorePlace); store/outbox.go (discardOpTx); TestOrderDiscardAfterRenumbering, TestOrderOfMoveToOtherSection

#### Scenario: 並べ替えを取り消す
- **WHEN** C を A の後ろに移して、送る前に outbox から取り消す
- **THEN** C は元の場所 (前にあったタスクのあと) に戻る
