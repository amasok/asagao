## MODIFIED Requirements

### Requirement: 更新の検証
`UpdateTaskLocal` は、欄の名前が許可した一覧 (`completed`・`name`・`notes`・`html_notes`・`due_on`・`assignee`・`liked`・`resource_subtype`・`approval_status`・`custom_type`・`custom_type_status_option`) にあり、型と値が正しいときだけ積む (MUST)。名前は空にできない。`approval_status` は、種類が `approval` のタスクにだけ設定でき、断ったときは何も積まない。`custom_type` と `custom_type_status_option` (#59) は `resource_subtype` が `custom` のタスクにだけ設定でき、`custom` には種類が要り、ステータスはそのタスクの (同じ変更で変える種類があればその) 種類のものだけ。
根拠: outbox.go:93-101, :705-743; domain/task/fields.go (CheckCustomType); store/customtypes.go (customTypeChanges); TestApprovalStatusAndType, TestApprovalOnlyForApprovalTasks, TestCheckCustomType, TestCustomTypeNeedsCustomSubtype

#### Scenario: 普通のタスクに承認の状態を付ける
- **WHEN** `default_task` のタスクに `approval_status` を設定する
- **THEN** エラーになり、outbox もキャッシュも変わらない

#### Scenario: ほかの種類のステータス
- **WHEN** 種類「バグ」のタスクに、種類「依頼」のステータスを設定する
- **THEN** エラーになり、outbox もキャッシュも変わらない

## ADDED Requirements

### Requirement: カスタムタイプの変更
種類 (`custom_type`) だけを変えたときは、その種類の最初の選べるステータス (完了でないものを先に) も積む (MUST)。種類はタスクのプロジェクトが出すものだけ、ステータスは選べるものだけ (今のステータスと Asana 側のステータスは通す) を受け付ける。
根拠: store/customtypes_write.go (customTypeChanges, defaultCustomStatus, offersCustomType); TestUpdateCustomTypeLocal, TestAttackDisabledStatusIsRefused, TestAttackUnknownTypeIsRefused

#### Scenario: 種類だけを変える
- **WHEN** 普通のタスクを種類「バグ」(ステータス: 完了・対応中) にする
- **THEN** payload に `resource_subtype: custom`・`custom_type`・`custom_type_status_option` (対応中) が入る

### Requirement: ステータスと完了
完了のステータス (`completion_state` が Complete) にするとローカルでも完了にし、未完了のステータスなら未完了にする (MUST)。
根拠: store/outbox.go (applyFields); TestUpdateCustomTypeLocal

#### Scenario: 完了のステータス
- **WHEN** 種類「バグ」のタスクのステータスを「完了」にする
- **THEN** タスクはローカルで完了になる

### Requirement: カスタムタイプをやめる
ほかの種類 (`resource_subtype`) にすると、ローカルの種類とステータスを外し、payload から `custom_type`・`custom_type_status_option` を除く (MUST)。base には種類・カスタムタイプ・ステータス・完了を残し、取り消すと元に戻る。
根拠: store/outbox.go (applyFields); domain/outbox/fold.go; TestDiscardRestoresCustomType, TestFoldCustomType

#### Scenario: マイルストーンにして取り消す
- **WHEN** 種類「バグ」のタスクをマイルストーンにし、その変更を取り消す
- **THEN** 種類「バグ」と元のステータス・完了に戻る

### Requirement: オフラインで作ったタスクの種類
オフラインで作ったタスクの種類は作成に混ぜず、その後のタスクの変更もまとめて `update_task` として作成のあとに送る (MUST)。
根拠: store/outbox.go (updateTaskLocal); TestCustomTypeOfOfflineTaskIsAnUpdate

#### Scenario: 作ってから種類を付ける
- **WHEN** オフラインで作ったタスクに種類を付け、名前を変える
- **THEN** `create_task` には種類が無く、種類と名前は後の `update_task` に入る

### Requirement: カスタムタイプを送る
`custom_type` か `custom_type_status_option` のある `update_task` は、`resource_subtype: custom` と (ステータスだけなら base の) `custom_type` も付けて `PUT /tasks/{gid}` で送り、`POST /batch` にはまとめない (MUST)。Asana のステータスが base と違えば競合にする。
根拠: sync/push_customtypes.go (taskUpdateFields); sync/batch.go (batchEntryFor); TestPushCustomType, TestPushCustomTypeConflict

#### Scenario: ステータスだけを変える
- **WHEN** 種類「バグ」のタスクのステータスを変えて送る
- **THEN** 本文に `resource_subtype: custom`・`custom_type`・`custom_type_status_option` が入る

### Requirement: Asana が受け付けないカスタムタイプ
Asana が断ったとき (402・400・403・422)、または 200 でも答えに送った種類・ステータスが無いときは、種類の部分だけを操作から外し、理由付きで捨てた変更に残して、タスクの種類・ステータス・完了をローカルで base に戻す (MUST)。残りは、断られたときは送り直し、200 のときは完了にする。答えの種類かステータスに gid が無いときは比べられないので、断られたとはみなさない。
根拠: sync/push_customtypes.go (customTypeRefused, customTypeApplied); store/customtypes_write.go (WithdrawCustomType); TestPushCustomTypeNotSupported, TestCustomTypeAppliedWithoutGid

#### Scenario: Asana が種類を受け付けない
- **WHEN** 種類と名前を変えた変更に、Asana が 400 を返す (または種類を変えずに 200 を返す)
- **THEN** 種類は理由付きで捨てた変更になってタスクの種類が元に戻り、名前は Asana に届く

### Requirement: カスタムタイプの 402
種類を送って 402 が返ったら、そのタスクのワークスペースについて `plan_unavailable:custom_types:<workspace>` を記録する (MUST)。同じ操作に開始日があっても、開始日の印は付けず、種類を外した残りと一緒に送り直す。
根拠: sync/push_customtypes.go (customTypeRefused); sync/push.go (send); TestPushCustomTypePaymentRequired, TestPushCustomTypeAndStartOnPaymentRequired

#### Scenario: 開始日と種類に 402
- **WHEN** 開始日と種類を変えた変更に、種類があるときだけ 402 が返る
- **THEN** 種類だけが捨てた変更になり、開始日は送られ、開始日の印は付かない
