# AI エージェント向けの強化 (Issue #3)

振る舞いを変える変更 (足すだけ。今のコマンドの振る舞いは、`.asana.json` がなければ変わらない)。

## なぜ

Claude Code などのエージェントに asagao を使わせるとき、(1) どのプロジェクト・タスクの作業かを毎回伝える必要があり、(2) 書き込みを試しに実行して確かめる手段がなく、(3) スタンドアップや期限切れの整理のような決まった頼み方を毎回書き、(4) 使い方をエージェントに教える文を各自で書いていた。

## 何を変えるか

- `.asana.json` (`{"project": …, "section": …, "task": …}`、どれも省略可): カレントディレクトリから上に向かって探す。`.git` のあるフォルダ (リポジトリのルート) より上は探さない。知らないキーや読めない内容はエラーにする。
  - CLI: `asagao context` で、書かれた内容をキャッシュで引いて表示。`task create` は `--project` と `--parent` がないとき `.asana.json` のプロジェクト (と、`--section` がなければセクション) に作る。`task list` は `--project` がないとき `.asana.json` のプロジェクトで絞る。`task show` は gid を省くと `.asana.json` のタスク。使ったときはエラー出力にファイルの場所を書く。`--no-context` で使わない。
  - MCP: ツール `get_context`。サーバーを起動したフォルダから探す (`asagao mcp --no-context` では使わない)。
- 書き込み系の CLI すべてに `--dry-run`: ローカルストアを一時フォルダに写し (`VACUUM INTO`)、写しの上で同じ処理をして、写しの outbox に積まれたものを表示する。写しは終わったら消す。Asana に送らず、本物のストア・outbox・添付のキャッシュに書かない。オンラインだけの操作 (`project create` / `archive` / `members`) は、引数を引いたうえで行うはずの操作を表示する。`outbox resolve` / `retry` / `discard` は写しの上で解決して表示する。
- MCP の定型プロンプト `standup`・`triage_overdue`・`weekly_review`。引数 `project` (省くと `.asana.json` のプロジェクト、それもなければ自分のタスク全体)。変更は了承を得てから行うよう指示する。`--read-only` では提案だけにするよう指示する。
- `asagao agent-guide`: CLAUDE.md や AGENTS.md に貼る使い方のブロックを表示する。README に同じ文を載せる (テストで一致を確かめる)。
- マイグレーションはない。

## やらないこと

- MCP の `create_task` / `list_tasks` は `.asana.json` を既定に使わない (エージェントが `get_context` で読んで、引数で渡す)。
- `search` / 保存した検索 / TUI は `.asana.json` を使わない。
- `searches save` / `rm` (searches.toml を書く)、`sync`、`reset`、`auth` には `--dry-run` を付けない。
- `api` コマンド (#9) と git 連携 (#8) の書き込みへの `--dry-run` は、それぞれの取り込みのあとに足す。
- タスクの gid の代わりに `.asana.json` のタスクを指す記法 (`.` など) は足さない。
