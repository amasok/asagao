## MODIFIED Requirements

### Requirement: 操作の種類
outbox の操作の種類は 33 個 (MUST): `update_task`、`create_task`、`add_comment`、`update_story`、`delete_story`、`move_task`、`move_my_section`、`delete_task`、`set_parent`、`add_follower`、`remove_follower`、`add_project`、`remove_project`、`duplicate_task`、`upload_attachment`、`delete_attachment`、`set_custom_field`、`create_enum_option`、`add_custom_field_setting`、`remove_custom_field_setting`、`add_tag`、`remove_tag`、`add_dependency`、`remove_dependency`、`rename_section`、`create_section`、`delete_section`、`insert_section`、`update_tag`、`delete_tag`、`create_status`、`mark_notification`、`update_project`。
根拠: internal/domain/outbox/outbox.go (種類の定数。store の Op* はその別名)

#### Scenario: タスクにタグを付ける
- **WHEN** 既にあるタグをタスクに付ける
- **THEN** `add_tag` の操作が積まれ、payload に `tag`・`tag_name`・`workspace` が入る

#### Scenario: 選択肢を足す
- **WHEN** 選択式のフィールドに無い選択肢「低」を足す
- **THEN** `create_enum_option` の操作が積まれ、`task_gid` は選択肢のローカル gid、payload に `field`・`field_name`・`name` が入る

## ADDED Requirements

### Requirement: オフラインで足した選択肢
選択肢の作成 (`create_enum_option`) はローカル gid を付けて積み、タスクの値にすぐ使える (MUST)。ローカル gid の選択肢を使う値の変更 (`set_custom_field`、値付きの `create_task`) は、その作成が outbox にある間は (失敗していても) 送らずに待ち、`POST /batch` にもまとめない。
根拠: store/field_ops.go (AddEnumOptionLocal); sync/push_fields.go (waitsForField); sync/batch.go; TestPushNewOptionForbidden

#### Scenario: 作成を断られた選択肢を使う値
- **WHEN** 足した選択肢を値にしたタスクと、既存の選択肢を値にしたタスクがあり、選択肢の作成が断られる
- **THEN** 既存の選択肢の値だけが送られ、ローカル gid は Asana に送られない

### Requirement: 足した選択肢の gid の置き換え
選択肢の作成が送れたら、フィールドの選択肢、タスクの値、outbox の payload・base・conflict の中のローカル gid を本物に書き換え (multi_enum は並べ直す)、`sync_state` に `enum_option:<local>` → 本物 を記録する (MUST)。同期でフィールドの選択肢を入れ直しても、作成が outbox にある選択肢は残す。
根拠: store/field_ops.go (replaceLocalOption, localOptions); TestPushNewOptionThenValue, TestPushNewOptionMultiEnum, TestCompleteCreateEnumOption, TestPullKeepsLocalOption

#### Scenario: 足した選択肢を値にして同期する
- **WHEN** オフラインで「優先度」に「低」を足してタスクの値にし、同期する
- **THEN** 先に選択肢が作られ、値は本物の gid で送られる。キャッシュの値と選択肢も本物の gid になる

### Requirement: 選択肢の照合
選択肢は二度作らない (MUST)。送る前と、答えが届かなかったときの確認で、Asana のフィールドを読み、同じ名前の有効な選択肢 (ほかの作成の結果として記録されていないもの) があれば、それを作成の結果として記録して作らない。
根拠: sync/push_fields.go (pushCreateEnumOption, findEnumOption); TestPushNewOptionTakesExisting, TestPushNewOptionLostAnswer

#### Scenario: 作成の答えが届かない
- **WHEN** 選択肢の作成に Asana が 500 を返したが、選択肢は作られていた
- **THEN** 次の送信でその選択肢を結果として記録し、もう一度は作らない

### Requirement: 足した選択肢の取り消し
送っていない選択肢の作成を取り消すと、フィールドの選択肢から消す (MUST)。その選択肢だけを設定した変更は元の値に戻して消し、multi_enum のほかの選択肢はそのまま送る。オフラインで作ったタスクは、その値なしで作る。
根拠: store/field_ops.go (undoCreateEnumOption); TestDiscardCreateEnumOption, TestPushNewOptionForbidden

#### Scenario: 作成を取り消す
- **WHEN** 足した選択肢をタスクに設定し、作成を取り消す
- **THEN** タスクの値は元に戻り、outbox は空になる

### Requirement: フィールドの付け外し
既存のフィールドをプロジェクトに付ける (`add_custom_field_setting`) と、すぐにプロジェクトのタスクで編集でき、その値の変更は付ける操作が outbox にある間は待つ (MUST)。送れていない逆の操作があるときは、積まずにそれを消す。外す (`remove_custom_field_setting`) のを取り消すと、元の位置と「重要」に戻す。同期でプロジェクトのフィールドを入れ直しても、送っていない付け外しは保つ。
根拠: store/field_ops.go (SetProjectFieldLocal, reapplyFieldSettingOps, undoFieldSettingOp); TestSetProjectFieldLocal, TestValueWaitsForFailedFieldSetting, TestPushRemoveFieldSetting

#### Scenario: 付けたフィールドに値を入れて同期する
- **WHEN** オフラインでフィールド「メモ」をプロジェクトに付け、タスクの「メモ」を設定して同期する
- **THEN** 先にフィールドが付けられ、そのあとで値が送られる

### Requirement: フィールドの付け外しの送信
フィールドの付け外しや選択肢の作成に Asana が 4xx (401・402・403・404・429 を除く。例: "Custom field already exists in parent object."、"An enum option already exists with the name") を返しても、Asana を読んで既にその状態 (選択肢は同じ名前の有効なもの) なら完了とする (MUST)。送れたらプロジェクトのフィールドを取り直す。
根拠: sync/push_fields.go (pushFieldSetting, pushCreateEnumOption, isRefusedAsIs); TestPushAddFieldSettingThenValue, TestPushAddFieldSettingAlreadyThere, TestPushNewOptionDuplicateMeanwhile

#### Scenario: ほかの人が先に付けていた
- **WHEN** 付けようとしたフィールドを、送る前にほかの人がプロジェクトに付けていた
- **THEN** 400 が返っても完了になり、失敗にしない

### Requirement: カスタムフィールドの変更をプランで断られたとき
選択肢の作成やフィールドの付け外しに Asana が 402 を返したら、そのワークスペースについて `plan_unavailable:custom_fields:<workspace>` に時刻を記録し、24 時間はどちらの機能もそのワークスペースで TUI・CLI・MCP に出さない (MUST)。24 時間たてば試し直し、書き込みが通れば印を消す。ほかのワークスペースでは隠さない。操作は理由付きで失敗にする。
根拠: sync/plan.go (recordPlanRefusal, clearPlanRefusal); sync/push.go (send); readmodel/plan.go (PlanKey, PlanOffFor); app/plan.go (planOffered); TestPushNewOptionPaymentRequired, TestNewOptionNotOfferedWhenRefused

#### Scenario: 無料のワークスペースで 402
- **WHEN** あるワークスペースで選択肢の作成に Asana が 402 を返す
- **THEN** そのワークスペースでは 24 時間「選択肢を足して設定する」を出さず、ほかのワークスペースでは出す

### Requirement: カスタムフィールドの変更を権限で断られたとき
選択肢の作成に Asana が 403 を返したら、そのフィールドについて `field_options_locked:<field>` を記録し、そのフィールドでは選択肢の追加を出さない (MUST)。フィールドの付け外しの 403 なら、そのプロジェクトについて `project_fields_locked:<project>` を記録し、そこでは出さない。操作は理由付きで失敗にする。
根拠: sync/push_fields.go (fieldForbidden); app/fields.go; TestPushNewOptionForbidden, TestValueWaitsForFailedFieldSetting, TestProjectFieldsLocked

#### Scenario: 選択肢の追加を 403 で断られる
- **WHEN** 選択肢の作成に Asana が 403 を返す
- **THEN** 操作は失敗になり、そのフィールドでは「選択肢を足して設定する」を出さない。ほかのフィールドでは出す

### Requirement: カスタムフィールドの値の書き込み形式
`set_custom_field` の値は、Asana が受け付ける形で送る (MUST): date は `{"date": "YYYY-MM-DD"}` (文字列は "date_value: DayAndDateTime is not a JSON object" で断られる)、people と multi_enum は gid の配列で、空にするときは `[]` (people は null を "people_value: Not an array" で断られる)、そのほかを空にするときは null。本物の Asana で確かめた (Issue #1)。number の形は未確認。
根拠: readmodel/fieldvalues.go (CustomFieldAPIValue); TestMovedValuesMatchOldStore, TestPushClearMultiEnumSendsEmptyList

#### Scenario: 担当者のフィールドを空にする
- **WHEN** people のフィールドを空にして同期する
- **THEN** `{"custom_fields": {"<field>": []}}` が送られる
