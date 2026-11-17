## MODIFIED Requirements

### Requirement: TUI のタスク作成の入力
TUI のタスク作成 (`a`) とサブタスクの作成 (`S`) は、1 行の入力をクイック追加の文法で読む (MUST)。入力の末尾から語を 1 つずつ読み、`@名前` を担当、`#名前` を既存のタグ、期日の形 (`dates.Parse` が読むもの、または `due:<期日>`) を期日にする。それ以外の語にぶつかったら止め、そこまでを名前にする (語の間の空白はそのまま)。最初の語はいつも名前。`\@` `\#` は記号をそのまま名前に入れる。全角の `＠` `＃` は半角と同じに読む。名前の途中に日付の `due:…` があれば、読まなかったことをステータス行に出す。期日がなく、カレンダーを開いているときは、選んでいる日を期日にする (サブタスクでは使わない)。担当を読んだら、作成先の既定 (My Tasks なら自分、プロジェクトなら担当なし) の代わりにする。タグは作成のあとに付ける。ステータス行に、読んだもの (担当・期日・タグ) を出す。
根拠: app/quickadd.go; tui/quickadd.go; tui/write.go (createTask); tui/subtasks.go (createSubtask); TestParseQuickAdd, TestCreateTaskQuickAdd, TestCreateTaskQuickAddAssigneeInMyTasks, TestCreateSubtaskQuickAdd, TestCreateTaskInProject, TestCreateSubtask

#### Scenario: TUI で担当・期日・タグ付きのタスクを作る
- **WHEN** TUI の作成で `牛乳を買う @hanako 明日 #買い物` と入力する (hanako と 買い物 はキャッシュにある)
- **THEN** 名前は「牛乳を買う」、担当は hanako、期日は明日、タグは 買い物 になり、ステータス行に「担当: … / 期日: … / タグ: #買い物」が出る

#### Scenario: TUI で期日付きのタスクを作る
- **WHEN** TUI の作成で `買い物 牛乳 due:tomorrow` と入力する
- **THEN** 名前は「買い物 牛乳」、期日は明日になる。`買い物 due:tomorrow 牛乳` のように途中にある `due:` は読まず、名前に残す

#### Scenario: 名前の途中の語は読まない
- **WHEN** TUI の作成で `明日の準備 #12 の件` と入力する
- **THEN** 名前は「明日の準備 #12 の件」のままで、期日もタグも付かない

### Requirement: 期日の解釈
期日は `dates.Parse` で解釈する (MUST)。`YYYY-MM-DD`、`today`/`今日`、`tomorrow`/`明日`、`明後日`、`yesterday`/`昨日`、`+Nd`/`-Nd` を受け付け、空なら空にする。解釈できなければ「日付の形式が不正です」のエラーにし、何も積まない。エラーの包み方は入り口ごとに違う: CLI は `--due:`、MCP は `due_on:`、TUI のエディタは `due:`、TUI の期日の選択は包まない。TUI の作成 (クイック追加) は、日付として読めない `due:…` をエラーにせず、読めなかったトークンとして名前に残す。
根拠: internal/dates/dates.go:16-46; app/quickadd.go; tui/edit.go:269; tui/pickers.go:83; cli/write.go; mcpserver/server.go:472, :548; TestParse, TestParseQuickAdd, TestEditTaskErrorsAndNoChange, TestCreateTaskInvalidWritesNothing, TestFailedWritesChangeNothing

#### Scenario: MCP で不正な期日を渡す
- **WHEN** MCP の `update_task` に `due_on: "来週のどこか"` を渡す
- **THEN** `due_on:` で始まるエラーを返し、キャッシュも outbox も変わらない

## ADDED Requirements

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
