# タスク

- [x] asana: `SearchTasks`、`TypeaheadTasks` / `TypeaheadUsers` / `TypeaheadTags`、`TaskByCustomID` (httptest のテスト)
- [x] sync: `SearchTasks` (402 で typeahead に切り替えて覚える)、`FindUsers` / `FindTags`、`TaskByCustomID`
- [x] store: `TaskFilter.GIDs`、`AddUsers`、`AddTags`
- [x] app: `SearchWithAsana` / `WithFound`、`ReadQuickAddFinding`、`Tasks.Names`、`ResolveCustomIDs`、`GitLink.CustomIDs`
- [x] domain/commitref: `asana#<custom ID>`
- [x] CLI: `search --remote`、assign・create の名前、`git scan` の custom ID
- [x] TUI: `space g`、クイック追加の名前
- [x] MCP: `search_tasks` の `remote`、`create_task` / `update_task` の担当
- [x] README
- [x] `go build ./... && go vet ./... && go test -race ./...`
