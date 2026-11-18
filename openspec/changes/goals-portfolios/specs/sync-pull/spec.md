## ADDED Requirements

### Requirement: ゴールとポートフォリオは表示するときに取る
ゴール・期間・ポートフォリオは同期では取らず、表示するとき (TUI で開いたとき、CLI `goal`・`portfolio`、MCP `list_goals`・`get_goal`・`list_portfolios`・`get_portfolio`) にオンラインなら取り直してキャッシュし、表示はキャッシュから行う (MUST)。402 以外の失敗はキャッシュを残してエラーを返す。期間だけが取れなかったときもゴールは置き換える。一覧から消えたゴールはキャッシュからも消し、1 件を取って 404 ならそのゴール (ポートフォリオ) を消す。
根拠: internal/sync/goals.go; TestFetchGoalsAndGoal, TestGoalsFailuresKeepTheCache, TestFetchGoneGoal, TestFetchPortfolios

#### Scenario: 期間だけが取れない
- **WHEN** `GET /goals` は答え、`GET /time_periods` が 500 を返す
- **THEN** ゴールはキャッシュに入り、期間は前のものが残り、エラーが返る

### Requirement: プランにない機能は 1 日隠して、また試す
`GET /goals` などが 402 を返したら、`plan_unavailable:<機能>:<ワークスペースの gid>` (readmodel.PlanKey) に時刻を記録し、そのワークスペースでその機能を 1 日 (PlanRecheckAfter) 隠して問い合わせない (MUST)。1 日たったら再び出して試し、答えが返れば記録を消す。402 はエラーにせず、読む側が「プランで使えない」と伝える。
根拠: internal/sync/plan.go; internal/readmodel/plan.go; TestGoals402HidesThemForADay, TestGoals402IsPerWorkspace, TestPlanOffFor

#### Scenario: お試しを始めたワークスペース
- **WHEN** ゴールが 402 で隠れてから 1 日たち、Asana がゴールを返すようになっている
- **THEN** TUI のメニューにゴールが戻り、開くとゴールが取れて、402 の記録が消える

#### Scenario: 無料のワークスペースの 402
- **WHEN** 別のワークスペース (無料) でゴールが 402 を返した記録がある
- **THEN** 既定のワークスペース (有料の組織) ではゴールを隠さない。ワークスペースが消えれば、その記録も消える

#### Scenario: 読めない記録
- **WHEN** 記録が時刻として読めない、または未来の時刻である
- **THEN** 隠さずに試す
