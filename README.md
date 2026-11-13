# asana-tui

ターミナルで Asana のタスクを確認・操作するための TUI / CLI / MCP サーバー。

- 要件定義: [docs/requirements.md](docs/requirements.md)
- 現在の状態: v0.2 開発中（ボード表示・セクション移動/名前変更/折りたたみ・タグ絞り込み・詳細の全画面表示・画像表示）

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

### キー操作

キーは 3 層に整理しています。迷ったら **`space`**（その画面でできる操作のメニュー）か **`?`**（ヘルプ）を押してください。

1. **移動（どの画面でも同じ）**: `j/k` `↑↓` 上下、`h/l` `←→` 閉じる・開く、`g/G` 先頭・末尾、`ctrl+d/u` 半ページ、`enter` 開く・決定、`esc` `backspace` 戻る、`tab` ペイン切り替え、`q` 終了
2. **よく使う操作（どの画面でも同じ）**: `x` 完了、`a` 追加、`e` 編集（エディタ）、`c` コメント、`/` 検索
3. **それ以外は `space` メニュー**: `space` を押すと、画面の下にその場で使える操作が一覧で出ます（which-key 方式）。続けてキーを押すと実行します。

| space のあと | 操作 | 直接押すキー |
|---|---|---|
| **タスク** | | |
| `a` / `s` | タスク / サブタスクを追加 | `a` / `S` |
| `x` | 完了 / 未完了 | `x` |
| `e` | 名前・説明をエディタで編集 | `e` |
| `d` | 期日を変更（一覧から選ぶ） | |
| `u` | 担当者を変更（一覧から選ぶ） | `@` |
| `t` | タグを付ける / 外す（一覧から選ぶ・新規作成） | |
| `m` | セクションを移動 | `m` |
| `c` / `C` | コメント（1 行 / エディタ） | `c` / `C` |
| `L` / `w` | いいね / フォロワーを追加・外す | |
| `P` | ほかのプロジェクトに追加 / 外す | |
| `D` / `A` | 複製 / ファイルを添付 | |
| `i` / `o` | 画像を全画面で見る / ブラウザで開く | `i` / `o` |
| **表示** | | |
| `f` / `T` | 絞り込み / タグで絞り込む（一覧から選ぶ） | `f` / `T` |
| `v` / `z` | 表示を切り替え（リスト→ボード→カレンダー） / 詳細を全画面 | `v` / `z` |
| `-` / `_` / `+` | このセクションを折りたたむ / すべて折りたたむ / すべて開く | 同じ |
| `b` / `<` / `>` | プロジェクト一覧を隠す / 詳細を狭く / 広く | 同じ |
| **プロジェクト** | | |
| `E` | セクション名を変更 | `E` |
| `U` / `W` | ステータス更新を見る / 書く（エディタ） | |
| `N` / `M` / `X` | 新しいプロジェクト / メンバー / アーカイブ（オンラインのみ） | |
| **その他** | | |
| `r` / `!` | 今すぐ同期 / 未送信の変更・競合 | `r` / `!` |

### 画面ごとの動き

- **タスク一覧**: `l` でサブタスクをツリー表示、`h` で閉じる・親へ。`enter` で詳細を全画面で開きます。My Tasks は期日ごと、プロジェクトはセクションごとに表示します。
- **ボード**（`v`）: `h/l` で列、`j/k` でカード、`H/L` でカードを左右の列へ移します。
- **カレンダー**（`v` をもう一度。My Tasks では 1 回）: 期日のあるタスクを月表示します。`h/l` で日、`j/k` で週、`n/p` で月、`.` で今日、`enter` でその日のタスク一覧（選ぶと詳細）、`a` でその日が期日のタスクを追加します。
- **最近の更新**（プロジェクト一覧の 2 番目）: 直近 7 日に変更されたタスクを、新しい順・日付ごとに表示します（Asana の受信箱の代わり。受信箱は API で取得できないため）。
- **詳細**: `j/k` で項目（担当・期日・プロジェクト/セクション・タグ・フォロワー・いいね・状態・親タスク・説明・添付ファイル・サブタスク・「＋追加」）を選び、`enter` で変更します。添付ファイルは `enter` で開きます（画像はビューア、それ以外は OS のアプリ）。担当・期日・セクション・タグは一覧から選びます（期日は「日付を入力…」で自由入力も可）。名前と説明はエディタで編集します。サブタスクは `enter` で開き、`backspace` で戻ります。
- 折りたたんだセクション、詳細ペインの幅、プロジェクト一覧の表示状態は記憶します。

### 見た目とキーの設定

```toml
[ui]
theme = "auto"       # auto（端末の背景に合わせる）/ dark / light
accent = "#7aa2f7"   # 強調色（省略で既定のピンク）

[keys]               # 直接押すキーを変える（操作名は `asana-tui keys` で確認）
complete = "d"
due = ["D", "ctrl+t"]
```

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
| `task move <gid> --section <セクション> [--project]` | 別のセクションへ移動 |
| `task assign <gid> <me\|none\|名前>` | 担当者を変更 |
| `task tag <gid> --add <タグ> --remove <タグ>` | タグを付ける / 外す（ないタグは作る） |
| `task like <gid> [--off]` | いいね / 取り消し |
| `task follow <gid> --add <ユーザー> --remove <ユーザー>` | フォロワーを追加 / 外す |
| `task project <gid> --add <プロジェクト> --remove <プロジェクト>` | ほかのプロジェクトに追加 / 外す |
| `task duplicate <gid> [--name]` | 複製（サブタスク・添付・フォロワーも） |
| `task attach <gid> <ファイル>` | ファイルを添付（100MB まで） |
| `task create <名前> --parent <gid>` | サブタスクを作成 |
| `section rename <セクション> <新しい名前> --project <プロジェクト>` | セクションの名前を変更 |
| `tag list` | タグと、そのタグが付いたタスクの数 |
| `comment add <gid> <コメント>` | コメントする |
| `outbox list` | 未送信の変更・競合・失敗を一覧表示 |
| `outbox resolve <id> --keep local\|remote` | 競合を解決（こちらの値で上書き / Asana の値を採用） |
| `outbox retry <id>` / `outbox discard <id>` | 失敗した変更を再送 / 変更を取り消す |
| `mcp [--read-only]` | MCP サーバーとして起動（stdio） |
| `keys` | キー設定で使える操作名の一覧 |
| `project status <プロジェクト>` / `project status post` | ステータス更新を見る / 投稿する |
| `project create <名前>` / `project archive <プロジェクト> [--undo]` | プロジェクトを作る / アーカイブ（オンラインのみ） |
| `project members <プロジェクト> [--add] [--remove]` | メンバーを表示 / 追加 / 外す（変更はオンラインのみ） |

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
| `list_tasks` | タスク一覧（デフォルトは自分の未完了。プロジェクト・セクション・担当者・期日・期限切れ・タグで絞り込み。`modified_within_days` で最近の更新） |
| `search_tasks` | タスク名・説明・コメントを全文検索 |
| `get_task` | タスク詳細。説明とコメントをマークダウンで返す（サブタスク・タグ・添付込み。オンラインなら最新を取得） |
| `get_attachment` | 添付ファイル。画像は画像として返すので、AI が中身を見られる |
| `list_projects` | プロジェクトとセクション |
| `sync_status` | 最終同期の時刻と、未送信の変更・競合 |
| `sync` | 未送信の変更を送り、Asana の変更を取り込む |
| `create_task` | タスクを作成（`--read-only` では無効） |
| `update_task` | 完了 / 未完了・名前・説明（マークダウン）・期日・担当者・タグ・フォロワー・いいね・所属プロジェクトを変更（`--read-only` では無効） |
| `duplicate_task` / `upload_attachment` | 複製 / ファイルを添付（`--read-only` では無効） |
| `resolve_outbox` | 送れなかった変更を解決（こちらを採用 / Asana を採用 / 再送 / 取り消し。`--read-only` では無効） |
| `move_task` | 別のセクションへ移動（`--read-only` では無効） |
| `rename_section` | セクションの名前を変更（`--read-only` では無効） |
| `list_tags` | タグとタスク数 |
| `get_project` | プロジェクトのセクション・メンバー・ステータス更新 |
| `create_status_update` | ステータス更新を投稿（`--read-only` では無効） |
| `create_project` / `update_project` | プロジェクトを作る / アーカイブ・メンバー（オンラインのみ。`--read-only` では無効） |
| `list_users` | ワークスペースのメンバー（担当者の指定に使う） |
| `add_comment` | コメント（マークダウン。`--read-only` では無効） |

- 読み取りはローカルストアから行うので、高速でオフラインでも動きます。
- 書き込みは TUI・CLI と同じ「未送信の変更」を通ります。オンラインならすぐに送り、結果の `sent` で送れたかどうかが分かります。
- 期日は `YYYY-MM-DD` のほか、`today` / `tomorrow` / `+7d` などで指定できます。
- 説明とコメントはマークダウンで読み書きし、Asana のリッチテキストに変換します。画像は `![画像](asana:<ID>)` で表し、`get_attachment` で中身を見られます。
- TUI でできる操作は、MCP でもすべてできます（表示の切り替えなど画面の操作を除く）。

## 詳細画面

- リストやボードで `enter` を押すと、タスクの詳細を全画面で開きます。
- 詳細では `J` / `K` でサブタスクを選び、`enter` でそのサブタスクを開きます（`backspace` で親に戻る）。`x` で選んだサブタスクを完了、`S` で表示中のタスクにサブタスクを追加します。
- `c`・`@`・`m`・`e`・`C` は、詳細画面では表示中のタスクに対して働きます。

## エディタで編集する

`e` を押すと、タスクを次のようなマークダウンファイルにして `$EDITOR` で開きます。保存して閉じると、変えた項目だけを反映します（オフラインでも可）。

```markdown
---
name: ソファを買う
due: 2026-11-20
assignee: me
completed: false
section: 家具
---

候補は **ニトリ**

![画像](asana:1220507679870013)
```

- 説明はマークダウンから Asana のリッチテキストに変換して送るので、太字・リスト・見出しなどが Asana 側でも保たれます。画像の行は消さずに残してください。
- ファイルの拡張子は `.md` なので、エディタのマークダウン用プラグイン（Neovim の markview.nvim や markdown-preview.nvim など）がそのまま使えます。
- エディタは `config.toml` の `[ui] editor`、`$VISUAL`、`$EDITOR`、nvim、vi の順に探します。

## 画像の表示

タスクの説明やコメントに貼り付けた画像は、本文の中のその位置に表示します。添付ファイルの画像と合わせて `i` で全画面表示もできます（`n` / `p` で切り替え、`o` で OS のビューアで開く、`q` で戻る）。

- 本文中への表示は kitty 方式の「Unicode プレースホルダー」を使います（kitty・Ghostty、herdr 経由も可）。使えない端末では、その位置に `[画像1]` と表示します。
- エディタで編集するとき、画像はダウンロード済みのファイルのパスで書き出すので、snacks.nvim などの画像表示プラグインでも見られます。

- 対応している端末では、ターミナルの中に表示します: iTerm2・WezTerm（iTerm2 方式）、kitty・Ghostty（kitty 方式）
- それ以外の端末や、tmux・zellij などのマルチプレクサの中では、OS の画像ビューア（macOS ならプレビュー）で開きます（マルチプレクサは画像データを通さないことが多いため。herdr は kitty 方式を通すので端末内に表示します）
- 一度表示した画像は `~/.cache/asana-tui/attachments/` に保存し、オフラインでも表示できます
- 自動判定がうまくいかない場合は `config.toml` で指定できます:

```toml
[ui]
image_protocol = "kitty"   # auto / iterm / kitty / open
inline_images = "auto"     # 本文中に画像を表示するか: auto（kitty 方式のとき）/ on / off
```

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
