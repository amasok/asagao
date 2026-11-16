# asana-tui (Claude 向けメモ)

Asana をローカルの SQLite にキャッシュして扱う Go 製ツール。TUI (Bubble Tea v2)・CLI (cobra)・MCP サーバー (stdio) の 3 つの入り口がある。オフラインの変更は outbox に積んで、同期 (sync) のときに送る。

## パッケージ

| パッケージ | 役割 |
|---|---|
| `cmd/asana-tui` | エントリポイント |
| `internal/asana` | Asana REST クライアント (batch、events、custom fields、dependencies、レート制限) |
| `internal/auth` (`tokenstore`) | OAuth (PKCE)・PAT、トークンの保存先 (keychain / 1Password / ファイル) |
| `internal/store` | SQLite キャッシュ。マイグレーションは `schema.go` の `migrations` (末尾に足すだけ)。outbox もここ |
| `internal/sync` | Push (outbox 送信、照合) と Pull (Events API の差分、modified_since、全件)。push は flock で排他 |
| `internal/tui` | 画面 |
| `internal/cli` | コマンド |
| `internal/mcpserver` | MCP ツール。読み取りはキャッシュから、書き込みは outbox 経由 |
| `internal/richtext` | Asana の html_text ⇔ エディタ用 Markdown |
| `internal/termsafe` | 端末の制御文字を取り除く |
| `internal/imageview` | インライン画像 (kitty プレースホルダ)、添付を開く |
| `internal/config` | 設定 (`$XDG_CONFIG_HOME/asana-tui/config.toml`) |

## 確認コマンド

```sh
go build ./... && go vet ./... && go test -race ./...
```

CI は PR のときだけ ubuntu で `go vet`・`go test`・仕様の検証を回す (文書だけの変更では回らない)。macOS は週に 1 回と手動 (`gh workflow run CI`) だけ。使う分を抑えるため、PR はいくつかの作業をまとめて 1 つにする。出力を読むときは `| grep -v '^ok\|no test files'` で失敗だけに絞る。

## 守ること

- **本物の Asana に書き込まない。** テストは `httptest` の偽サーバーを使う。
- **本物の `~/.local/share/asana-tui/store.db` を、作業中のブランチのビルドで開かない。** スキーマが進んで、元に戻せなくなる。動かして確かめるときは `XDG_DATA_HOME` と `XDG_CONFIG_HOME` を一時ディレクトリに向ける。
- **マイグレーション** は `internal/store/schema.go` の末尾に足す。並行するブランチで番号がぶつかるので、取り込む順番はリーダーが決める。
- **Asana から来た文字列は termsafe を通す。** 主な通り道は 3 つ: store の driver ラッパー (`termsafe_driver.go`)、JSON 列をデコードする場所 (`termsafe.Unmarshal`)、CLI の writer。新しく表示やエラーメッセージに出すときも通す。
- **ユーザーに判断を押し付けない。** 同期の状態があいまいなとき (送れたか分からない、など) は、Asana を見て自動で照合する。確かめられなければ保留にして、次の同期で再確認する。ユーザーに聞くのは、どうしても決められないときだけ。
- **git**
  - commit や push は、頼まれたときだけ。
  - `git add -A` は使わない。ほかの人の未コミットのファイルを巻き込むので、ファイルを指定する。
  - 並行して作業するときは `.worktrees/<branch>` に worktree を切る。
  - `git checkout -- .` や `reset --hard` は、自分専用の worktree の中でだけ使う。
- 依存を足すときは、理由をコミットメッセージに書く。

## チームで作業するとき

役割ごとの決まりは `.claude/agents/` にある (`asana-tui-coder` / `asana-tui-tester` / `asana-tui-reviewer`)。決まった手順はスキルにしてある。

- `/land`: ブランチを取り込み、確認して PR を作る
- `/issue`: Issue を作る
- `/mutation`: テストの強さを確かめる
