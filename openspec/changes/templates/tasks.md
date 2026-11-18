# タスク

- [x] asana: テンプレートの一覧、instantiateTask・instantiateProject・プロジェクトの複製、`GET /jobs/{gid}`
- [x] sync: 一覧 (termsafe を通す)、作成とジョブ待ち、作られたものの取り込み、送ったか分からない失敗の照合、402 の印
- [x] app: `TemplatesOffered` (402 の印を 1 日で無視する)
- [x] TUI: メニュー `G`・`B`・`S`、一覧の先読み、空と 402 のときは出さない
- [x] CLI: `template tasks`・`projects`・`new-task`・`new-project`、`project duplicate`
- [x] MCP: `list_task_templates`・`list_project_templates`・`create_task_from_template`・`create_project_from_template`・`duplicate_project`
- [x] README
- [x] `go build ./... && go vet ./... && go test -race ./...`
