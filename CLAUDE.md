# asagao (Claude 向けメモ)

Asana をローカルの SQLite にキャッシュして扱う Go 製ツール。TUI (Bubble Tea v2)・CLI (cobra)・MCP サーバー (stdio) の 3 つの入り口がある。オフラインの変更は outbox に積んで、同期 (sync) のときに送る。

## パッケージ

| パッケージ | 役割 |
|---|---|
| `cmd/asagao` | エントリポイント |
| `internal/wire` | アダプター (store・Asana クライアント・トークンの保存先) と、その上のユースケース (`app.UseCases`)・同期を組み立てて入り口に渡す |
| `internal/tui` / `internal/cli` / `internal/mcpserver` | 3 つの入り口 (Markdown⇔HTML 変換や `*Local` 直呼びは禁止、`archtest` で検証)。MCP は読み取りをキャッシュから、書き込みを outbox 経由で行う |
| `internal/app` | ユースケース層 (入り口共通の書き込み・プラン判定・`Query` の共通の読み取り) |
| `internal/domain` (`outbox`・`task`・`project`・`tag`・`plan`・`rich`・`gid`・`timetrack`・`commitref`) | 純粋ドメインロジック (`time.Now`・`os`・外部依存禁止、`archtest` で検証) |
| `internal/readmodel` | store が表示用に返す DTO の型 |
| `internal/ui/format` | 画面向けの日本語の表示名 (outbox の操作の説明など) |
| `internal/asana` (`model`) | Asana REST クライアント (batch、events、custom fields、dependencies、レート制限と任意のリミッター)。`model` は API が返す型 |
| `internal/auth` (`tokenstore`) | OAuth (PKCE)・PAT、トークンの保存先 (keychain / 1Password / ファイル) |
| `internal/store` | SQLite キャッシュ。マイグレーションは `schema.go` の `migrations` (末尾に足すだけ)。outbox もここ |
| `internal/sync` | Push (outbox 送信、照合) と Pull (Events API の差分、`modified_since`、全件)。push は flock で排他。部品 (`Pusher`・`Puller`・`Details`・`Goals`・`Templates`・`Search`・`Projects`) ごとに小さなポート、`Syncer` がまとめる |
| `internal/richtext` | Asana の `html_text` ⇔ エディタ用 Markdown |
| `internal/termsafe` | 端末の制御文字を取り除く |
| `internal/imageview` | インライン画像 (kitty プレースホルダ)、添付を開く |
| `internal/config` / `internal/settings` | 設定ファイル (`$XDG_CONFIG_HOME/asagao/config.toml`) の読み書きと、設定の型 |
| `internal/xdg` | 設定・データの置き場所 (XDG ベースディレクトリ) |
| `internal/repoctx` | フォルダとプロジェクトを結び付ける `.asana.json` を読む |
| `internal/dates` | 相対・絶対の日付の解釈 (CLI・TUI・MCP 共通) |
| `internal/browser` | URL をブラウザで開く |
| `internal/archtest` / `internal/testguard` | アーキテクチャ依存・`TestMain` 隔離の自動検証 |

## 確認コマンド

```sh
gofmt -l ./cmd ./internal   # 何も出なければよい
go build ./... && go vet ./... && go test -race ./...
```

CI は PR のときだけ ubuntu で `go vet`・`go test`・仕様の検証を回す (文書だけの変更では回らない)。macOS は週に 1 回と手動 (`gh workflow run CI`) だけ。使う分を抑えるため、PR はいくつかの作業をまとめて 1 つにする。出力を読むときは `| grep -v '^ok\|no test files'` で失敗だけに絞る。

- **コミットの前に `gofmt -w` をかける。** CI は gofmt を検査しないので、通っていないファイルがそのまま入ってしまう (PR #81 のあと 10 ファイルで見つかった)。`gofmt -l ./cmd ./internal` が空になってからコミットする。

## 守ること

- **本物の Asana に書き込まない。** テストは `httptest` の偽サーバーを使う。
  - 例外は、本物での確認 (#31・#47) のテスト用ワークスペース (gid `1220646164450270`) だけ。必ず `~/asagao-test/asagao` (設定・データ・トークンを `~/asagao-test` に分けたもの) を通す。
  - 書き込むのは、確認のために作ったもの (前置き付きのプロジェクトとその中のもの、テンプレート・カスタムフィールド・ゴール・ポートフォリオ) だけ。ほかのユーザーのものや既存のものには触らない。作ったものは最後に消す。
  - 書き込みは無料プランで 150 回/分に収める。ふだんのトークン・keychain・`~/.config/asagao` には触らない。
- **本物の `~/.local/share/asagao/store.db` を、作業中のブランチのビルドで開かない。** スキーマが進んで、元に戻せなくなる。動かして確かめるときは `XDG_DATA_HOME` と `XDG_CONFIG_HOME` を一時ディレクトリに向ける。
- **マイグレーション** は `internal/store/schema.go` の末尾に足す。並行するブランチで番号がぶつかるので、取り込む順番はリーダーが決める。
- **Asana から来た文字列は termsafe を通す。** 主な通り道は 3 つ: store の driver ラッパー (`termsafe_driver.go`)、JSON 列をデコードする場所 (`termsafe.Unmarshal`)、CLI の writer。新しく表示やエラーメッセージに出すときも通す。
- **ユーザーに判断を押し付けない。** 同期の状態があいまいなとき (送れたか分からない、など) は、Asana を見て自動で照合する。確かめられなければ保留にして、次の同期で再確認する。ユーザーに聞くのは、どうしても決められないときだけ。
- **git**
  - commit や push は、頼まれたときだけ。
  - `git add -A` は使わない。ほかの人の未コミットのファイルを巻き込むので、ファイルを指定する。
  - 並行して作業するときは `.worktrees/<branch>` に worktree を切る。
  - `git checkout -- .` や `reset --hard` は、自分専用の worktree の中でだけ使う。
- 依存を足すときは、理由をコミットメッセージに書く。
- **エージェントの署名**: `git log`・PR・Issue (コメント含む) で書き込み元が分かるよう、Claude Code は system-reminder 指定の `Co-Authored-By` (`LAND_TRAILERS` 含む) と `🤖 Generated with [Claude Code](https://claude.com/claude-code)` を必ず付ける（Gemini 側は `Co-Authored-By: Gemini (Antigravity) <noreply@google.com>` と `🤖 Generated with Gemini (Antigravity)` を使用）。

## チームで作業するとき

役割ごとの決まりは `.claude/agents/` にある (`asagao-coder` / `asagao-tester` / `asagao-reviewer`)。決まった手順はスキルにしてある。

- **名前付きのチームメイトは、1 Issue につき実装担当 (`asagao-coder`) の 1 人だけ。** テストとレビューは、実装担当が名前を付けないサブエージェント (`subagent_type: asagao-tester` / `asagao-reviewer`) として自分で呼ぶ。サブエージェントは結果を実装担当に返すだけなので、リーダーに待機の知らせが届かない。名前付きのチームメイトは、ターンを終えるたびにリーダーへ待機の知らせを送り、そのたびにリーダーが会話全体を読み直してトークンを使うため (2026-11-20 に決めた)。

- **リーダーは途中経過をユーザーに伝えない。** チームメイトの待機の知らせや、チームメイト同士のやりとりは中継しない。ユーザーに話すのは、ユーザーの判断が要るとき、聞かれたとき、全部終わったときだけ。チームメイトの決められないことは、リーダーが決めて返す。
  - idle_notification やチームメイト同士のやりとりの知らせには、「作業中です」のような 1 行も書かず、テキストを出さずにターンを終える。「出力がなかった」という system の催促が来ても書かない。チームメイトを作った・指示を送った、という知らせも書かない。
- **終わったらすぐ終了する。** 実装担当は、レビューの承認とテストが済んで報告したら終了させる。同時に動かすのは 1〜2 Issue までに抑える。
- **単発作業はチームメイトにしない。** 1 回限りの調査や軽微なレビューなど継続対話が不要な作業は、常駐チームメイトではなく name なしの単発サブエージェント (`Agent`) を使う。
- **分けすぎない。** 1〜2 ファイルの修正や定数・色名・文言追加などの小規模 Issue は、実装担当だけ (またはテストのサブエージェントだけ足す) で済ませる。テストとレビューの両方を呼ぶのは `sync` / `store` / マイグレーション / セキュリティ / 複数層変更に絞る。
- **`go test -race ./...` を重複実行しない。** 実装・レビュー中の確認は変更パッケージと `-run` に絞り、全パッケージの `-race ./...` は `asagao-tester` の最終 1 回または `scripts/land.sh merge` の 1 回に集約する。
- **リーダーのコンテキストを肥大させない。** Issue 群や PR の区切りで `/compact` を行い、長寿命セッションのまま待機通知を受け続けない。

- `/land`: ブランチを取り込み、確認して PR を作る
- `/issue`: Issue を作る
- `/mutation`: テストの強さを確かめる
