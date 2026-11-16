# コンテキストマップ

asana-tui を、言葉と決まりがまとまる単位 (境界づけられたコンテキスト) に分け、その間の関係と、翻訳する場所を書く。仕様 (`openspec/specs/`) はこの単位に合わせて分けてある。

## 一覧

| コンテキスト | 何を決めるか | 今のパッケージ | 仕様 |
|---|---|---|---|
| **Asana** (外部) | Asana の REST API・Events・rich text のタグの決まり | (外部のサービス) | — |
| **Asana 連携** (腐敗防止層) | HTTP、再試行、batch、Events の token、Asana の JSON の型 | `internal/asana` | sync-pull (再試行) |
| **認証** | ログイン、トークンの更新と保存、保存先の選び方 | `internal/auth`, `internal/auth/tokenstore`, `internal/cli/auth.go` | auth-token |
| **ローカルキャッシュ** | キャッシュの表、開き方・移行・排他・初期化、取り込みのときの当て直し | `internal/store` | store-lifecycle |
| **送信待ち** (中心) | 操作の種類、ローカル gid、畳み込み、再送してよいか、捨てた変更、送った記録 | `internal/store` (outbox*.go ほか), `internal/sync/resolve.go` | outbox-push |
| **同期** | 送る順番と結果の扱い、照合、取り込みの取り方と記録の進め方、片付け | `internal/sync` | sync-pull, outbox-push |
| **説明文** | Markdown ⇔ rich text、送り先ごとの制限、入れ子の決まり | `internal/richtext` | rich-text |
| **タスクの操作** (中心) | タスク作成・更新・コメント・status update の手順。入力の解釈 (期日、名前の解決) | 今は入り口ごとに散らばっている (`tui`・`cli`・`mcpserver`) | write-flow |
| **表示** (入り口) | 画面・コマンド・MCP のツール。利用者への知らせ方 | `internal/tui`, `internal/cli`, `internal/mcpserver` | write-flow |
| 共通の道具 | 端末の制御文字の除去、期日の解釈、設定 | `internal/termsafe`, `internal/dates`, `internal/config` | — |

「中心」と書いた 2 つ (送信待ち、タスクの操作) が、このアプリがほかの Asana クライアントと違うところ (オフラインで書けて、自動で照合する) で、設計の手間をかける場所。

## 関係の図

```
                         ┌───────────────────────── 表示 (入り口) ─────────────────────────┐
                         │   TUI (tui)            CLI (cli)            MCP サーバー (mcpserver) │
                         └──────┬───────────────────┬───────────────────────┬──────────────┘
                                │ 今: store の *Local を直接呼ぶ (3 か所に同じ手順)
                                ▼                   ▼                       ▼
        ┌──────────────────── タスクの操作 (段階 a で app 層として作る) ───────────────────┐
        │  入力の解釈 (dates)・名前の解決・送り先を決めた変換 (説明文)・outbox に積む・すぐ送る │
        └──────┬──────────────────────────────┬───────────────────────────┬──────────────┘
               │ 顧客/供給者                   │ 顧客/供給者                │ 顧客/供給者
               ▼                              ▼                           ▼
        ┌─ 送信待ち ─┐  同じパッケージ  ┌─ ローカルキャッシュ ─┐       ┌─ 説明文 ─┐
        │ (store の   │◀──────────────▶│ (store)               │       │(richtext) │
        │  outbox)    │   共有の核      └──────────┬────────────┘       └──────────┘
        └──────┬──────┘                           │ 順応者 (asana.Task などの型をそのまま表に写す)
               │                                  │
               ▼                                  ▼
        ┌──────────── 同期 (sync) ─────────────────────────────────┐
        │ push・照合・pull。 Asana 連携の型を使い、キャッシュに書く   │
        └──────┬───────────────────────────────────────┬───────────┘
               │ 腐敗防止層                              │ (x/oauth2 のエラーの型を直接見る)
               ▼                                        ▼
        ┌──── Asana 連携 (asana) ────┐            ┌──── 認証 (auth) ────┐
        │ REST / batch / Events / 再試行 │◀──────────│ トークンの供給        │
        └──────────────┬────────────────┘  HTTP クライアント └─────────────┘
                       ▼
                 ┌── Asana (外部) ──┐
                 └──────────────────┘
```

## 関係ごとの説明

### Asana → Asana 連携: 腐敗防止層 (Anticorruption Layer)

- Asana の JSON は `internal/asana` の型 (`asana.Task`、`asana.Story` など) に読み替える。再試行・batch・Events の token・Date ヘッダーの時計のずれも、ここに閉じている。
- ただし、腐敗防止層の型 (`asana.Task` など) が、キャッシュ・同期・表示まで、そのまま使われている。`internal/store` は 9 つのファイルで `internal/asana` を import している (attachments.go、customfields.go、dependencies.go、outbox_send.go、projects.go、notifications.go、outbox.go、tags.go、write.go)。つまり、内側は Asana の型に**順応 (Conformist)** している。
- 判断: 読み取りのモデルとしては、この順応を受け入れる (ADR 0005)。Asana の型を自前の型に写し替える手間に見合う利点が、今はない。代わりに、型だけを HTTP クライアントから分けて (`internal/asana/model`)、キャッシュが HTTP のパッケージに依存しないようにする (段階 d)。

### 認証 → Asana 連携

- 認証は `oauth2.TokenSource` を作り、組み立ての場所 (`internal/cli/root.go:91-97`) で `asana.NewClient(oauth2.NewClient(ctx, src))` にする。`asana.NewClient` を呼ぶのは、ここ 1 か所だけ。
- 問題: 同期 (`internal/sync/push.go:614-616`) が `*oauth2.RetrieveError` と `auth.ErrNotLoggedIn`・`auth.ErrLoginReplaced` を直接見て、「送っていない」と判断している。同期が認証のライブラリを知っている。段階 d で、認証が「送る前に失敗した」を表す自前のエラー (例: `auth.ErrNoToken`) に包んで返すようにし、sync から x/oauth2 の import を外す。

### タスクの操作 → 送信待ち・ローカルキャッシュ・説明文: 顧客/供給者

- 今は「タスクの操作」というまとまりがなく、3 つの入り口が直接 `store.*Local` を呼び、`richtext.Target.*FromMarkdown` と `dates.Parse` を呼んでいる (write-flow の仕様)。同じ手順の違いが、入り口ごとの小さな振る舞いの違いになっている (担当の既定、エラーの包み方、タグを付けるトランザクション)。
- 段階 a で、これをアプリケーション層 (`internal/app`) のユースケースにする。入り口は、入力を集めてユースケースを呼び、結果を表示するだけにする。

### 送信待ち ⇔ ローカルキャッシュ: 共有の核 (Shared Kernel)

- outbox とキャッシュの表は同じ SQLite のファイルにあり、同じトランザクションで書く必要がある (操作を積むのとローカルに反映するのは同時、取り込みと当て直しも同時、作成の完了と gid の書き換えも同時)。この「同じトランザクション」は #50・#55 の修正の前提で、崩さない。
- だから、2 つは同じリポジトリ (今の `internal/store`) に置いたままにする。分けるのは、トランザクションの中で行っている**判断** (畳み込みの規則、再送してよいか、片付けてよいか) を、外部依存のないドメインの関数にすること (段階 b)。

### 同期 → 送信待ち・ローカルキャッシュ

- 同期は store の具体的な型 (`*store.Store`) と、Asana 連携の具体的な型 (`*asana.Client`) を持つ (`internal/sync/sync.go:33-34`)。段階 d で、両方を同期の側で定義したインターフェースにする。テストは今も `httptest` の偽サーバーで書けているので、インターフェースにする目的は、依存の向きをはっきりさせることと、`cli` 以外 (TUI・MCP) が `sync.Syncer{...}` を組み立てている 5 か所 (`tui/extras.go:219`、`tui/edit.go:173`、`tui/markdown.go:148`、`tui/images.go:78`、`mcpserver/server.go:838`) を減らすこと。

### 説明文

- 送り先ごとの決まり (`richtext.Target` と `layout()`、入れ子の決まり) は Asana の決まり (ドメイン) だが、goldmark の AST を歩く処理と 1 つのパッケージに混ざっている。決まりを表す部分は小さい (target.go 33 行と、frommd.go の `isBlockElement`・`asText`)。
- 段階 b で、送り先と許すタグの決まりを外部依存のないパッケージに出し、goldmark と x/net/html を使う変換はその決まりを読む adapter にする (ADR 0004)。

## 翻訳する場所のまとめ

| 境界 | 翻訳する場所 (今) | 翻訳する場所 (目標) |
|---|---|---|
| Asana の JSON ⇔ 内側の型 | `internal/asana` | `internal/asana` (型は `internal/asana/model`) |
| Asana のエラー ⇔ 送信の結果 | `internal/sync/push.go` (`handleError`、`isNetworkError`、`requestNotSent`) | 同じ。ただし oauth2 のエラーは認証の中で包む |
| エディタの Markdown ⇔ rich text | `internal/richtext` | 決まり: ドメインのパッケージ / 変換: `internal/richtext` |
| 利用者の入力 (期日・名前) ⇔ 操作 | 入り口ごと (tui・cli・mcpserver) | `internal/app` のユースケース |
| キャッシュの行 ⇔ 型 | `internal/store` | 同じ (段階 c で SQL だけにする) |
