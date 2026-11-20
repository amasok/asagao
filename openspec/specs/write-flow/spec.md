# write-flow (入り口ごとの書き込みの流れ)

## Purpose

TUI・CLI・MCP サーバーの 3 つの入り口が、書き込みを受けてからローカルに反映し、outbox に積み、送り、結果を知らせるまでの流れの基準。入り口ごとの違い (タスク作成の欄、名前の解決、期日の解釈、知らせ方) も、今の振る舞いとして書く。構成を移す段階 a (アプリケーション層) では、この仕様を変えずにユースケースを 1 か所にまとめる。今の実装は `internal/tui` (write.go、edit.go ほか)、`internal/cli` (write.go、project.go)、`internal/mcpserver` (server.go、upload.go)。

## Requirements

### Requirement: 書き込みはローカルが先
どの入り口の書き込みも、まず store の `*Local` 関数でキャッシュに反映して outbox に積み、そのあとで送る (MUST)。送れなくても、ローカルの変更は残る。
根拠: internal/tui/write.go:353-372; internal/cli/write.go:80-199; internal/mcpserver/server.go:464-513, :720-765; TestWriteCommandsOfflineQueue (完了・担当・移動・コメント・タグ・セクション名), TestWritesOfflineAreQueued

#### Scenario: オフラインで CLI からコメントする
- **WHEN** オフラインで `comment add t1 本文` を実行する
- **THEN** コマンドは成功し、`add_comment` の操作が `pending` で 1 件積まれる

### Requirement: 入り口ごとの送り方
CLI と MCP は、書き込みのあとすぐに `Syncer.Push` を 20 秒の期限で呼ぶ (MUST)。TUI は書き込みのあと同期全体 (`Syncer.Run`) を頼み、同期中なら終わってからもう一度行う。push のロックが取れないときは 5 秒から倍にして 1 分まで待って試し直す。読み取り専用の MCP サーバー (`--read-only`) は書き込みのツールを出さず、送らない。TUI の `--offline` は送らない。
根拠: cli/write.go:80-89; mcpserver/server.go:720-793; tui/write.go:99-114, :178-190; tui/model.go:393-423, :608-614; TestWriteCommandsOnlineSend, TestWritesOnlineAreSent, TestReadOnlySyncDoesNotSendOutbox

#### Scenario: 別のプロセスが送信中
- **WHEN** MCP の書き込みのあと、push のロックを別のプロセスが持っている
- **THEN** 書き込みは成功し、`sent` は false、note で送信中だと知らせる (TestPushBusyIsNotOffline)

### Requirement: 即時送信の判定
CLI と MCP の `sent` は、push がエラーなしで、outbox の未送信 (`Remaining`)・失敗・競合がどれも 0 で、通知の既読が断られておらず、その変更が捨てられていないときだけ真にする (MUST, #75)。この判定と、作ったタスク・セクション・作業時間のローカル gid を作成が送れたら本物の gid に直すことは `app.PushNow` で行い、入り口は表示だけをする (ADR 0010)。
根拠: app/pushnow.go; TestPushNowOutcome, TestPushNowResolvesGIDs, TestPushNowDropped

#### Scenario: 残りがある
- **WHEN** push がエラーなしで終わったが、outbox に未送信の変更が残っている
- **THEN** `sent` は false

### Requirement: CLI の送れたかの知らせ方
CLI の `--json` は `task_gid`・`comment_gid`・`sent`・`push`・`discarded`・`error` を返す (MUST)。文は、送れたら「（Asana に送信しました）」、ログインしていない・トークンを拒まれたなら「（ログインが必要なため未送信。ログインしたあとの sync で送ります: <ログインの案内>）」、そのほかの push のエラーなら「（オフラインのため未送信。次の sync で送ります: <エラー>）」。
根拠: cli/write.go (reportWrite); TestWriteCommandJSON, TestWriteCommandJSONOnline, TestWriteCommandOnlineConflict, TestCommentAndStatusPostSameAsBeforeA4

#### Scenario: 送ったが競合した
- **WHEN** CLI の書き込みを送り、競合になる
- **THEN** 「送信できなかった変更があります」と知らせ、`sent` は false

#### Scenario: オフラインで作って送れたあと
- **WHEN** ローカル gid のタスクを作る `--json` のコマンドで、送信まで成功する
- **THEN** `task_gid` は Asana の gid になる (TestWriteJSONCreateAndDeletedTask)

### Requirement: MCP と TUI の送れたかの知らせ方
MCP の `sent` は CLI と同じ判定 (`app.PushNow`) で決める (MUST, #75)。未送信の数 (`Remaining`) が残れば `sent` は false で、note でキューに残っていると知らせる。`task_gid` は、送った記録があれば本物の gid にする。捨てた変更があれば、`discarded` と note で知らせる。TUI はしたことだけを表示し、未送信の数と項目ごとの「未送信」で示す。
根拠: mcpserver/server.go (pushTarget); tui/view.go:214, :700; TestWritesOnlineAreSent, TestPushBusyIsNotOffline, TestWriteLeftQueuedIsNotSent

#### Scenario: MCP でオフラインに作ったタスクが送れた
- **WHEN** MCP の `create_task` のあと、push で作成が送れる
- **THEN** `task_gid` は Asana の gid になる (TestWritesOnlineAreSent)

#### Scenario: MCP で作成がキューに残った
- **WHEN** MCP の `create_task` のあと、Asana が作成に 500 を返し、Asana にまだないためキューに残る
- **THEN** `sent` は false で、note でキューに残っていると知らせる (TestWriteLeftQueuedIsNotSent)

### Requirement: タスク作成の入力
タスク作成の入力は、CLI では名前・親・プロジェクト・セクション・期日 (`--due`)・担当・説明、MCP では CLI と同じ欄と `tags` (タグは作成のあとに別のトランザクションで付ける) (MUST)。名前が空なら store が「タスク名が空です」で断る。
根拠: cli/write.go:166-199; mcpserver/server.go:227-236, :464-513; store/outbox.go:261-262; TestCreateTaskAssignee, TestCreateSubtaskAndAssignByName

#### Scenario: 名前が空
- **WHEN** CLI か MCP で、空白だけの名前でタスクを作る
- **THEN** 「タスク名が空です」のエラーになり、outbox に積まない

### Requirement: TUI のタスク作成の入力
TUI のタスク作成 (`a`) とサブタスクの作成 (`S`) は、1 行の入力を、要件「クイック追加の文法」のとおりに読む (MUST)。名前の途中に日付の `due:…` があれば、読まなかったことをステータス行に出す。期日がなく、カレンダーを開いているときは、選んでいる日を期日にする (サブタスクでは使わない)。担当を読んだら、作成先の既定 (My Tasks なら自分、プロジェクトなら担当なし) の代わりにする。タグは作成のあとに付ける。ステータス行に、読んだもの (担当・期日・タグ) を出す。
根拠: tui/quickadd.go; tui/write.go (createTask); tui/subtasks.go (createSubtask); TestCreateTaskQuickAdd, TestCreateTaskQuickAddAssigneeInMyTasks, TestCreateSubtaskQuickAdd, TestCreateTaskInProject, TestCreateSubtask

#### Scenario: TUI で担当・期日・タグ付きのタスクを作る
- **WHEN** TUI の作成で `牛乳を買う @hanako 明日 #買い物` と入力する (hanako と 買い物 はキャッシュにある)
- **THEN** 名前は「牛乳を買う」、担当は hanako、期日は明日、タグは 買い物 になり、ステータス行に「担当: … / 期日: … / タグ: #買い物」が出る

#### Scenario: TUI で期日付きのタスクを作る
- **WHEN** TUI の作成で `買い物 牛乳 due:tomorrow` と入力する
- **THEN** 名前は「買い物 牛乳」、期日は明日になる。`買い物 due:tomorrow 牛乳` のように途中にある `due:` は読まず、名前に残す

### Requirement: クイック追加の文法
クイック追加の文法は、入力の末尾から語を 1 つずつ読み、`@名前` を担当、`#名前` を既存のタグ、期日の形 (`dates.Parse` が読むもの、または `due:<期日>`) を期日にする (MUST)。それ以外の語 (日付として読めない `due:…` も) にぶつかったら止め、そこまでを名前にする (語の間の空白はそのまま)。最初の語はいつも名前。`\@` `\#` は記号をそのまま名前に入れる。全角の `＠` `＃` は半角と同じに読む。
根拠: app/quickadd.go (ParseQuickAdd); TestParseQuickAdd

#### Scenario: 名前の途中の語は読まない
- **WHEN** TUI の作成で `明日の準備 #12 の件` と入力する
- **THEN** 名前は「明日の準備 #12 の件」のままで、期日もタグも付かない

### Requirement: 作成先と担当の決め方
CLI と MCP は、プロジェクトを gid・完全な名前・一意な部分で、セクションを指定があればプロジェクトの中で、なければ最初のセクションで、担当を `me`・`none`・gid・名前で解決する (MUST)。プロジェクトも親もないときは、担当の既定を `me` にし、既定のワークスペースに作る。TUI は今見ているプロジェクトの gid をそのまま使い、セクションは選んでいるタスクのセクションか最初のセクション、担当はプロジェクトの画面では空、それ以外では自分にする。
根拠: cli/write.go:166-199; mcpserver/server.go:464-505; tui/write.go:343-371; store/read.go:452, :544-564; store/outbox.go:264-277

#### Scenario: プロジェクトなしで CLI から作る
- **WHEN** `task create 名前` をプロジェクトも親も付けずに実行する
- **THEN** 担当は自分、ワークスペースは既定のワークスペースになる

### Requirement: 説明とコメントは送り先を決めて変換する
入り口は、エディタの Markdown を、タスクの説明は `TaskNotes`、コメントは `Comment`、status update は `StatusUpdate` で html と plain の組に変換してから store に渡す (MUST)。今この組を作る場所は 11 か所 (TUI 4、CLI 3、MCP 4) ある。
根拠: tui/write.go:388; tui/edit.go:286-287, :334; tui/projects.go:247; cli/write.go:169, :603; cli/project.go:115; mcpserver/server.go:469, :527-528, :701, :965; TestTUICommentAndStatusWithoutLayoutTags, TestCLIRichTextTagsPerTarget, TestMCPRichTextTagsPerTarget

#### Scenario: MCP でコメントに見出しを書く
- **WHEN** MCP の `add_comment` に `# 見出し` を渡す
- **THEN** store に渡る html_text に `<h1>` はなく、太字の段落になる

### Requirement: 期日の解釈
期日は `dates.Parse` で解釈する (MUST)。`YYYY-MM-DD`、`today`/`今日`、`tomorrow`/`明日`、`明後日`、`yesterday`/`昨日`、`+Nd`/`-Nd` を受け付け、空なら空にする。解釈できなければ「日付の形式が不正です」のエラーにし、何も積まない。エラーの包み方は入り口ごとに違う: CLI は `--due:`、MCP は `due_on:`、TUI のエディタは `due:`、TUI の期日の選択は包まない。クイック追加は読めない `due:…` をエラーにしない。
根拠: internal/dates/dates.go:16-46; tui/edit.go:269; tui/pickers.go:83; cli/write.go; mcpserver/server.go:472, :548; TestParse, TestEditTaskErrorsAndNoChange, TestCreateTaskInvalidWritesNothing, TestFailedWritesChangeNothing

#### Scenario: MCP で不正な期日を渡す
- **WHEN** MCP の `update_task` に `due_on: "来週のどこか"` を渡す
- **THEN** `due_on:` で始まるエラーを返し、キャッシュも outbox も変わらない

### Requirement: 添付のアップロードの制限
添付は、大きさの上限 (`MaxUploadSize`) を超えるものを断る (MUST)。MCP は、さらに `UploadPolicy` で許した場所のファイルだけを受け付ける。CLI にはパスの制限がない。
根拠: store/relations.go:232, :262; mcpserver/upload.go:12-61; TestUploadAttachmentTool

#### Scenario: MCP で許していない場所のファイル
- **WHEN** MCP の `upload_attachment` に、許していないフォルダのファイルを渡す
- **THEN** エラーを返し、outbox に積まない

### Requirement: 削除は確かめてから行う
コメントと添付の削除は、送ったあと取り消せないので、入り口ごとに確かめる (MUST, #34)。CLI の `comment delete` と `task detach` は `--yes` がなければ止まる (`--dry-run` では要らない)。TUI は「やめる」を既定にした確認を出す。MCP の `delete_comment`・`delete_attachment`・`update_comment` は破壊的な操作の印を付け、利用者に確かめるよう説明に書く。CLI の `task show` と MCP の `get_task` は、使う gid と、MCP では自分のコメントかどうか (`mine`) を示す。
根拠: cli/write.go, tui/comment_edit.go, mcpserver/server.go; TestCommentEditDeleteDetach, TestCommentEditDryRun, TestRewriteAndDeleteMyComment, TestCommentEditTools

#### Scenario: CLI で --yes なしに削除する
- **WHEN** `asagao comment delete <gid>` を `--yes` なしで実行する
- **THEN** エラーで止まり、outbox に何も積まない

### Requirement: プロジェクトの作成とアーカイブはオンラインだけ
プロジェクトの作成・アーカイブ・メンバーの変更は outbox に積まず、その場で Asana に送る (MUST)。
根拠: cli/project.go:130-230; tui/projects.go:270-280; internal/sync/projects.go; TestProjectCommandsOffline, TestCreateProjectDroppedConnectionPCClockAhead

#### Scenario: オフラインでプロジェクトを作る
- **WHEN** オフラインで `project create` を実行する
- **THEN** エラーになる (TestProjectCommandsOffline)。outbox に積む経路はコードにない

### Requirement: クイック追加で読めないトークン
クイック追加で、読めないトークン (一致するユーザーやタグがない・複数に一致する・2 つ目の担当や期日・`due:` のあとが日付でない・同期前の `@me`) が来たら、そこで読むのを止め、そのトークンとそれより前を名前に残す (MUST)。黙って捨てない。何が読めなかったかと理由を、TUI はステータス行、CLI は出力 (`--json` では `quick.unread` と `quick.unread_reason`) に出す。新しいタグは作らない。表示に出すユーザー名・タグ名・トークンは termsafe を通す。
根拠: app/quickadd.go (ParseQuickAdd, Problem); TestParseQuickAdd, TestQuickAddSummary, TestCreateTaskQuickAddUnread, TestCreateQuickUnread, TestCreateQuickJSON

#### Scenario: 一致するタグがない
- **WHEN** TUI の作成で `カーテンを買う #ない 明日` と入力する (タグ「ない」はない)
- **THEN** 名前は「カーテンを買う #ない」、期日は明日になり、ステータス行に「「#ない」は読めませんでした」と理由が出る。タグは作られない

### Requirement: CLI のクイック追加
CLI の `task create` は、`--quick` を付けたときだけ、名前をクイック追加の文法で読む (MUST)。付けなければ、名前は今までどおりそのまま。`--due` / `--assignee` と同じものを名前でも指定したらエラーにし、何も積まない。読んだものは、作成の知らせの前に「読み取り: …」の行で出す。
根拠: cli/quickadd.go; cli/write.go (taskCreateCmd); TestCreateQuick, TestCreateWithoutQuickKeepsName, TestCreateQuickConflictsWithFlags, TestCreateQuickJSON

#### Scenario: --quick で作る
- **WHEN** `asagao task create --quick 牛乳を買う @partner 2026-12-01 #急ぎ` を実行する
- **THEN** 名前は「牛乳を買う」、担当は partner、期日は 2026-12-01 のタスクを作り、`add_tag` も積む。出力に「読み取り: 担当: … / 期日: 2026-12-01 / タグ: #急ぎ」が出る

### Requirement: CLI の --dry-run は写しの上で実行する
CLI の書き込み系コマンドは `--dry-run` を受け付ける (MUST)。ストアを一時フォルダに写し (`VACUUM INTO`)、写しの上で同じ処理をして、写しの outbox で増えた・変わった・消えた変更を表示し、写しを消す。Ctrl-C で止められても消す。強制終了で残った写しは、1 時間たてば次の `--dry-run` が消す。Asana には何も送らず、本物のストア・outbox・添付ファイルのキャッシュに何も書かない。
根拠: internal/store/copy.go; internal/cli/dryrun.go; internal/cli/write.go:84-104; internal/cli/project.go:53-59; TestCopyToIsIndependent, TestDryRunChangesNothing, TestDryRunCancelled, TestDryRunRemovesStaleCopies, TestDryRunRemovesCopyOnSignal, TestWriteCommandsHaveDryRun

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

### Requirement: テンプレートからの作成と複製はオンラインだけ
テンプレートからのタスク・プロジェクトの作成と、プロジェクトの複製は outbox に積まず、その場で Asana に送る (MUST)。Asana が返すジョブを `GET /jobs/{gid}` で最大 1 分待ち、作られたものをキャッシュに取り込む。終わらなければ作られた分を取り込み、残りは次の同期に任せる (pending)。
根拠: internal/sync/templates.go; TestInstantiateTask, TestInstantiateTaskJobStillRunning, TestTemplateCommandsOffline, TestTemplateToolsOffline, TestTemplatesOffline

#### Scenario: ジョブが終わらない
- **WHEN** instantiateTask のジョブが 1 分たっても in_progress のまま
- **THEN** 作られたタスクを取り込み、pending として返す (エラーにしない)

#### Scenario: オフラインでテンプレートから作る
- **WHEN** Asana に接続できないときに `template new-task` を実行する
- **THEN** エラーになり、outbox に何も積まない

### Requirement: 送ったか分からない作成は照合し、作り直さない
テンプレートからの作成や複製の POST が 5xx や接続切れで失敗したときは、Asana の一覧で、同じ名前で送った後に作られたものを探す (MUST)。見つかればそれを使い、見つからなければ作り直さずに、次の同期の後に確かめるよう伝える。4xx は作られていないので探さない。
根拠: internal/sync/templates.go (foundTask, findMadeTask, makeProject); TestInstantiateTaskRecovered, TestInstantiateTaskNotFoundAfter5xx, TestInstantiateProjectRecovered, TestInstantiateTaskRefused

#### Scenario: 作られた後に接続が切れた
- **WHEN** instantiateTask が 502 を返したが、Asana にはタスクができている
- **THEN** そのタスクを取り込み、POST をもう一度送らない

### Requirement: テンプレートが無いときは出さない
TUI はテンプレートが 1 つもないとき、またはプランで使えない (402) とき、メニューに「テンプレートから作る」を出さない (MUST)。402 はそのワークスペースのその機能に印を付け (`plan_unavailable:<機能>:<ワークスペースの gid>`)、1 日たったらまた確かめる。一覧が取れたら印を消す。
根拠: internal/tui/templates.go; internal/app/templates.go; internal/sync/templates.go (noteTemplatePlan); internal/sync/plan.go; TestTemplatesHiddenWithoutTemplates, TestTemplatesOffered, TestTaskTemplatesPlanOfTheProjectsWorkspace, TestTemplatesMarkOfAnotherWorkspace, TestTaskTemplatesListAndPlan

#### Scenario: プランがテンプレートを持たない
- **WHEN** `GET /task_templates` が 402 を返す
- **THEN** メニューに「テンプレートからタスクを作る」が出ず、24 時間後の読み込みでまた出る

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

### Requirement: 無い選択肢を足して設定する
選択式のフィールドの値を入れるとき、利用者が明示したとき (TUI の「＋ 選択肢を足して設定する…」、CLI の `--add-option`、MCP の `add_missing_options`) は、フィールドに無い名前を選択肢として足し、その値にする (MUST)。既存とみなすのは gid か、名前が大文字小文字を除いて一致するものだけで、部分一致では既存の選択肢を選ばない。multi_enum はほかの選択肢を残す。すべての名前を確かめてから書き込み、だめなら何も積まない。
根拠: app/fields.go (FieldOptions.ParseAdding, SetWithNewOption); app/relations.go (ResolveFieldsAdding); TestNewOptionFromChooser, TestTaskFieldAddOption, TestUpdateTaskAddMissingOptions

#### Scenario: 部分一致する既存の選択肢があるとき
- **WHEN** 選択肢「高」があるフィールドに、`--add-option` で「最高」を設定する
- **THEN** 「最高」が選択肢として足され、そのタスクの値になる

## 未確認

- TUI の期日の選択 (tui/pickers.go:83) の不正な入力を確かめるテスト。
- 名前が空のタスク作成が断られることを、入り口のテストでは確かめていない (コードは store/outbox.go:261-262)。
- オフラインでのプロジェクト作成で outbox が空のままかを確かめるテスト (TestProjectCommandsOffline はエラーになることだけを見ている)。
