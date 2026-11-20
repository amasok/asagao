# コンテキストマップ

asagao を、言葉と決まりがまとまる単位 (境界づけられたコンテキスト) に分け、その間の関係と、翻訳する場所を書く。仕様 (`openspec/specs/`) はこの単位に合わせて分けてある。

ここに書くのは今の形 (Issue #69 の段階 a〜d と #11 の段階 c2 のあと)。パッケージの依存の決まりは [dependency-rules.md](dependency-rules.md) にあり、`internal/archtest` が確かめる。

## 一覧

| コンテキスト | 何を決めるか | 今のパッケージ | 仕様 |
|---|---|---|---|
| **Asana** (外部) | Asana の REST API・Events・rich text のタグの決まり | (外部のサービス) | — |
| **Asana 連携** (腐敗防止層) | HTTP、再試行、任意のリミッター、batch、Events の token、Asana の JSON の型 | `internal/asana` (型は `internal/asana/model`) | sync-pull (再試行) |
| **認証** | ログイン、トークンの更新と保存、保存先の選び方 | `internal/auth`, `internal/auth/tokenstore`, `internal/cli/auth.go` | auth-token |
| **ローカルキャッシュ** | キャッシュの表、開き方・移行・排他・初期化、取り込みのときの当て直し | `internal/store` (表示用の型は `internal/readmodel`) | store-lifecycle |
| **送信待ち** (中心) | 操作の種類、ローカル gid、畳み込み、再送してよいか、捨てた変更、送った記録 | 決まり: `internal/domain/outbox`, `internal/domain/gid` / 表とトランザクション: `internal/store` (outbox*.go ほか) / 利用者の解決: `internal/app/outbox.go` | outbox-push |
| **同期** | 送る順番と結果の扱い、batch でまとめる、照合、取り込みの取り方と記録の進め方、片付け | `internal/sync` (使う相手は `ports.go` のインターフェース) | sync-pull, outbox-push |
| **説明文** | Markdown ⇔ rich text、送り先ごとの制限、入れ子の決まり | 決まり: `internal/domain/rich` / 変換: `internal/richtext` | rich-text |
| **タスクの操作** (中心) | タスク作成・更新・コメント・status update・削除と付け替えの手順。入力の解釈 (期日、名前の解決、クイック追加) | `internal/app` (ユースケース)、決まりは `internal/domain/task`・`project`・`tag`・`timetrack`・`plan` | write-flow |
| **Asana で探す** | キャッシュにないタスク・ユーザー・タグを Asana で探してキャッシュに入れる | `internal/sync/search.go`, `internal/app/remotesearch.go` | remote-search |
| **git 連携** | コミットメッセージのタスク参照、コメントと完了、二重処理の防止 | `internal/domain/commitref`, `internal/app/gitlink.go`, `internal/cli/git.go` | git-link |
| **エージェント向け** | `.asana.json` の作業の文脈、`--dry-run`、MCP の定型プロンプト | `internal/repoctx`, `internal/app/workcontext.go`, `internal/cli/dryrun.go`, `internal/mcpserver/agent.go` | agent-support |
| **表示** (入り口) | 画面・コマンド・MCP のツール。利用者への知らせ方。送り方 (ADR 0008) | `internal/tui`, `internal/cli`, `internal/mcpserver` (日本語の表示名は `internal/ui/format`) | write-flow |
| 組み立て | アダプター (store・Asana クライアント・トークンの保存先) を作って渡す | `internal/wire` | — |
| 共通の道具 | 端末の制御文字の除去、期日の解釈、設定、置き場所 | `internal/termsafe`, `internal/dates`, `internal/config`, `internal/settings`, `internal/xdg` | — |

「中心」と書いた 2 つ (送信待ち、タスクの操作) が、このアプリがほかの Asana クライアントと違うところ (オフラインで書けて、自動で照合する) で、設計の手間をかける場所。

## 関係の図

```
                    ┌───────────────────────── 表示 (入り口) ─────────────────────────┐
                    │   TUI (tui)            CLI (cli)            MCP サーバー (mcpserver) │
                    └──────┬────────────────────────┬──────────────────────────┬─────┘
                           │ 書き込み: app のユースケース / 読み取り: app.Query  │ 送る: sync (入り口が呼ぶ)
                           ▼                                                  │
   ┌──────────────────── タスクの操作 (app) ─────────────────────┐            │
   │  入力の解釈 (dates)・名前の解決・送り先を決めた変換・積む   │            │
   │  決まりは domain (task・project・tag・timetrack・plan)       │            │
   └──────┬───────────────────────┬───────────────────┬──────────┘            │
          │ 顧客/供給者 (app のポート)                  │ RichText ポート       │
          ▼                       ▼                    ▼                      │
   ┌─ 送信待ち ─┐  同じパッケージ  ┌─ ローカルキャッシュ ─┐  ┌─ 説明文 ─────────┐  │
   │ (store の   │◀──────────────▶│ (store)               │  │ richtext         │  │
   │  outbox)    │   共有の核      │ 表示の型は readmodel  │  │ 決まりは          │  │
   │ 決まりは    │                └──────────┬────────────┘  │ domain/rich       │  │
   │domain/outbox│                           │ 順応者 (asana/model の型を表に写す) └──┘  │
   └──────┬──────┘                           │                                        │
          ▲                                  ▲                                        │
          │ LocalStore ポート                 │                                        │
   ┌──────┴────── 同期 (sync) ───────────────┴──────────────────┐◀────────────────────┘
   │ push (batch でまとめる)・照合・pull。AsanaAPI ポートで送る    │
   └──────┬────────────────────────────────────────────────────┘
          │ 腐敗防止層 (AsanaAPI ポートを asana.Client が満たす)
          ▼
   ┌──── Asana 連携 (asana) ──────────┐          ┌──── 認証 (auth) ────┐
   │ REST / batch / Events / 再試行     │◀─────────│ トークンの供給        │
   │ 任意のリミッター (Limiter)          │ HTTP クライアント └─────────────┘
   └──────────────┬───────────────────┘
                  ▼
            ┌── Asana (外部) ──┐
            └──────────────────┘

   組み立て (wire): store・asana.Client・sync.Syncer を作って入り口に渡す
```

## 関係ごとの説明

### Asana → Asana 連携: 腐敗防止層 (Anticorruption Layer)

- Asana の JSON は `internal/asana/model` の型 (`model.Task`、`model.Story` など) に読み替える。再試行・batch・Events の token・Date ヘッダーの時計のずれ・任意のリミッター (`Client.Limiter`、送る前に `Wait(ctx)` する) は、`internal/asana` に閉じている。
- 型は HTTP クライアントから分けてあり (`internal/asana/model`、段階 d)、キャッシュ (`internal/store`) と同期 (`internal/sync`) は `internal/asana` を import しない。`model` だけを使う。
- 内側は Asana の型に**順応 (Conformist)** している: キャッシュと同期の型として `model.Task` などをそのまま使う (ADR 0005)。入り口が読む表示用の型は store が返す `internal/readmodel` の型 (段階 c2)。

### 認証 → Asana 連携

- 認証は `oauth2.TokenSource` を作り、組み立ての場所 (`internal/wire/wire.go`) で `asana.NewClient` にする。`asana.NewClient` を呼ぶのは、ここ 1 か所だけ。
- 送る前の失敗: 認証の `Source.Token` は、トークンを得られなかったとき (ログアウト、別のログイン、OAuth の更新の失敗) に自前のエラー `*auth.TokenError` で包んで返す。同期 (`internal/sync/push.go` の `requestNotSent`) は、そのエラーの `RequestNotSent()` のメソッドで「送っていない」と判断し、認証も x/oauth2 も import しない (d2)。

### 表示 → タスクの操作: 顧客/供給者

- 入り口 (`tui`・`cli`・`mcpserver`) は `internal/store` を import しない。書き込みは `internal/app` のユースケース (ADR 0006) を呼び、読み取りは `app.Query` を通す (ADR 0009)。名前の解決・期日の解釈・送り先を決めた rich text への変換・outbox に積むことは、app の中で 1 回だけ書く (write-flow の仕様)。
- 送り方 (すぐ送るか、積むだけか、何秒試すか) は入り口ごとに違うので、ポートにせず入り口に残す (ADR 0008)。入り口は積んだあとで `sync.Syncer` の push を呼ぶ。

### タスクの操作 → 送信待ち・ローカルキャッシュ・説明文

- app は自分の側でポート (`TaskStore`・`CommentStore`・`QueryStore` など) と `RichText` を定義し、`*store.Store` と `richtext.Converter` がそれを満たす。判断の決まり (畳み込み、検証、プランで使えるか) は `internal/domain` の外部依存のない関数で、app と store の両方が使う。

### 送信待ち ⇔ ローカルキャッシュ: 共有の核 (Shared Kernel)

- outbox とキャッシュの表は同じ SQLite のファイルにあり、同じトランザクションで書く必要がある (操作を積むのとローカルに反映するのは同時、取り込みと当て直しも同時、作成の完了と gid の書き換えも同時)。この「同じトランザクション」は #50・#55 の修正の前提で、崩さない。
- だから、2 つは同じリポジトリ (`internal/store`) に置いたままにする。トランザクションの中で行う**判断** (畳み込みの規則、再送してよいか、片付けてよいか) は、`internal/domain/outbox` と `internal/domain/gid` に出してある (段階 b)。

### 同期 → 送信待ち・ローカルキャッシュ・Asana 連携

- 同期は store と Asana 連携を、同期の側で定義したインターフェース (`internal/sync/ports.go` の `LocalStore` と `AsanaAPI`) で持つ (段階 d)。組み立ては `internal/wire` の `Syncer` が行う。
- 残っていること: TUI と MCP は、オフラインで添付・画像のキャッシュだけを読むために `&sync.Syncer{Store: ...}` を自分で作っている (`tui/extras.go`、`tui/edit.go`、`tui/markdown.go`、`tui/images.go`、`mcpserver/server.go`)。

### 説明文

- 送り先ごとの決まり (許すタグ、入れ子の決まり) は Asana の決まり (ドメイン) で、`internal/domain/rich` に外部依存なしで置いてある。goldmark と x/net/html を使う変換 (`internal/richtext`) は、その決まりを読む adapter (ADR 0004)。

## 翻訳する場所のまとめ

| 境界 | 翻訳する場所 |
|---|---|
| Asana の JSON ⇔ 内側の型 | `internal/asana` (型は `internal/asana/model`) |
| Asana のエラー ⇔ 送信の結果 | `internal/sync/push.go` (`handleError`、`isNetworkError`、`requestNotSent`)。oauth2 のエラーは認証の中で `*auth.TokenError` に包む |
| エディタの Markdown ⇔ rich text | 決まり: `internal/domain/rich` / 変換: `internal/richtext` |
| 利用者の入力 (期日・名前・クイック追加) ⇔ 操作 | `internal/app` のユースケース |
| キャッシュの行 ⇔ 型 | `internal/store` (表示用の型は `internal/readmodel`) |
| 操作 ⇔ 日本語の表示名 | `internal/ui/format` |
