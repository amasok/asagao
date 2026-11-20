# タスク

- [x] asana: `Goals`・`Goal`・`GoalRelationships`・`TimePeriods`・`Portfolios`・`Portfolio`・`PortfolioItems`
- [x] store: マイグレーション、ゴール・期間・ポートフォリオの保存と読み出し、名前の解決、ワークスペースの片付けと初期化
- [x] sync: `FetchGoals`・`FetchGoal`・`FetchPortfolios`・`FetchPortfolio`、402 の記録と 1 日後の再試行
- [x] app: `Goals` (期間での絞り込み、402 のときの `ErrPlanUnavailable`)
- [x] TUI: ゴール・ポートフォリオの画面 (`Q`・`l`)、402 でメニューから隠す
- [x] CLI: `goal list`・`goal show`・`goal periods`・`portfolio list`・`portfolio show`
- [x] MCP: `list_goals`・`get_goal`・`list_portfolios`・`get_portfolio`
- [x] `go build ./... && go vet ./... && go test -race ./...`
