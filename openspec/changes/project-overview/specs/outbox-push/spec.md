## MODIFIED Requirements

### Requirement: 操作の種類
outbox の操作の種類は 19 個 (MUST): `update_task`、`create_task`、`add_comment`、`move_task`、`add_follower`、`remove_follower`、`add_project`、`remove_project`、`duplicate_task`、`upload_attachment`、`set_custom_field`、`add_tag`、`remove_tag`、`add_dependency`、`remove_dependency`、`rename_section`、`create_status`、`mark_notification`、`update_project`。
根拠: internal/store/outbox.go:28-33; relations.go:18-23; customfields.go:38-40; tags.go:15-16; dependencies.go:20-21; sections.go:16; projects.go:16; notifications.go:21; project_fields.go:20

#### Scenario: タスクにタグを付ける
- **WHEN** 既にあるタグをタスクに付ける
- **THEN** `add_tag` の操作が積まれ、payload に `tag`・`tag_name`・`workspace` が入る

## ADDED Requirements

### Requirement: プロジェクトの編集は update_project に積む
プロジェクトの説明 (`notes`・`html_notes`)・オーナー (`owner`、`""` で外す)・開始日 (`start_on`)・期日 (`due_on`)・色 (`color`) の変更は、ローカルに当てて `update_project` に積む (MUST)。`task_gid` はプロジェクトの gid、`base` は変える前の値。同じプロジェクトの編集は 1 つにまとめ、全部を元に戻したら消す。捨てたら `base` に戻す。
根拠: internal/store/project_fields.go; TestUpdateProjectLocal, TestDiscardProjectEdit

#### Scenario: 元に戻す
- **WHEN** 色を変えたあと、元の色に戻す
- **THEN** `update_project` は残らない

### Requirement: プロジェクトの編集の検査
色は Asana の色の名前か `none`、日付は `YYYY-MM-DD` でなければ拒む (MUST)。開始日には期日が要り、期日より後にできない。拒んだときは何も当てず、何も積まない。
根拠: internal/domain/project; TestUpdateProjectLocalRejects, TestValidateFields, TestCheckDates

#### Scenario: 期日のないプロジェクトに開始日
- **WHEN** 期日のないプロジェクトに開始日だけを付ける
- **THEN** 「開始日を付けるには期日も必要です」で拒み、outbox は変わらない

### Requirement: 取り込みで未送信のプロジェクトの編集を残す
プロジェクトを取り込むときは、Asana の値で書いたあと、同じトランザクションで、`failed` でない `update_project` を当て直す (MUST)。
根拠: internal/store/write.go (UpsertProjects); TestUnsentProjectEditSurvivesSync, TestFetchProjectOverviewKeepsUnsentEdit

#### Scenario: 期日を変えて未送信のまま取り込む
- **WHEN** プロジェクトの期日をローカルで変えて未送信のまま、同期でプロジェクトを取り込む
- **THEN** 期日はローカルの値のまま、ほかの欄は Asana の値になる

### Requirement: update_project の送り方
`update_project` は、Asana のプロジェクトを取り、変えた欄 (`html_notes` を除く) のどれかが `base` ともこちらの値とも違えば競合にする (MUST)。そうでなければ `PUT /projects/{gid}` を送る。`html_notes` があれば `notes` は送らない。`""` のオーナーと日付は `null` で送る。開始日を送るときに期日を送らないなら、Asana の今の期日を添える。プロジェクトが消えていれば (404)、変更を捨てて記録する。
根拠: internal/sync/projects.go (pushUpdateProject); TestPushUpdateProject, TestPushUpdateProjectConflict, TestPushUpdateProjectGone

#### Scenario: 両方で期日を変えた
- **WHEN** ローカルで期日を変えたあと、Asana でも別の期日に変わっている
- **THEN** 送らずに競合にし、`conflict` に Asana の期日が入る
