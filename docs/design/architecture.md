# 目標のパッケージ構成と依存の向き

Issue #69 の段階 a〜d が終わったときの形。どの段階でも、移すのはコードの場所と依存の向きだけで、振る舞い (`openspec/specs/`) は変えない。判断の理由は ADR 0003〜0007 にある。

## 層

```
            ┌──────────────────────────────────────────────────────────┐
  入り口    │  tui            cli            mcpserver                   │  画面・コマンド・MCP のツール
            └───────┬──────────────┬──────────────┬──────────────────────┘
                    │              │              │        ▲
                    ▼              ▼              ▼        │ 組み立て (wire) が adapter を作って渡す
            ┌──────────────────────────────────────────┐   │
  アプリ    │  app (ユースケース)      sync (同期)       │   │
            │  ポート (interface) はここで定義する       │   │
            └───────────────┬──────────────────────────┘   │
                            ▼                              │
            ┌──────────────────────────────────────────┐   │
  ドメイン  │  domain/...  標準ライブラリだけ。時刻は引数 │   │
            └──────────────────────────────────────────┘   │
                            ▲ (実装する)                    │
            ┌──────────────────────────────────────────┐   │
  adapter   │  store (SQLite)  asana (HTTP)  auth  tokenstore   │
            │  richtext (goldmark)  config (TOML)  imageview  browser│
            └──────────────────────────────────────────┘
  どこからでも: termsafe (標準ライブラリだけ)、asana/model と readmodel (型だけ)
```

依存してよい向きは、上から下、adapter から domain と asana/model だけ。

## パッケージごとの置き場所

| パッケージ (目標) | 層 | 今 | 使ってよい外部ライブラリ | 中身 |
|---|---|---|---|---|
| `internal/domain/gid` | ドメイン | (store の `LocalPrefix`・`IsLocalGID`) | なし | ローカル gid の判定と作り方 (乱数は引数。型は作らない: gid はどこでも string) |
| `internal/domain/task` | ドメイン | (store の `NewTask`・`TagEdit`・`TagRef`、`validateFields` ほか) | なし | 作るタスクの内容 (`NewTask`)、解決済みのタグの変更 (`TagEdit`)、タスクの欄の検証、承認の状態の決まり |
| `internal/domain/outbox` | ドメイン | (store の `Op`、`safeToResend`、`UpdateTaskLocal` の後半、sync の `sameTaskFields`) | なし | 操作の種類・状態、畳み込み、再送してよいか、照合で同じとみなす条件 |
| `internal/domain/rich` | ドメイン | (richtext の `Target`、`layout()`、入れ子の決まり) | なし | 送り先と、送り先ごとに許すタグ |
| `internal/dates` | ドメイン | 同じ | なし | 期日の解釈 (今も外部依存なし。場所は変えない) |
| `internal/app` | アプリ | **ない** | なし | ユースケース (タスク作成・更新・コメント・status update・タグ・…)、ポート |
| `internal/sync` | アプリ | 同じ | なし (段階 d で x/oauth2 を外す) | 同期のユースケース (push・照合・pull)。Asana とキャッシュはポート経由 |
| `internal/store` | adapter | 同じ | modernc.org/sqlite、x/sys | キャッシュのリポジトリ。SQL とトランザクションだけ |
| `internal/asana` | adapter | 同じ | なし (net/http) | Asana の REST クライアント (腐敗防止層) |
| `internal/asana/model` | 共有の型 | (今は `internal/asana` の中) | なし | `Task`・`Story`・`Project` などの型 (JSON のタグ付き)、`Error` |
| `internal/readmodel` | 共有の型 | (今は `internal/store` の中。a4・a6 で `UserRef`・`CustomFieldView`・`EnumOptionView`、c2 の最初の PR でタスクとプロジェクトの表示の型を移した) | なし | 表示用の読み取りの型 (`TaskSummary`・`TaskDetail`・`CustomFieldView` など)、表示の文言 (`StatusLabel`・`ConflictLines` など)、入り口が判定に使うエラーの値 (`ErrNotFound` など) |
| `internal/auth` | adapter | 同じ | x/oauth2 | OAuth、トークンの供給 |
| `internal/auth/tokenstore` | adapter | 同じ | go-keyring | トークンの保存先 |
| `internal/richtext` | adapter | 同じ | goldmark、x/net/html | Markdown ⇔ rich text の変換。決まりは `domain/rich` を読む |
| `internal/config` | adapter | 同じ | go-toml | 設定ファイル |
| `internal/imageview`, `internal/browser` | adapter | 同じ | x/sys、x/term (imageview だけ。browser は標準ライブラリだけ) | 端末と OS |
| `internal/termsafe` | 共通 | 同じ | なし | 端末の制御文字の除去 |
| `internal/tui` | 入り口 | 同じ | bubbletea、bubbles、lipgloss、glamour、x/ansi | 画面 |
| `internal/mcpserver` | 入り口 | 同じ | MCP go-sdk | MCP のツール |
| `internal/cli` | 入り口 | 同じ | cobra、bubbletea、MCP go-sdk、x/term、go-runewidth | コマンド |
| `internal/wire` | 組み立て | (`internal/cli/root.go` の一部) | なし (adapter を通す) | adapter を作り、app と sync に渡す。adapter を import してよい唯一の場所 |

`domain/rich` などの名前は、その段階の PR で決め直してよい (ADR にする)。

## 依存の決まり

1. **ドメイン** (`internal/domain/...`、`internal/dates`) は、標準ライブラリとほかのドメインのパッケージだけを import する。`time.Now`、`os`、`net`、`database/sql` を使わない。時刻と乱数は引数で受ける。
2. **アプリ** (`internal/app`、`internal/sync`) は、ドメイン・`asana/model`・`readmodel`・`termsafe` と、標準ライブラリだけを import する。外部ライブラリを (間接にも) 使わない。Asana・キャッシュ・時計・rich text の変換は、自分で定義したポートで受ける (送るのは入り口で、app は送らない。ADR 0006・0008)。ポートの引数と戻り値にも、adapter の型を使わない (ドメインか共有の型にする)。
3. **adapter** は、ドメイン・共有の型 (`asana/model`・`readmodel`)・`termsafe`・自分の外部ライブラリを import してよい。アプリと入り口と、ほかの adapter は import しない。例外: `auth` は `auth/tokenstore`・`config`・`browser` を、`auth/tokenstore` は `config` を使ってよい (同じ認証のまとまりと、その設定)。
4. **入り口** は、アプリ・ドメイン・共有の型・`termsafe`・自分の画面のライブラリを import してよい。adapter のうち、次の 4 つだけは入り口から使ってよい:
   - `auth`: 認証のまとまりが公開するもの (`LoginHelp`・`ErrNotLoggedIn`・`ErrLoginReplaced`、CLI のログインのコマンドが使う `Login` など)。
   - `richtext`: **表示のための変換** (html → Markdown: `ToMarkdown`・`ToEditableMarkdownWith`・`Images`・`ImageToken` など)。Markdown → html (`Target.FromMarkdown`・`PlainFromMarkdown`) は、段階 a のあとは app だけが呼ぶ (関数の決まり。`dependency-rules.md`)。
   - `imageview`・`browser`: 端末と OS (画像の表示、ブラウザを開く)。表示の一部なので。
   - `store`・`asana`・`config`・`tokenstore` は、入り口から import しない (組み立てだけ)。
5. **入り口から入り口**: `cli` が `tui` と `mcpserver` を import するのはよい (`cli` は、TUI と MCP サーバーを起動するコマンドの入り口)。ほかの向きはない。
6. **組み立て** (`internal/wire`、`cmd/asagao`) だけが、全部を import してよい。
7. 外部ライブラリは、上の表の「使ってよい外部ライブラリ」の場所でだけ使う。

## 今の import と、それぞれの扱い

`go list -f '{{.ImportPath}}: {{.Imports}}' ./internal/...` の、internal のパッケージ同士の import をすべて並べ、「許す」(上の決まりに合う) か「直す」(依存の向きのテストの許可リストに入れ、段階で消す) かを決めた (main, dac91b2)。

| import | 使っているもの | 扱い | 段階 |
|---|---|---|---|
| cmd/asagao → cli | `Execute` | 許す (組み立て) | — |
| asana → termsafe | 文字の除去 | 許す | — |
| auth → auth/tokenstore・config・browser | 保存先、設定、ログインでブラウザを開く | 許す (決まり 3 の例外) | — |
| auth → asana | `*asana.Error` で 401 の判定 (tokensource.go:56) | 直す | d1 (`asana/model` へ) |
| auth/tokenstore → config | 設定の型、保存先のフォルダ | 許す (決まり 3 の例外) | — |
| store → dates・termsafe | 期日のフィルター、文字の除去 | 許す | — |
| store → asana | `asana.Task` などの型 (9 ファイル) | 直す | d1 |
| store → richtext | `Target.Fit` (outbox_richtext.go。送り待ちのコメントと status update を今の変換で書き直す、#71)。`ToEditableMarkdown` (describe.go) は c1 で呼ぶ側に移した | 直す | d3 (`sync.Push` が変換をポートで受け、`FitQueuedRichText` に渡す) |
| sync → store | `*store.Store`、`store.Op` などの型 | 直す | b2 (Op を domain/outbox へ)、d3 (ポートにする) |
| sync → asana | `*asana.Client`、型 | 直す | d1 (型)、d3 (ポート) |
| sync → auth | なし。送る前の失敗は、auth が `*auth.TokenError` で包み、sync は `RequestNotSent()` のメソッドで見分ける (push.go の `requestNotSent`) | 直した | d2 (済み) |
| sync → config | `SyncConfig`、`CacheDir` | 直す | d3 |
| sync → termsafe | 比べる前の文字の除去 | 許す | — |
| tui → store | `*Local` 15 種類と、読み取りの型・文言・エラー 約 50 種類 (`TaskSummary` 61 か所ほか) | 直す | a (書き込み)、c2 (読み取りの型を readmodel へ) |
| mcpserver → store | `*Local` 13 種類と、読み取りの型 約 20 種類 | 直す | a、c2 |
| cli → store | `*Local` 13 種類、`store.Open`、読み取りの型 約 40 種類 | 直す | a、c2、d4 (`Open` を wire へ) |
| tui・mcpserver・cli → richtext | Markdown → html (11 か所) と、表示の変換 (tui/edit.go:118-125、tui/markdown.go:29-34、mcpserver/server.go:393-398 ほか) | Markdown → html は直す (a5 で済み。archtest の関数の決まりで確かめる)、表示の変換は許す | a1・a4・a5 |
| tui・mcpserver・cli → auth | `LoginHelp`・`ErrNotLoggedIn`・`ErrLoginReplaced`、cli のログインのコマンド | 許す (決まり 4) | — |
| tui・mcpserver・cli → sync | `Syncer.Run`・`Push`・`Resolve`・`Result`・`ErrPushBusy` など | 許す (アプリ層)。ただし `sync.Syncer{...}` の組み立て (5 か所と cli) は直す | d4 |
| tui・cli → imageview、cli → browser | 画像の表示、ブラウザ | 許す (決まり 4) | — |
| tui・mcpserver・cli → dates | 期日の解釈 (6 か所) | import は許す (ドメイン)。呼び出しは app に移した (`app.ParseDue`。archtest の関数の決まりで確かめる) | a1・a5 (済み) |
| cli → tui・mcpserver | 起動 | 許す (決まり 5) | — |
| cli → asana・config・tokenstore | `asana.NewClient`、設定の読み込み、`auth migrate` の保存先 | 直す | d4 (組み立てを wire へ、移す処理を auth へ) |
| cli → termsafe、tui → termsafe ほか | 文字の除去 | 許す | — |

外部ライブラリの「直す」: cli → x/oauth2 (d4)。sync → x/oauth2 は d2 で済んだ。ほかは表のとおり許す。

段階 d が終わると「直す」の行がなくなる。これを #69 の終わりの条件の 1 つにする (ADR 0007)。

## 読み取りの扱い

- 読み取り (一覧、検索、詳細) は、段階 a では今のまま入り口から `store` を呼んでよい。書き込みと違って、同じ手順が入り口ごとに重なっている量が少なく、振る舞いの違いの原因にもなっていない。
- 入り口が読む型は、`asana/model` の型ではなく、**store が定義した表示用の型** (`store.TaskSummary`・`store.TaskDetail`・`store.CustomFieldView`・`store.ProjectView` など) と、表示の文言の関数 (`StatusLabel`・`ConflictLines`・`DescribeOp`・`AccountChangedHelp` など) と、エラーの値 (`ErrNotFound`・`ErrPushBusy`・`ErrUnsentChanges` など)。tui だけで約 50 種類ある。
- 段階 c2 で、これらを標準ライブラリだけの `internal/readmodel` に移す。store は readmodel の型を作って返し、エラーも readmodel の値を返す。store には型の別名をしばらく残し、入り口を少しずつ移す。そのあと `app` に読み取りのポート (例: `TaskQuery`) を置き、入り口は `app` を通して読む。これで入り口から `store` の import がなくなる。
- `asana/model` の型 (ADR 0005) は、キャッシュと同期が Asana の応答を扱うための型で、入り口の表示には readmodel を使う。
