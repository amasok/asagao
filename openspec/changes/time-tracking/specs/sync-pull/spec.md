## ADDED Requirements

### Requirement: 作業時間の記録は開いたときに取る
タスクの作業時間の記録は同期では取らず、利用者が記録を見るとき (TUI の一覧、CLI の `time list`、MCP の `list_time_entries`) に `GET /tasks/{gid}/time_tracking_entries` で取ってキャッシュを置き換える (MUST)。送っていない記録 (ローカル gid)、取り始めより後に Asana が作った記録は残し、送っていない変更と削除はやり直す。取れなければキャッシュを出す。
根拠: sync/timetracking.go (FetchTimeEntries); store/time_entries.go (ReplaceTimeEntries); TestReplaceTimeEntries, TestUpdateTimeEntryLocal, TestTimeEntryChangedWhileSending, TestTimeAddAndList

#### Scenario: 送っていない変更のあとに取り直す
- **WHEN** 30 分の記録を 70 分に直し、送る前に Asana の一覧 (30 分のまま) を取る
- **THEN** キャッシュの記録は 70 分のまま
