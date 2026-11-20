## ADDED Requirements

### Requirement: コミットメッセージのタスク参照
`git scan` と post-commit hook は、コミットメッセージの `#<数字>` を、数字が 10 桁以上のときだけタスク ID として読む (MUST)。`asana#<数字>` (接頭辞の大文字小文字は問わない) は桁数によらず読む。`#` (または `asana`) の直前が英数字・`_`・`&`・`/`・`#` のときと、数字のすぐあとに英数字か `_` が続くときは読まない。
根拠: domain/commitref/commitref.go; TestParse

#### Scenario: GitHub の Issue 番号と取り違えない
- **WHEN** メッセージが `Merge pull request #30 from x; fixes #8` のコミットを読む
- **THEN** タスクの参照はなく、何も積まない

### Requirement: 完了にするキーワード
`fix` `fixes` `fixed` `close` `closes` `closed` `resolve` `resolves` `resolved` (大文字小文字は問わない) のあと、同じ行の空白か `:` に続く参照は、そのタスクを完了にしてコメントする (MUST)。キーワードはすぐあとの 1 つの参照にだけ効く。メッセージが `Revert "` で始まるときは、どの参照も完了にしない。完了済みのタスクはコメントだけにする。
根拠: domain/commitref/commitref.go; app/gitlink.go; TestParse, TestGitLinkApply, TestGitLinkFixesCompletedTask

#### Scenario: fixes で完了にする
- **WHEN** メッセージが `Add sofa #1204567890123456` と `fixes #1204567890123457` を含むコミットを読む (2 つともキャッシュにある)
- **THEN** 前のタスクにはコメントだけ、後のタスクは完了にしてコメントし、どちらも outbox に積む

### Requirement: コミットの印
コミットを処理してタスクに足すコメントは、末尾に `asagao-git: <コミットの ID (40 桁)>` を持つ (MUST)。印として数えるのは、rich text があればコード (`<pre>`・`<code>`) の外にあるもの、なければ平文の最後の行にあるものだけで、16 進の続きがあってはならない。メッセージに引用された印は数えない。
根拠: domain/commitref/commitref.go (Marker, Marked); app/gitlink.go (GitComment); TestMarker, TestGitLinkQuotedMarker, TestGitScanQuotedMarker

#### Scenario: 引用された印で処理済みに見えない
- **WHEN** 2 つ目のコミットのメッセージが 1 つ目のコミットの印を引用していて、2 つ目を先に読む
- **THEN** 1 つ目を読んだときも、そのタスクにコメントする

### Requirement: 同じコミットを二重に処理しない
タスクのコメント (送信済み・未送信) がコミットの印を持つか、このコンピューターで同じ印のコメントを積んだ記録 (sync_state) があれば、そのコミットではそのタスクに何もしない (MUST)。コメントも完了もしない。印と記録の確認、完了、コメントを積むことは、store の 1 つのトランザクションで行う。オンラインなら、判断の前にタスクを Asana から取り直す。取り直せなければキャッシュで判断し、注意を出す。
根拠: app/gitlink.go; store/outbox.go (AddCommentOnceLocal); cli/git.go (refreshTasks); TestGitLinkApplyTwice, TestGitLinkConcurrentWriter, TestAddCommentOnceLocal, TestAddCommentOnceLocalConcurrent, TestGitScanOffline, TestGitScanOnline

#### Scenario: 2 回目は何もしない
- **WHEN** 同じコミットを 2 回 `git scan` する (1 回目のあと、タスクを未完了に戻した)
- **THEN** 2 回目は「処理済み」と出し、コメントは 1 つのまま、タスクは未完了のまま

#### Scenario: 同時に処理しても 1 つ
- **WHEN** hook と手動の `git scan` が、同じコミットを同時に処理する
- **THEN** コメントは 1 つだけ積まれ、もう一方は「処理済み」になる

### Requirement: git scan の dry-run
`git scan --dry-run` は、ほかの書き込み系コマンドの `--dry-run` と同じく、ストアの使い捨ての写しで処理し、積まれるはずの変更を表示する (MUST)。本物のストア (キャッシュを含む) は変えず、Asana にも送らない。処理済み・見つからないタスクも表示する。
根拠: cli/git.go; cli/dryrun.go; TestGitScanDryRunOnCopy, TestGitScanNoRefsAndDryRun

#### Scenario: dry-run は何も変えない
- **WHEN** `git scan --dry-run` で `fixes #<タスクID>` のコミットを読む
- **THEN** 「--dry-run: 次の変更をするところでした」に続けて完了とコメントを表示し、outbox もタスクも変わらない

### Requirement: post-commit hook の出し入れ
`git install-hook` は、`git rev-parse --git-path hooks/post-commit` の場所 (`core.hooksPath` に従う) に、`asagao git scan --hook HEAD` を呼ぶ hook を入れる (MUST)。その場所にほかのファイル (リンクを含む) があれば上書きせず、足す 1 行を出してエラーにする。`git uninstall-hook` は、asagao が入れたまま変わっていない hook だけを消す。
根拠: cli/git.go; TestGitInstallHook, TestGitInstallHookKeepsOtherHooks, TestGitInstallHookHooksPath, TestGitHookScriptRuns

#### Scenario: ほかの hook を上書きしない
- **WHEN** post-commit hook がすでにあるリポジトリで `git install-hook` する
- **THEN** hook はそのままで、足す 1 行 (`asagao git scan --hook HEAD`) を出してエラーになる

### Requirement: hook で読むコミット
hook モードは、HEAD の reflog の最新の項目が HEAD を指し、`commit` で始まるときだけ処理する (MUST)。rebase・cherry-pick・revert のコミットは飛ばす。`commit (amend)` なら、amend 前のメッセージになかった参照 (完了が足されたものを含む) だけを扱う。reflog がなければ、参照のあるコミットは読まずに `asagao git scan HEAD` を案内する。
根拠: cli/git.go (hookCommits); domain/commitref/commitref.go (Amended); TestGitScanHook, TestGitScanHookWithoutReflog, TestAmended

#### Scenario: rebase のコミットは飛ばす
- **WHEN** rebase で書き直されたコミットで hook が動く
- **THEN** 何も積まず、何も出さない

### Requirement: hook はコミットを待たせない
hook モードはタスクを Asana から取り直さず、送信は短いタイムアウト (3 秒) で 1 回だけ試す (MUST)。送れなかった分は outbox に残し、次の sync で送る。失敗しても終了コード 0 で、理由を出す。
根拠: cli/git.go (hookPushTimeout); TestGitScanHookDoesNotWait, TestGitScanHook

#### Scenario: Asana が遅いとき
- **WHEN** Asana が応答しないときに hook が動く
- **THEN** 数秒で戻り、完了とコメントは outbox に残る
