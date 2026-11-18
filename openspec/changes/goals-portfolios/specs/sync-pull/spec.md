## ADDED Requirements

### Requirement: ゴールとポートフォリオは表示するときに取る
ゴール・期間・ポートフォリオは同期では取らず、表示するとき (TUI で開いたとき、CLI `goal`・`portfolio`、MCP `list_goals`・`get_goal`・`list_portfolios`・`get_portfolio`) にオンラインなら取り直してキャッシュし、表示はキャッシュから行う (MUST)。402 以外の失敗はキャッシュを残してエラーを返す。期間だけが取れなかったときもゴールは置き換える。一覧から消えたゴールはキャッシュからも消し、1 件を取って 404 ならそのゴール (ポートフォリオ) を消す。
根拠: internal/sync/goals.go; TestFetchGoalsAndGoal, TestGoalsFailuresKeepTheCache, TestFetchGoneGoal, TestFetchPortfolios

#### Scenario: 期間だけが取れない
- **WHEN** `GET /goals` は答え、`GET /time_periods` が 500 を返す
- **THEN** ゴールはキャッシュに入り、期間は前のものが残り、エラーが返る

### Requirement: プランにない機能は 1 日隠して、また試す
`GET /goals` などが 402 を返したら、`plan_unavailable:goals` (ポートフォリオは `plan_unavailable:portfolios`) に時刻を記録し、その機能を 1 日 (PlanRecheckAfter) 隠す (MUST)。その間は Asana に問い合わせない。1 日たったら TUI のメニューや CLI に再び出し、開いたときにもう一度問い合わせる。答えが返れば記録を消す。時刻として読めない記録や未来の時刻は、隠さない。402 はエラーとして返さず、読む側が「プランで使えない」と伝える。
根拠: internal/sync/goals.go (goalPlanOff, goalPlanRefused, goalPlanGranted); internal/readmodel/store_names.go (PlanOffFor); TestGoals402HidesThemForADay, TestGoalsPlanUnavailable, TestPlanOffFor, TestGoalsScreenOnline402

#### Scenario: お試しを始めたワークスペース
- **WHEN** ゴールが 402 で隠れてから 1 日たち、Asana がゴールを返すようになっている
- **THEN** TUI のメニューにゴールが戻り、開くとゴールが取れて、402 の記録が消える
