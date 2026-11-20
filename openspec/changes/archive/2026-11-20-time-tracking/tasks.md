# タスク

- [x] domain: 時間の読み書き (`timetrack`)、outbox の種類、二度送ってはいけない作成
- [x] asana: `TimeEntries`・`TimeEntry`・`CreateTimeEntry`・`UpdateTimeEntry`・`DeleteTimeEntry`
- [x] store: `time_entries` (マイグレーション)、ローカルの書き込み、取り直しのあとの再適用、取り消し、作成の記録
- [x] sync: 取得 (`FetchTimeEntries`)、送信、作成の照合、402 の記録
- [x] app: `TimeTracking`
- [x] TUI / CLI / MCP、README
- [x] `go build ./... && go vet ./... && go test -race ./...`
