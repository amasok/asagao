## ADDED Requirements

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
根拠: internal/sync/templates.go (recoverInstantiateTask, makeProject); TestInstantiateTaskRecovered, TestInstantiateTaskNotFoundAfter5xx, TestInstantiateProjectRecovered, TestInstantiateTaskRefused

#### Scenario: 作られた後に接続が切れた
- **WHEN** instantiateTask が 502 を返したが、Asana にはタスクができている
- **THEN** そのタスクを取り込み、POST をもう一度送らない

### Requirement: テンプレートが無いときは出さない
TUI はテンプレートが 1 つもないとき、またはプランで使えない (402) とき、メニューに「テンプレートから作る」を出さない (MUST)。402 はそのワークスペースのその機能に印を付け (`plan_unavailable:<機能>:<ワークスペースの gid>`)、1 日たったらまた確かめる。一覧が取れたら印を消す。
根拠: internal/tui/templates.go; internal/app/templates.go; internal/sync/templates.go (notePlan); TestTemplatesHiddenWithoutTemplates, TestPlanOffered, TestTaskTemplatesListAndPlan

#### Scenario: プランがテンプレートを持たない
- **WHEN** `GET /task_templates` が 402 を返す
- **THEN** メニューに「テンプレートからタスクを作る」が出ず、24 時間後の読み込みでまた出る
