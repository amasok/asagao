## ADDED Requirements

### Requirement: .asana.json を探す
`.asana.json` は、カレントディレクトリ (MCP ではサーバーを起動したフォルダ) から上に向かって探し、最初に見つかったものを使う (MUST)。`.git` (フォルダでもファイルでも) のあるフォルダより上は探さない。リポジトリの外では、ファイルシステムのルートまで探す。`--no-context` (`asagao mcp --no-context` も) では探さない。
根拠: internal/repoctx/repoctx.go (Find); internal/cli/context.go (repoContext); internal/cli/mcp.go; TestFind, TestFindNoRepository

#### Scenario: リポジトリの下のフォルダから
- **WHEN** リポジトリのルートに `.asana.json` があり、その下のフォルダで `asagao context` を実行する
- **THEN** ルートの `.asana.json` の内容を、キャッシュで引いた名前と gid つきで表示する

### Requirement: .asana.json の検査
`.asana.json` のキーは `project` (gid か名前)・`section` (gid か名前。`project` と一緒に)・`task` (gid) で、どれも省略できる (MUST)。知らないキー・JSON として読めない内容・制御文字・通常のファイルでないもの・64KB を超えるものはエラーにし、ファイルの場所をエラーに入れる。使うコマンドはそこで止まり、何も積まない。
根拠: internal/repoctx/repoctx.go (Load, Parse); TestInvalid, TestLoadRefusesOddFiles, TestLoadRefusesFIFO, TestContextInvalid

#### Scenario: 書き間違い
- **WHEN** `.asana.json` が `{"projet": "新居"}` のとき `task create x` を実行する
- **THEN** `.asana.json` の場所と `projet` を含むエラーになり、何も積まない

### Requirement: CLI の既定に .asana.json を使う
`task create` は `--project` と `--parent` がないとき、`.asana.json` のプロジェクトに作り、`--section` がなければ `.asana.json` のセクションに入れる (MUST)。`task list` は `--project` を指定しないとき、`.asana.json` のプロジェクトで絞る (`--project ""` で全体)。`task show` は gid を省くと `.asana.json` のタスクを表示する。使ったときは、そのファイルの場所をエラー出力に書く (`--json` の出力は変えない)。
根拠: internal/cli/write.go (taskCreateCmd); internal/cli/task.go (taskListCmd, taskShowCmd); TestContextTaskCreate, TestContextTaskList, TestContextShowAndContextCommand

#### Scenario: プロジェクトを指定せずに作る
- **WHEN** `.asana.json` が `{"project": "新居", "section": "完了"}` のとき `task create 棚を買う` を実行する
- **THEN** 新居の「完了」セクションに作る変更を積み、エラー出力に `.asana.json` の場所を書く

### Requirement: asagao context
`asagao context` は、`.asana.json` に書かれた値と、キャッシュで引いた gid・名前 (タスクは完了・期日も) を表示する (MUST)。キャッシュで見つからないものは、理由を添えて表示する (エラーにしない)。`.asana.json` がなければ、そう表示する (`--json` では `found: false`)。
根拠: internal/cli/context.go; internal/app/workcontext.go; TestContextShowAndContextCommand, TestContextNotInCache, TestContextNone

#### Scenario: キャッシュにないプロジェクト
- **WHEN** `.asana.json` が `{"project": "ない", "task": "t9"}` のとき `asagao context` を実行する
- **THEN** 「プロジェクト: ない（…）」「タスク: t9（…）」と理由つきで表示し、エラーにはしない

### Requirement: MCP の get_context
MCP サーバーは、ツール `get_context` で `.asana.json` の内容をキャッシュで引いて返す (MUST)。ないときは `found: false`、不正なときはエラーを返す。
根拠: internal/mcpserver/agent.go (getContext); internal/mcpserver/server.go (ContextDir); TestGetContext, TestGetContextNone, TestGetContextInvalid

#### Scenario: .asana.json のあるリポジトリで
- **WHEN** `.asana.json` が `{"project": "新居", "section": "手続き", "task": "t2"}` のフォルダで起動したサーバーの `get_context` を呼ぶ
- **THEN** プロジェクト・セクション・タスクの gid と名前、タスクの期日を返す

### Requirement: MCP の定型プロンプト
MCP サーバーはプロンプト `standup`・`triage_overdue`・`weekly_review` を持ち、どれも任意の引数 `project` を取る (MUST)。`project` がなければ `.asana.json` のプロジェクト、それもなければ自分のタスク全体を対象にする。本文には今日の日付と、使うツールと引数 (プロジェクトは gid で) を書き、`.asana.json` にタスクがあればそれを挙げる。Asana を変える前にユーザーの了承を得るよう指示し、`--read-only` のサーバーでは提案だけにするよう指示する。知らないプロジェクトはエラーにする。本文は termsafe を通す。
根拠: internal/mcpserver/agent.go; TestPromptsListed, TestPromptScope, TestPromptsReadOnly

#### Scenario: .asana.json のあるリポジトリで週次の振り返り
- **WHEN** `.asana.json` が `{"project": "p1", "task": "t1"}` のフォルダで起動したサーバーから `weekly_review` を引数なしで取る
- **THEN** 本文は `project: "p1"` で list_tasks を呼ぶよう指示し、t1 の名前と gid、ステータス更新の下書きを挙げる

### Requirement: エージェント向けの使い方のブロック
`asagao agent-guide` は、CLAUDE.md や AGENTS.md に貼る Markdown のブロックを表示する (MUST)。`context --json`・`task list --json`・`--dry-run`・`comment add` の使い方と、書き込み前に `--dry-run` で確かめること、勝手に再送や outbox の操作をしないことを含む。README には同じ文を載せる。
根拠: internal/cli/agentguide.go; README.md (AI エージェントから使う); TestAgentGuideInReadme

#### Scenario: CLAUDE.md に足す
- **WHEN** `asagao agent-guide >> CLAUDE.md` を実行する
- **THEN** README の「CLAUDE.md に貼るブロック」と同じ文が足される
