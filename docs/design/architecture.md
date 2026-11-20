# 目標のパッケージ構成と依存の向き

Issue #69 の段階 a〜d と、仕上げの e1 が終わったときの形。移行は e1 で終わり、コードはこの形になっている (依存の向きのテストの許可リスト `allowed` は空)。どの段階でも、移したのはコードの場所と依存の向きだけで、振る舞い (`openspec/specs/`) は変えていない。判断の理由は ADR 0003〜0009 にある。

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
  どこからでも: termsafe・xdg (標準ライブラリだけ)、asana/model と readmodel (型だけ)、ui/format (表示の整形)
```

依存してよい向きは、上から下、adapter から domain と asana/model だけ。

## パッケージごとの置き場所

| パッケージ (目標) | 層 | 今 | 使ってよい外部ライブラリ | 中身 |
|---|---|---|---|---|
| `internal/domain/gid` | ドメイン | (store の `LocalPrefix`・`IsLocalGID`) | なし | ローカル gid の判定と作り方 (乱数は引数。型は作らない: gid はどこでも string) |
| `internal/domain/task` | ドメイン | (store の `NewTask`・`TagEdit`・`TagRef`、`validateFields` ほか) | なし | 作るタスクの内容 (`NewTask`)、解決済みのタグの変更 (`TagEdit`)、タスクの欄の検証、承認の状態の決まり |
| `internal/domain/outbox` | ドメイン | (store の `Op`、`safeToResend`、`UpdateTaskLocal` の後半、sync の `sameTaskFields`) | なし | 操作の種類・状態、畳み込み、再送してよいか、照合で同じとみなす条件 |
| `internal/domain/rich` | ドメイン | (richtext の `Target`、`layout()`、入れ子の決まり。#82 で `readmodel/assets.go` を移した) | なし | 送り先と、送り先ごとに許すタグ。画像の決まり (`IsImageName`、貼り付けた画像のリンク `InlineAssets`・`ReplaceInlineAssets`) |
| `internal/domain/plan` | ドメイン | (#73 で `readmodel` から分離) | なし | 402 プラン制限の機能定数、有効期間判定 (`OffAt`・`Window`)、`FeatureOf` (日本語の文言は #82 で `readmodel` の `PlanUnavailableMessage`・`PlanUnavailableError` に移した) |
| `internal/dates` | ドメイン | 同じ | なし | 期日の解釈 (今も外部依存なし。場所は変えない) |
| `internal/app` | アプリ | **ない** | なし | ユースケース (タスク作成・更新・コメント・status update・タグ・…)、ポート |
| `internal/sync` | アプリ | 同じ | なし (x/oauth2 は d2 で外した) | 同期のユースケース (push・照合・pull)。Asana とキャッシュはポート経由 |
| `internal/store` | adapter | 同じ | modernc.org/sqlite、x/sys | キャッシュのリポジトリ。SQL とトランザクション、`sync_state` 生キー規約・`resultPrefix` の隠蔽と型付きアクセサ |
| `internal/asana` | adapter | 同じ | なし (net/http) | Asana の REST クライアント (腐敗防止層) |
| `internal/asana/model` | 共有の型 | 同じ (d1 で `internal/asana` から移した。`internal/asana` に別名。#73 で PUT 値変換とタスク/CF 抽出を移した) | なし | `Task`・`Story`・`Project` などの型 (JSON のタグ付き)、`Error`、`CF*` 定数、`TaskFieldValues`・`CustomFieldValue`・`CustomFieldAPIValue`・`ServerCustomField` |
| `internal/readmodel` | 共有の型 | (#73 で非 DTO 責務を `domain/plan`・`asana/model`・`store`・`ui/format` に分離し、純粋な DTO 層へ整理) | なし | 表示・問い合わせ用の読み取り DTO (`TaskSummary`・`TaskDetail`・`CustomFieldView`・`FilterSpec`・`Discard` など)、型付き `ResultKind`、共通エラー値 (`ErrNotFound`・`ErrPushBusy`・`PlanUnavailableError` など)、保存される値 (`DiscardReasonReset`)。`asana/model` と `domain/plan` を import する |
| `internal/ui/format` | 共通 | (#73 で `readmodel` から分離) | なし | TUI・CLI・MCP・sync が使う日本語の表示フォーマット (`StatusLabel`・`ConflictLines`・`DescribeOp`・`OpLabel`・`DiscardLine`・`TaskBadges`・`AccountChangedHelp` など)。保存される値は置かない。adapter は import しない (#82。破棄した変更の要約は、組み立てが `DescribeChange` を `Store.SetDescribeChange` で渡す) |
| `internal/auth` | adapter | 同じ | x/oauth2 | OAuth、トークンの供給 |
| `internal/auth/tokenstore` | adapter | 同じ | go-keyring | トークンの保存先 |
| `internal/richtext` | adapter | 同じ | goldmark、x/net/html | Markdown ⇔ rich text の変換。決まりは `domain/rich` を読む |
| `internal/config` | adapter | 同じ | go-toml | 設定ファイル |
| `internal/imageview`, `internal/browser` | adapter | 同じ | x/sys、x/term (imageview だけ。browser は標準ライブラリだけ) | 端末と OS |
| `internal/termsafe` | 共通 | 同じ | なし | 端末の制御文字の除去 |
| `internal/settings` | 共有の型 | (d4b で `internal/config` の `Config` などの型と `Default` を移した。config に別名) | なし | 設定の型と既定値。ファイルや TOML は読まない |
| `internal/xdg` | 共通 | (d3 で `internal/config` の `ConfigDir`・`DataDir`・`CacheDir` の決まりを移した) | なし | ツールのフォルダの場所 (XDG)。環境変数とホームディレクトリだけを読む |
| `internal/testguard` | テストの支え | (#53 で足した) | なし | テストの前に HOME と XDG のフォルダを一時フォルダに向ける。テストのあるパッケージは、どれも `TestMain` から `testguard.Main` (または `Run`) を呼ぶ。import してよいのはテストのファイルだけ (archtest で確かめる) |
| `internal/tui` | 入り口 | 同じ | bubbletea、bubbles、lipgloss、glamour、x/ansi | 画面 |
| `internal/mcpserver` | 入り口 | 同じ | MCP go-sdk | MCP のツール |
| `internal/cli` | 入り口 | 同じ | cobra、bubbletea、MCP go-sdk、x/term、go-runewidth | コマンド |
| `internal/wire` | 組み立て | 同じ (d4 で `internal/cli/root.go` から移した。cmd/asagao が `cli.Wiring` で cli に渡す) | なし (adapter を通す) | adapter を作り、app と sync に渡す。adapter を import してよい唯一の場所 |
| `scripts/realcheck` | 共通 (標準ライブラリだけ) | 同じ | なし | 本物の Asana での確認 (#31)。asagao の実行ファイル (CLI・MCP) を外から動かすので、asagao のパッケージは import しない |

`domain/rich` などの名前は、その段階の PR で決め直してよい (ADR にする)。

## 依存の決まり

1. **ドメイン** (`internal/domain/...`、`internal/dates`) は、標準ライブラリとほかのドメインのパッケージだけを import する。`time.Now`、`os`、`net`、`database/sql` を使わない。時刻と乱数は引数で受ける。
2. **アプリ** (`internal/app`、`internal/sync`) は、ドメイン・`asana/model`・`readmodel`・`termsafe` と、標準ライブラリだけを import する。外部ライブラリを (間接にも) 使わない。Asana・キャッシュ・時計・rich text の変換は、自分で定義したポートで受ける (送るのは入り口で、app は送らない。ADR 0006・0008)。ポートの引数と戻り値にも、adapter の型を使わない (ドメインか共有の型にする)。
3. **adapter** は、ドメイン・共有の型 (`asana/model`・`readmodel`)・`termsafe`・自分の外部ライブラリを import してよい。アプリと入り口と、ほかの adapter は import しない。表示の文言 (`ui/format`) も import しない (#82。保存する値はドメインか `readmodel` の値にする)。例外: `auth` は `auth/tokenstore`・`config`・`browser` を、`auth/tokenstore` は `config` を使ってよい (同じ認証のまとまりと、その設定)。
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
| auth → asana | なし (401 の判定は `*model.Error`) | 直した | d1 (済み) |
| auth/tokenstore → config | 設定の型、保存先のフォルダ | 許す (決まり 3 の例外) | — |
| store → dates・termsafe | 期日のフィルター、文字の除去 | 許す | — |
| store → asana | なし (型は `asana/model` から) | 直した | d1 (済み) |
| store → richtext | なし。`FitQueuedRichText` は変換を関数で受け (sync のポート `RichText`、`richtext.Converter.Fit`)、送り先は `domain/rich` で決める。`ToEditableMarkdown` は c1 で呼ぶ側に移した | 直した | d3 (済み) |
| sync → store | なし。ストアは d3 で sync のポート `LocalStore` にした (型は domain/outbox・readmodel・asana/model)。#73 で、402 プラン判定を `domain/plan`、日本語整形を `ui/format`、PUT/CF 変換を `asana/model`、`sync_state` 生キーを `store` 内の型付きアクセサへ分離し、`readmodel` からは読み取り DTO・`ResultKind`・共通エラー値のみを使う | 直した | d3・e1・#73 (済み) |
| sync → asana | なし。クライアントは sync のポート `AsanaAPI` (ports.go) で受け、型は `asana/model` で呼ぶ | 直した | d3 (済み) |
| sync → auth | なし。送る前の失敗は、auth が `*auth.TokenError` で包み、sync は `RequestNotSent()` のメソッドで見分ける (push.go の `requestNotSent`) | 直した | d2 (済み) |
| sync → config | なし。`SyncConfig` は `sync.Config` に、`CacheDir` の既定値は `xdg.CacheDir()` に (d3) | 直した | d3 (済み) |
| sync → termsafe | 比べる前の文字の除去 | 許す | — |
| sync → xdg、config → xdg | 添付の既定の場所、ツールのフォルダ | 許す | — |
| tui → store | なし。ストアは `tui.Store` (app と sync のポートと、画面が書く状態) で受け、名前は readmodel・domain から | 直した | a・c2・d4b (済み) |
| mcpserver → store | なし。ストアは `mcpserver.Store` (app と sync のポート) で受ける | 直した | a・c2・d4b (済み) |
| cli → store | なし。ストアは `cli.Store` (tui.Store・mcpserver.Store と、コマンドが書くもの) で受け、`Wiring.OpenStore` (wire.OpenStore) で開く | 直した | a・c2・d4・d4b (済み) |
| tui・mcpserver・cli → richtext | Markdown → html (11 か所) と、表示の変換 (tui/edit.go:118-125、tui/markdown.go:29-34、mcpserver/server.go:393-398 ほか) | Markdown → html は直す (a5 で済み。archtest の関数の決まりで確かめる)、表示の変換は許す | a1・a4・a5 |
| tui・mcpserver・cli → auth | `LoginHelp`・`ErrNotLoggedIn`・`ErrLoginReplaced`、cli のログインのコマンド | 許す (決まり 4) | — |
| tui・mcpserver・cli → sync | `Syncer.Run`・`Push`・`Resolve`・`Result`・`ErrPushBusy` など | 許す (アプリ層)。cli の組み立ては d4 で `wire.Syncer` に移した。オフラインのときにキャッシュの添付と画像だけを読む `sync.Syncer{Store: ...}` (tui 4 か所、mcpserver 1 か所) は残っている (adapter は作らず、持っているストアを渡すだけ。import の決まりには反しない) | d4 (cli は済み) |
| tui・cli → imageview、cli → browser | 画像の表示、ブラウザ | 許す (決まり 4) | — |
| tui・mcpserver・cli → dates | 期日の解釈 (6 か所) | import は許す (ドメイン)。呼び出しは app に移した (`app.ParseDue`。archtest の関数の決まりで確かめる) | a1・a5 (済み) |
| cli → tui・mcpserver | 起動 | 許す (決まり 5) | — |
| cli → asana | なし。クライアントは wire が作り (`wire.APIClient`・`wire.CheckPAT`)、cmd/asagao が `cli.Wiring` で渡す | 直した | d4 (済み) |
| cli → config | なし。設定の型は `internal/settings` (共有の型)、読み込みとフォルダの移し替えは `Wiring.LoadSettings`・`MigrateLegacyDirs`、場所は `internal/xdg` | 直した | d4b (済み) |
| cli → tokenstore | なし。保存先は auth を通す (`auth.TokenStore`・`OpenTokenStore`・`MoveLogin` ほか) | 直した | d4b (済み) |
| cli → termsafe、tui → termsafe ほか | 文字の除去 | 許す | — |

外部ライブラリの「直す」: なし。cli → x/oauth2 は d4 で済んだ (HTTP クライアントは `auth.Source.Client`・`auth.PATClient`)。sync → x/oauth2 は d2 で済んだ。ほかは表のとおり許す。

e1 で「直す」の行がなくなり、依存の向きのテストの許可リスト (`internal/archtest/rules_test.go` の `allowed`) が空になった。#69 の終わりの条件の 1 つ (ADR 0007)。新しい違反は許可リストに足さず、コードを移して直す。

## 読み取りの扱い

- (c2・d4b・#73 で済んだ。今は入り口は `app.Query` を通して読み、`store` を import しない。以下は決めたときの経緯と現在の整理。)
- 読み取り (一覧、検索、詳細) は、段階 a では今のまま入り口から `store` を呼んでよい。書き込みと違って、同じ手順が入り口ごとに重なっている量が少なく、振る舞いの違いの原因にもなっていない。
- 入り口が読む型は、`asana/model` の型ではなく、**store が定義した表示用の型** (`store.TaskSummary`・`store.TaskDetail`・`store.CustomFieldView`・`store.ProjectView` など) と、表示の文言の関数 (`StatusLabel`・`ConflictLines`・`DescribeOp`・`AccountChangedHelp` など) と、エラーの値 (`ErrNotFound`・`ErrPushBusy`・`ErrUnsentChanges` など)。tui だけで約 50 種類ある。
- 段階 c2 でこれらを `internal/readmodel` に移し、段階 e1 を経て、`app` に読み取りのユースケース `Query` とポート `QueryStore` を置いた (ADR 0009)。さらに #73 で非 DTO 責務（プラン判定 `domain/plan`・日本語表示整形 `ui/format`・Asana 値変換 `asana/model`・`sync_state` 生キー隠蔽 `internal/store`）を分離し、`readmodel` は純粋な表示・問い合わせ DTO と `ResultKind`・エラー値のみを持つ。
- `asana/model` の型 (ADR 0005) は、キャッシュと同期が Asana の応答を扱うための型で、入り口の表示には `readmodel`、日本語文言の整形には `ui/format` を使う。
