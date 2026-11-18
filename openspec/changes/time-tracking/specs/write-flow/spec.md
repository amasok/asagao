## ADDED Requirements

### Requirement: 作業時間の書き方
作業時間は `90` (分)・`90m`・`2h`・`1h30m`・`1.5h`・`1:30` (`時間`・`分` も可) で受け付け、1 分から 24 時間まで (MUST)。分にならない小数 (`1.01h`) やほかの書き方はエラーにし、何も積まない。日は期日と同じ書き方で、省略すれば今日。
根拠: domain/timetrack/timetrack.go; app/timetracking.go; TestParseDuration, TestTimeTrackingWrites, TestTimeEditDeleteOffline

#### Scenario: 読めない時間
- **WHEN** `asagao time add <タスク> abc` を実行する
- **THEN** 書き方の例をエラーで出し、何も積まない

### Requirement: 作業時間の記録の削除の確認
記録の削除は Asana でも取り消せないので確認する (MUST)。TUI は「やめる」を先頭にした選択で確かめ、CLI の `time delete` は `--yes` がなければ何も積まずにエラーにする。MCP のツールの説明で利用者に確かめるよう求める。
根拠: tui/timetracking.go; cli/timetracking.go; mcpserver/timetracking.go; TestTimeEntriesInTUI, TestTimeEditDeleteOffline

#### Scenario: TUI で記録を消す
- **WHEN** 記録を選んで「削除する…」を選ぶ
- **THEN** 確認が出て、「削除する」を選んだときだけ消す
