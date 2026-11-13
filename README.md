# asana-tui

ターミナルで Asana のタスクを確認・操作するための TUI / CLI / MCP サーバー。

- 要件定義: [docs/requirements.md](docs/requirements.md)
- 現在の状態: M5（MCP サーバー）

## インストール（開発版）

```sh
go install github.com/amasok/asana-tui/cmd/asana-tui@latest
```

## セットアップ

### 1. Asana に OAuth アプリを登録する

1. https://app.asana.com/0/my-apps で「Create new app」
2. **OAuth** の設定で Redirect URL を 2 つ追加する
   - `http://localhost:8976/callback`（通常のログイン用。ポートは `auth.redirect_port` に合わせる）
   - `urn:ietf:wg:oauth:2.0:oob`（`--manual` 用。SSH 先などブラウザがない環境向け）
3. **Permission scopes** で「Full permissions」を選ぶ
4. Client ID と Client secret を控える

### 2. 設定ファイルを作る

`~/.config/asana-tui/config.toml`:

```toml
[auth]
client_id = "1234567890"
token_store = "keychain"   # keychain / 1password / file
# redirect_port = 8976

# token_store = "1password" のとき
[auth.onepassword]
vault = "Private"
item = "asana-tui"
# account = "my.1password.com"   # 複数アカウントがある場合

[sync]
completed_days = 14     # 完了済みタスクを何日分保存するか
# all_projects = true   # 参加していないプロジェクトも同期する
```

1Password に保存する場合は、[1Password CLI](https://developer.1password.com/docs/cli/get-started/) をインストールし、デスクトップアプリとの連携を有効にしてください。リフレッシュトークンと Client secret が指定した保管庫のアイテムに保存されます。

### 3. ログインして同期する

```sh
asana-tui auth login     # ブラウザが開く。Client secret を聞かれたら入力
asana-tui sync           # ローカルストアに同期
asana-tui sync status
```

## TUI

```sh
asana-tui            # TUI を起動（バックグラウンドで同期。5 分ごとに自動同期）
asana-tui --offline  # 同期せず、ローカルストアだけで起動
```

左から「プロジェクト / タスク一覧 / 詳細」の 3 ペイン。幅が 100 桁未満の端末では 1 ペインずつ表示します。

| キー | 動作 |
|------|------|
| `j` / `k`, `↓` / `↑` | 上下に移動 |
| `g` / `G` | 先頭 / 末尾 |
| `ctrl+d` / `ctrl+u` | 半ページ移動 |
| `tab` / `shift+tab`, `h` / `l` | ペインを切り替え |
| `enter` | 選択 / 詳細を開く |
| `/` | 検索（全プロジェクトのタスク名・説明・コメント） |
| `esc` | 検索を解除 / 戻る |
| `f` / `F` | 絞り込みを切り替え（未完了 → 7日以内が期限 → 期限切れ → 完了済み → すべて） |
| `o` | ブラウザで開く |
| `r` | 今すぐ同期 |
| `x` | 完了 / 未完了を切り替え |
| `a` | タスクを追加（`牛乳を買う due:明日` のように期日も指定可） |
| `c` | コメントを追加 |
| `!` | 未送信の変更・競合を確認して解決 |
| `?` | ヘルプ |
| `q` | 終了 |

- My Tasks は期日ごと（期限切れ / 今日 / 7日以内 / それ以降 / 期日なし）、プロジェクトはセクションごとに表示します。
- タスクを選ぶと、オンラインならコメントとサブタスクを自動で取得します。

## コマンド

| コマンド | 内容 |
|----------|------|
| `auth login [--manual] [--no-browser]` | OAuth でログイン |
| `auth status` | ログイン状態 |
| `auth logout` | 保存したトークンを削除 |
| `auth migrate --to <store>` | トークンの保存先を移す |
| `sync [--full]` | Asana からローカルストアへ同期（通常は前回以降の変更分。24 時間ごとに自動で全件） |
| `sync status` | ローカルストアの状態 |
| `task list` | タスク一覧（デフォルトは自分の未完了タスク） |
| `task show <gid> [--offline]` | タスク詳細（サブタスク・コメント込み）。オンラインなら最新を取得 |
| `search <キーワード>...` | タスク名・説明・コメントを全文検索 |
| `project list [--all]` | プロジェクトとセクションの一覧 |
| `task complete <gid>...` / `task reopen <gid>...` | 完了にする / 未完了に戻す |
| `task create <名前> [--project] [--section] [--due] [--assignee] [--notes]` | タスクを作成（プロジェクト未指定なら自分に割り当て） |
| `comment add <gid> <コメント>` | コメントする |
| `outbox list` | 未送信の変更・競合・失敗を一覧表示 |
| `outbox resolve <id> --keep local\|remote` | 競合を解決（こちらの値で上書き / Asana の値を採用） |
| `outbox retry <id>` / `outbox discard <id>` | 失敗した変更を再送 / 変更を取り消す |
| `mcp [--read-only]` | MCP サーバーとして起動（stdio） |

`task list` と `search` の絞り込み:

```sh
asana-tui task list --due-before +7d          # 7 日以内に期限
asana-tui task list --overdue                 # 期限切れ
asana-tui task list --project 新居 --section 家具 --assignee any
asana-tui task list --tag 日用品 --all        # 完了済みも含む
asana-tui search 転居 届 --json               # 複数語は AND
```

日付は `YYYY-MM-DD` / `today` / `tomorrow` / `yesterday` / `+Nd` / `-Nd` で指定できます。

すべてのコマンドで `--json` を付けると JSON で出力します。

## MCP サーバー（AI から使う）

Claude Code などの MCP クライアントから、ローカルストアの検索や、タスクの作成・完了・コメントができます。

```sh
claude mcp add asana -- asana-tui mcp               # 読み書き
claude mcp add asana -- asana-tui mcp --read-only   # 読み取りだけ
```

| ツール | 内容 |
|--------|------|
| `list_tasks` | タスク一覧（デフォルトは自分の未完了。プロジェクト・セクション・担当者・期日・期限切れ・タグで絞り込み） |
| `search_tasks` | タスク名・説明・コメントを全文検索 |
| `get_task` | タスク詳細（サブタスク・コメント込み。オンラインなら最新を取得） |
| `list_projects` | プロジェクトとセクション |
| `sync_status` | 最終同期の時刻と、未送信の変更・競合 |
| `sync` | 未送信の変更を送り、Asana の変更を取り込む |
| `create_task` | タスクを作成（`--read-only` では無効） |
| `update_task` | 完了 / 未完了・名前・説明・期日・担当者を変更（`--read-only` では無効） |
| `add_comment` | コメント（`--read-only` では無効） |

- 読み取りはローカルストアから行うので、高速でオフラインでも動きます。
- 書き込みは TUI・CLI と同じ「未送信の変更」を通ります。オンラインならすぐに送り、結果の `sent` で送れたかどうかが分かります。
- 期日は `YYYY-MM-DD` のほか、`today` / `tomorrow` / `+7d` などで指定できます。

## オフライン編集

完了・作成・コメントは、まずローカルストアに反映してから「未送信の変更」として保存し、すぐに Asana へ送ります。オフラインのときは保存しておき、次の同期で送ります。

- 送る前に Asana 側の現在の値を確認します。こちらが変えた項目を Asana 側でも別の値に変えていたら**競合**として送らずに残し、どちらを採用するか選べます（TUI の `!` または `outbox resolve`）。別の項目の変更なら、そのまま送ります。
- オフラインで作ったタスクには仮の ID（`local:...`）を振り、送信後に本物の ID に置き換えます。
- 未送信の変更があるタスクは、同期で Asana の値を取ってきてもローカルの値を優先して表示します。

## 環境変数

| 変数 | 内容 |
|------|------|
| `ASANA_TOKEN` | Personal Access Token。設定されていれば OAuth より優先（開発・ヘッドレス用） |
| `ASANA_CLIENT_ID` | `auth.client_id` を上書き |
| `ASANA_CLIENT_SECRET` | ログイン時に Client secret を聞かずにこれを使う（例: `op run` と組み合わせる） |
| `XDG_CONFIG_HOME` / `XDG_DATA_HOME` | 設定 / データの置き場所 |

## データの置き場所

- 設定: `~/.config/asana-tui/config.toml`
- ローカルストア: `~/.local/share/asana-tui/store.db`（SQLite、権限 600）

## 開発

```sh
go test ./...
go run ./cmd/asana-tui --help
```
