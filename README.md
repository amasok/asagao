# asana-tui

ターミナルで Asana のタスクを確認・操作するための TUI / CLI / MCP サーバー。

- 要件定義: [docs/requirements.md](docs/requirements.md)
- 現在の状態: M1（OAuth ログイン、ローカルストアへの同期）

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

## コマンド

| コマンド | 内容 |
|----------|------|
| `auth login [--manual] [--no-browser]` | OAuth でログイン |
| `auth status` | ログイン状態 |
| `auth logout` | 保存したトークンを削除 |
| `auth migrate --to <store>` | トークンの保存先を移す |
| `sync` | Asana からローカルストアへ同期 |
| `sync status` | ローカルストアの状態 |

すべてのコマンドで `--json` を付けると JSON で出力します。

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
