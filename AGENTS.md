# asagao (エージェント向けルール)

Asana をローカルの SQLite にキャッシュして扱う Go 製ツール。TUI (Bubble Tea v2)・CLI (cobra)・MCP サーバー (stdio) の 3 つの入り口がある。オフラインの変更は outbox に積んで、同期 (sync) のときに送る。

> 既存の [CLAUDE.md](./CLAUDE.md) とルールを揃えており、Antigravity / Gemini 系エージェントでも自動読み込みされる共通ルールです。

## 応答・進行ルール

- **ユーザーへの返事は日本語**で簡潔に行う。
- **途中経過をユーザーに中継しない。** サブエージェント同士のやりとりや待機通知は中継せず、ユーザーに話すのは「ユーザーの判断が要るとき」「聞かれたとき」「作業が一通り終わったとき」だけにする。
- **ユーザーに判断を押し付けない。** 同期の状態があいまいなときは Asana を見て自動で照合し、確かめられなければ保留にして次の同期で再確認する。ユーザーに聞くのは、どうしても決められないときだけ。

## パッケージ構成

| パッケージ | 役割 |
|---|---|
| `cmd/asagao` | エントリポイント |
| `internal/asana` | Asana REST クライアント (batch、events、custom fields、dependencies、レート制限) |
| `internal/auth` (`tokenstore`) | OAuth (PKCE)・PAT、トークンの保存先 (keychain / 1Password / ファイル) |
| `internal/domain` | 純粋ドメインロジック (`time.Now`・`os`・外部依存禁止、`archtest` で検証) |
| `internal/store` | SQLite キャッシュ。マイグレーションは `schema.go` の `migrations` 末尾に足すだけ。outbox もここ |
| `internal/sync` | Push (outbox 送信、照合) と Pull (Events API の差分、`modified_since`、全件)。push は flock で排他 |
| `internal/app` | ユースケース層 (入り口共通の書き込み・プラン判定) |
| `internal/tui` / `internal/cli` / `internal/mcpserver` | 3 つの入り口 (Markdown⇔HTML 変換や `*Local` 直呼びは禁止、`archtest` で検証) |
| `internal/richtext` | Asana の `html_text` ⇔ エディタ用 Markdown |
| `internal/termsafe` | 端末の制御文字を取り除く |
| `internal/imageview` | インライン画像 (kitty プレースホルダ)、添付を開く |
| `internal/config` | 設定 (`$XDG_CONFIG_HOME/asagao/config.toml`) |
| `internal/archtest` / `internal/testguard` | アーキテクチャ依存・`TestMain` 隔離の自動検証 |

## 確認コマンド

```sh
go build ./... && go vet ./... && go test -race ./...
```

- 出力を読むときは `| grep -v '^ok\|no test files'` で失敗だけに絞る。
- 仕様を変更したときは `npm run -s spec:validate` も通す。
- CI は PR のときだけ ubuntu で回る (文書のみでは回らない)。CI 枠節約のため、PR はいくつかの作業をまとめて 1 つにする。

## 厳守事項

- **本物の Asana に書き込まない。** テストは `httptest` の偽サーバーを使う。
  - 例外は、本物での確認 (#31・#47) のテスト用ワークスペース (gid `1220646164450270`) だけ。必ず `~/asagao-test/asagao` (設定・データ・トークンを `~/asagao-test` に分けたもの) を通す。
  - 書き込むのは、確認のために作ったもの (前置き付きのプロジェクトとその中のもの、テンプレート・カスタムフィールド・ゴール・ポートフォリオ) だけ。作ったものは最後に消す。
  - 書き込みは無料プランで 150 回/分に収める。ふだんのトークン・keychain・`~/.config/asagao` には触らない。
- **本物の `~/.local/share/asagao/store.db` を作業中ブランチのビルドで開かない。** スキーマが進むと元に戻せない。動作確認時は `XDG_DATA_HOME` と `XDG_CONFIG_HOME` を一時ディレクトリに向ける。
- **マイグレーション** は `internal/store/schema.go` の `migrations` 末尾に足す。並行ブランチで番号が衝突しないよう、取り込み順は親エージェント (リーダー) が管理する。
- **Asana から来た文字列は `termsafe` を通す。** 主な経路: store の driver ラッパー (`termsafe_driver.go`)、JSON 列デコード (`termsafe.Unmarshal`)、CLI の writer。新しく表示やエラーに出す場合も通す。
- **新規パッケージには `testguard` を入れる** (`internal/archtest/guard_test.go` が全パッケージの `TestMain` を強制する)。
- **git**
  - `commit` や `push` は、頼まれたときだけ (worktree 上の作業コミットを除く場合も `push` は絶対に行わない)。
  - `git add -A` は禁止。ファイルを明示的に指定してステージする。
  - 並行作業は `.worktrees/<branch>` に worktree を切る。
  - `git checkout -- .` や `reset --hard` は自分専用の隔離 worktree 内でだけ使う。
- 依存パッケージを追加するときは、理由をコミットメッセージに書く。

## サブエージェントとスキル運用

複数の独立タスクや「設計・実装 / テスト / レビュー」の分離が必要な場合は、トークン効率を意識しつつ適切に並列化する。詳細な役割定義と運用手順は `.agents/skills/` のスキルを参照すること:

- `asagao-team`: 実装 (`asagao-coder`)・攻撃テスト (`asagao-tester`)・独立レビュー (`asagao-reviewer`) の役割定義と並列化ルール
- `land`: `scripts/land.sh` によるブランチ取り込み・検証・PR 作成
- `issue`: GitHub Issue の作成テンプレートと重複確認手順
- `mutation`: 一時 worktree での mutation テスト手順

### トークン・クォータ節約の基本ルール

- **サブエージェントのモデル階層化（5時間制限対策）**: すべてのサブエージェントを最高位モデル (`inherit`) で起動しない。調査・検索・定型テスト・通常レビューは `flash`（単純集計は `flash_lite`）を基本とし、`inherit` / `pro` は複雑な設計・実装・高リスクレビューに絞る。
- **同時稼働の上限と編成の適正化**: 同時稼働は **最大 2 本** を目安にする。3 名フル編成 (`coder` + `tester` + `reviewer`) は `sync` / `store` / マイグレーション / 認証・セキュリティ / 複数層変更に限定し、小規模 Issue は 1〜2 名で完結させる。
- **ポーリング禁止**: バックグラウンドコマンドやサブエージェントは完了通知で自動再開するため、`manage_task(status)` で繰り返し確認しない。
- **並列バッチ・まとめ読み・定期 `/compact`**: 独立したファイル参照・検索は 1 ターンで並列実行し、同じファイルを細切れで読み直さない。PR や Issue 群の区切りでは `/compact`（または新規会話）で親コンテキストの肥大化 (`O(N²)` 消費) を止める。
- **`go test -race ./...` の集約**: 実装・修正ループは変更パッケージと `-run` のみ回し、全パッケージの `-race ./...` は最終確認 (`scripts/land.sh merge` 等) に集約する。
- **差分パス渡しと簡潔な報告**: レビュー担当にはリーダーが差分パッチファイルと変更パス一覧を渡し、サブエージェントの完了報告は 10 行以内の箇条書きに収める。
