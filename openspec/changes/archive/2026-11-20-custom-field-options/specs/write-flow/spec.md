## ADDED Requirements

### Requirement: 無い選択肢を足して設定する
選択式のフィールドの値を入れるとき、利用者が明示したとき (TUI の「＋ 選択肢を足して設定する…」、CLI の `--add-option`、MCP の `add_missing_options`) は、フィールドに無い名前を選択肢として足し、その値にする (MUST)。既存とみなすのは gid か、名前が大文字小文字を除いて一致するものだけで、部分一致では既存の選択肢を選ばない。multi_enum はほかの選択肢を残す。すべての名前を確かめてから書き込み、だめなら何も積まない。
根拠: app/fields.go (FieldOptions.ParseAdding, SetWithNewOption); app/relations.go (ResolveFieldsAdding); TestNewOptionFromChooser, TestTaskFieldAddOption, TestUpdateTaskAddMissingOptions

#### Scenario: 部分一致する既存の選択肢があるとき
- **WHEN** 選択肢「高」があるフィールドに、`--add-option` で「最高」を設定する
- **THEN** 「最高」が選択肢として足され、そのタスクの値になる
