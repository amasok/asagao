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
種類 (`custom_type`) だけを変えたときは、その種類の最初の選べるステータス (完了でないものを先に) も積む (MUST)。完了のステータス (`completion_state` が Complete) にするとローカルでも完了にし、未完了のステータスなら未完了にする。ほかの種類 (`resource_subtype`) にすると、ローカルの種類とステータスを外し、payload から `custom_type`・`custom_type_status_option` を除く。base には種類・カスタムタイプ・ステータス・完了を残し、取り消すと元に戻る。オフラインで作ったタスクの種類は作成に混ぜず、その後のタスクの変更もまとめて `update_task` として作成のあとに送る。
根拠: store/customtypes.go (customTypeChanges, defaultCustomStatus); store/outbox.go (applyFields); domain/outbox/fold.go; TestUpdateCustomTypeLocal, TestDiscardRestoresCustomType, TestCustomTypeOfOfflineTaskIsAnUpdate, TestFoldCustomType

#### Scenario: 種類だけを変える
- **WHEN** 普通のタスクを種類「バグ」(ステータス: 完了・対応中) にする
- **THEN** payload に `resource_subtype: custom`・`custom_type`・`custom_type_status_option` (対応中) が入る

### Requirement: カスタムタイプを送る
`custom_type` か `custom_type_status_option` のある `update_task` は、`resource_subtype: custom` と (ステータスだけなら base の) `custom_type` も付けて `PUT /tasks/{gid}` で送り、`POST /batch` にはまとめない (MUST)。Asana のステータスが base と違えば競合にする。Asana が断ったとき (402・400・403・422)、または 200 でも答えのタスクに送った種類・ステータスが無いときは、種類の部分 (`custom_type`・`custom_type_status_option`・`resource_subtype: custom`) だけを操作から外して、理由付きで捨てた変更に残し、タスクの種類・ステータス・完了をローカルで base に戻す。残り (名前など) は、断られたときは送り直し、200 のときは届いたので完了にする。402 なら、そのタスクのワークスペースについて `plan_unavailable:custom_types:<workspace>` も記録する。
根拠: sync/push_customtypes.go (taskUpdateFields, customTypeRefused, customTypeApplied, customTypePlanRefusal); sync/batch.go (batchEntryFor); TestPushCustomType, TestPushCustomTypePaymentRequired, TestPushCustomTypeConflict, TestPushCustomTypeNotSupported; store/customtypes_write.go (WithdrawCustomType)

#### Scenario: ステータスだけを変える
- **WHEN** 種類「バグ」のタスクのステータスを変えて送る
- **THEN** 本文に `resource_subtype: custom`・`custom_type`・`custom_type_status_option` が入る

#### Scenario: Asana が種類を受け付けない
- **WHEN** 種類と名前を変えた変更に、Asana が 400 を返す (または種類を変えずに 200 を返す)
- **THEN** 種類は理由付きで捨てた変更になってタスクの種類が元に戻り、名前は Asana に届く
