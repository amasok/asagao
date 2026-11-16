# write-flow (入り口ごとの書き込みの流れ)

## Purpose

TUI・CLI・MCP サーバーの 3 つの入り口が、書き込みを受けてからローカルに反映し、outbox に積み、送り、結果を知らせるまでの流れの基準。入り口ごとの違い (タスク作成の欄、名前の解決、期日の解釈、知らせ方) も、今の振る舞いとして書く。構成を移す段階 a (アプリケーション層) では、この仕様を変えずにユースケースを 1 か所にまとめる。今の実装は `internal/tui` (write.go、edit.go ほか)、`internal/cli` (write.go、project.go)、`internal/mcpserver` (server.go、upload.go)。

## Requirements

### Requirement: 書き込みはローカルが先
どの入り口の書き込みも、まず store の `*Local` 関数でキャッシュに反映して outbox に積み、そのあとで送る (MUST)。送れなくても、ローカルの変更は残る。タスクの削除は、どの入り口にもない。
根拠: internal/tui/write.go:353-372; internal/cli/write.go:80-199; internal/mcpserver/server.go:464-513, :720-765; TestWriteCommandsOfflineQueue, TestWritesOfflineAreQueued

#### Scenario: オフラインで CLI からタスクを作る
- **WHEN** オフラインで `task create` を実行する
- **THEN** タスクはキャッシュにローカル gid で入り、`create_task` が outbox に積まれ、「オフラインのため未送信。次の sync で送ります」と出る

### Requirement: 入り口ごとの送り方
CLI と MCP は、書き込みのあとすぐに `Syncer.Push` を 20 秒の期限で呼ぶ (MUST)。TUI は書き込みのあと同期全体 (`Syncer.Run`) を頼み、同期中なら終わってからもう一度行う。push のロックが取れないときは 5 秒から倍にして 1 分まで待って試し直す。読み取り専用の MCP サーバー (`--read-only`) は書き込みのツールを出さず、送らない。TUI の `--offline` は送らない。
根拠: cli/write.go:80-89; mcpserver/server.go:96-105, :720-793; tui/write.go:99-114, :178-190; tui/model.go:393-423, :608-614; TestWriteCommandsOnlineSend, TestWritesOnlineAreSent, TestReadOnlySyncDoesNotSendOutbox

#### Scenario: 別のプロセスが送信中
- **WHEN** MCP の書き込みのあと、push のロックを別のプロセスが持っている
- **THEN** 書き込みは成功し、`sent` は false、note で送信中だと知らせる (TestPushBusyIsNotOffline)

### Requirement: 送れたかの知らせ方
CLI は、未送信・失敗・競合が 0 で、その変更が捨てられていないときだけ「Asana に送信しました」とし、`--json` では `task_gid`・`comment_gid`・`sent`・`push`・`discarded`・`error` を返す (MUST)。送れたら、ローカル gid ではなく本物の gid を返す。MCP は `task_gid`・`sent`・`note`・`discarded` を返す。TUI はしたことだけを表示し、未送信の数と項目ごとの「未送信」で示す。
根拠: cli/write.go:23-32, :91-131; mcpserver/server.go:309-316, :739-755; tui/view.go:214, :700; TestWriteCommandJSON, TestWriteCommandOnlineConflict

#### Scenario: 送ったが競合した
- **WHEN** CLI の書き込みを送り、競合になる
- **THEN** 「送信できなかった変更があります」と知らせ、`sent` は false

### Requirement: タスク作成の入力
タスク作成の入力は入り口ごとに次のとおり (MUST)。CLI: 名前・親・プロジェクト・セクション・期日 (`--due`)・担当・説明。MCP: CLI と同じ欄と `tags` (タグは作成のあとに別のトランザクションで付ける)。TUI: 名前と、入力の中の `due:` (最後のものを使う)。名前が空なら store が「タスク名が空です」で断る。
根拠: cli/write.go:166-199; mcpserver/server.go:227-236, :464-513; tui/write.go:334-409; store/outbox.go:261-262; TestCreateTaskAssignee, TestCreateSubtaskAndAssignByName, TestCreateTaskInProject, TestParseCreateInput

#### Scenario: TUI で期日付きのタスクを作る
- **WHEN** TUI の作成で `買い物 due:tomorrow 牛乳` と入力する
- **THEN** 名前は「買い物 牛乳」、期日は明日になる

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
期日は `dates.Parse` で解釈する (MUST)。`YYYY-MM-DD`、`today`/`今日`・`tomorrow`/`明日`・`明後日`・`yesterday`/`昨日`、`+Nd`/`-Nd` (符号と d の間は数字だけ) を受け付け、空なら空にする。解釈できなければ「日付の形式が不正です」のエラーにし、何も積まない。エラーの包み方は入り口ごとに違う: CLI は `--due:`、MCP は `due_on:`、TUI のエディタは `due:`、TUI の作成と期日の選択は包まない。
根拠: internal/dates/dates.go:16-46; tui/write.go:404; tui/edit.go:269; tui/pickers.go:83; cli/task.go:213; mcpserver/server.go:472, :548; TestParse, TestEditTaskErrorsAndNoChange

#### Scenario: MCP で不正な期日を渡す
- **WHEN** MCP の `update_task` に `due_on: "来週のどこか"` を渡す
- **THEN** `due_on:` で始まるエラーを返し、キャッシュも outbox も変わらない

### Requirement: 添付のアップロードの制限
添付は、大きさの上限 (`MaxUploadSize`) を超えるものを断る (MUST)。MCP は、さらに `UploadPolicy` で許した場所のファイルだけを受け付ける。CLI にはパスの制限がない。
根拠: store/relations.go:232, :262; mcpserver/upload.go:12-61; TestUploadAttachmentTool

#### Scenario: MCP で許していない場所のファイル
- **WHEN** MCP の `upload_attachment` に、許していないフォルダのファイルを渡す
- **THEN** エラーを返し、outbox に積まない

### Requirement: プロジェクトの作成とアーカイブはオンラインだけ
プロジェクトの作成・アーカイブ・メンバーの変更は outbox に積まず、その場で Asana に送る (MUST)。
根拠: cli/project.go:130-155; tui/write.go:274; internal/sync/projects.go; TestCreateProjectDroppedConnectionPCClockAhead

#### Scenario: オフラインでプロジェクトを作る
- **WHEN** オフラインで `project create` を実行する
- **THEN** エラーになり、outbox に操作は積まれない

## 未確認

- TUI の期日の選択 (tui/pickers.go:83) の不正な入力を確かめるテスト。
- `TestCreateTaskInvalidWritesNothing` と `TestFailedWritesChangeNothing` が期日の不正を含むか。
- オフラインでのプロジェクト作成で outbox に積まないことを確かめるテスト (コードには outbox に積む経路がない: cli/project.go:130-155)。
