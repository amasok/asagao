## ADDED Requirements

### Requirement: セクションとタグの削除の確認
セクションとタグの削除は Asana でも取り消せないので、確認してから行う (MUST)。TUI は「やめる」を先頭にした選択で確かめる (タグは外れるタスクの数を出す)。CLI の `section rm` と `tag rm` は `--yes` がなければ何も積まずにエラーにし、`--dry-run` は `--yes` なしで計画を出す。MCP のツールの説明で、利用者に確かめるよう求める。空でないセクションと、プロジェクトの最後のセクションは消さない。
根拠: cli/sections.go; tui/sectiontags.go; mcpserver/sections.go; TestWriteCommandsRefuseUnknown, TestDryRunChangesNothing, TestTUIAddSelectAndDeleteSection, TestTUIMoveSectionAndRefuseNonEmpty, TestTUITagManager

#### Scenario: CLI で --yes なしにタグを消す
- **WHEN** `asagao tag rm 急ぎ` を実行する
- **THEN** 何も積まずに、外れるタスクの数と `--yes` が要ることをエラーで出す

#### Scenario: TUI でセクションを消す
- **WHEN** 空のセクションの上で削除を選ぶ
- **THEN** 確認が出て、「削除する」を選んだときだけ削除する

### Requirement: タグの色
タグの色は Asana の 18 色か「なし」(`none` または空) だけを受け付ける (MUST)。大文字小文字は区別しない。ほかの値はエラーにし、何も積まない。「なし」は Asana に null で送る。
根拠: domain/tag/tag.go; store/tag_ops.go; sync/push_sections.go; TestNormalizeColor, TestPushUpdateAndDeleteTag

#### Scenario: 使えない色
- **WHEN** `asagao tag color 急ぎ green` を実行する
- **THEN** 使える色の一覧をエラーで出し、何も積まない
