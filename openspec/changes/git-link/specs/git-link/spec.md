## ADDED Requirements

### Requirement: コミットメッセージのタスク参照
`git scan` と post-commit hook は、コミットメッセージの `#<数字>` を、数字が 10 桁以上のときだけタスク ID として読む (MUST)。`asana#<数字>` (接頭辞の大文字小文字は問わない) は桁数によらず読む。`#` (または `asana`) の直前が英数字・`_`・`&`・`/`・`#` のときと、数字のすぐあとに英数字か `_` が続くときは読まない。`fix` `fixes` `fixed` `close` `closes` `closed` `resolve` `resolves` `resolved` (大文字小文字は問わない) のあと、同じ行の空白か `:` に続く参照は、そのタスクを完了にする。キーワードはすぐあとの 1 つの参照にだけ効く。メッセージが `Revert "` で始まるときは、どの参照も完了にしない。
根拠: domain/commitref/commitref.go; TestParse

#### Scenario: GitHub の Issue 番号と取り違えない
- **WHEN** メッセージが `Merge pull request #30 from x; fixes #8` のコミットを読む
- **THEN** タスクの参照はなく、何も積まない

#### Scenario: fixes で完了にする
- **WHEN** メッセージが `Add sofa #1204567890123456` と `fixes #1204567890123457` を含むコミットを読む (2 つともキャッシュにある)
- **THEN** 前のタスクにはコメントだけ、後のタスクは完了にしてコメントし、どちらも outbox に積む

### Requirement: 同じコミットを二重に処理しない
コミットを処理してタスクに足すコメントは、末尾に `asagao-git: <コミットの ID (40 桁)>` を持つ (MUST)。タスクのコメント (送信済み・未送信) にこの行があれば、そのコミットではそのタスクに何もしない (コメントも完了もしない)。オンラインなら、判断の前にタスクとコメントを Asana から取り直し、取り直せなければキャッシュで判断して注意を出す。完了はコメントより先に積み、コメントが積めなければ次の処理でやり直す。
根拠: app/gitlink.go; cli/git.go (refreshTasks); TestGitLinkApplyTwice, TestGitScanOffline, TestGitScanOnline

#### Scenario: 2 回目は何もしない
- **WHEN** 同じコミットを 2 回 `git scan` する (1 回目のあと、タスクを未完了に戻した)
- **THEN** 2 回目は「処理済み」と出し、コメントは 1 つのまま、タスクは未完了のまま

### Requirement: post-commit hook
`git install-hook` は、`git rev-parse --git-path hooks/post-commit` の場所 (`core.hooksPath` に従う) に、`asagao git scan --hook HEAD` を呼ぶ hook を入れる (MUST)。その場所にほかのファイル (リンクを含む) があれば上書きせず、足す 1 行を出してエラーにする。`git uninstall-hook` は、asagao が入れたまま変わっていない hook だけを消す。hook モードは、HEAD の reflog が `commit` で始まるときだけ処理し (rebase・cherry-pick・revert のコミットは飛ばす)、`commit (amend)` なら amend 前のメッセージになかった参照 (完了が足されたものを含む) だけを扱う。失敗しても終了コード 0 で、理由を出す。
根拠: cli/git.go; domain/commitref/commitref.go (Amended); TestGitScanHook, TestGitInstallHook, TestGitInstallHookKeepsOtherHooks, TestGitHookScriptRuns

#### Scenario: ほかの hook を上書きしない
- **WHEN** post-commit hook がすでにあるリポジトリで `git install-hook` する
- **THEN** hook はそのままで、足す 1 行 (`asagao git scan --hook HEAD`) を出してエラーになる

#### Scenario: rebase のコミットは飛ばす
- **WHEN** rebase で書き直されたコミットで hook が動く
- **THEN** 何も積まず、何も出さない
