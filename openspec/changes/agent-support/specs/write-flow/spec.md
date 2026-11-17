## ADDED Requirements

### Requirement: CLI の --dry-run は写しの上で実行する
CLI の書き込み系コマンドは `--dry-run` を受け付ける (MUST)。ストアを一時フォルダに写し (`VACUUM INTO`)、写しの上で同じ処理をして、写しの outbox で増えた・変わった・消えた変更を表示し、写しを消す。Asana には何も送らず、本物のストア・outbox・添付ファイルのキャッシュに何も書かない。
根拠: internal/store/copy.go; internal/cli/dryrun.go; internal/cli/write.go:84-104; internal/cli/project.go:53-59; TestCopyToIsIndependent, TestDryRunChangesNothing, TestDryRunCancelled, TestWriteCommandsHaveDryRun

#### Scenario: 完了を試す
- **WHEN** `asagao task complete t1 --dry-run` を実行する (オンラインでも)
- **THEN** 「--dry-run: 次の変更をするところでした」と `t1: 完了にする` を表示し、Asana へのリクエストはなく、ストアのタスクと outbox は変わらない

#### Scenario: 未送信の変更を取り消すことになる
- **WHEN** `task complete t1` が未送信のまま `task reopen t1 --dry-run` を実行する
- **THEN** 未送信の変更 #1 の取り消しを表示し、outbox は変わらない

### Requirement: --dry-run の検査と出力
`--dry-run` の引数の検査は本番と同じで、不正ならエラーにする (MUST)。オンラインだけの操作 (`project create` / `archive` / `members`) は、引数をキャッシュで引いたうえで、行うはずの操作を表示する。`--json` では `dry_run: true` と `planned` (kind・task_gid・description・payload) を返し、写しの中だけの local gid は出さない。
根拠: internal/cli/dryrun.go; internal/cli/project.go; TestDryRunValidates, TestDryRunJSON, TestDryRunOutbox

#### Scenario: 不正な引数を試す
- **WHEN** `asagao task create 牛乳 --due 来週のどこか --dry-run` を実行する
- **THEN** 本番と同じエラーになり、何も変わらない
