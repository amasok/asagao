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
  どこからでも: termsafe (標準ライブラリだけ)、asana/model (型だけ)
```

依存してよい向きは、上から下、adapter から domain と asana/model だけ。

## パッケージごとの置き場所

| パッケージ (目標) | 層 | 今 | 使ってよい外部ライブラリ | 中身 |
|---|---|---|---|---|
| `internal/domain/gid` | ドメイン | (store の `LocalPrefix`・`IsLocalGID`) | なし | ローカル gid の型と判定、作り方 (乱数は引数) |
| `internal/domain/task` | ドメイン | (store の `validateFields` ほか) | なし | タスクの欄の検証、承認の状態の決まり |
| `internal/domain/outbox` | ドメイン | (store の `Op`、`safeToResend`、`UpdateTaskLocal` の後半、sync の `sameTaskFields`) | なし | 操作の種類・状態、畳み込み、再送してよいか、照合で同じとみなす条件 |
| `internal/domain/rich` | ドメイン | (richtext の `Target`、`layout()`、入れ子の決まり) | なし | 送り先と、送り先ごとに許すタグ |
| `internal/dates` | ドメイン | 同じ | なし | 期日の解釈 (今も外部依存なし。場所は変えない) |
| `internal/app` | アプリ | **ない** | なし | ユースケース (タスク作成・更新・コメント・status update・タグ・…)、ポート |
| `internal/sync` | アプリ | 同じ | なし (段階 d で x/oauth2 を外す) | 同期のユースケース (push・照合・pull)。Asana とキャッシュはポート経由 |
| `internal/store` | adapter | 同じ | modernc.org/sqlite、x/sys | キャッシュのリポジトリ。SQL とトランザクションだけ |
| `internal/asana` | adapter | 同じ | なし (net/http) | Asana の REST クライアント (腐敗防止層) |
| `internal/asana/model` | 共有の型 | (今は `internal/asana` の中) | なし | `Task`・`Story`・`Project` などの型 (JSON のタグ付き) |
| `internal/auth` | adapter | 同じ | x/oauth2 | OAuth、トークンの供給 |
| `internal/auth/tokenstore` | adapter | 同じ | go-keyring | トークンの保存先 |
| `internal/richtext` | adapter | 同じ | goldmark、x/net/html | Markdown ⇔ rich text の変換。決まりは `domain/rich` を読む |
| `internal/config` | adapter | 同じ | go-toml | 設定ファイル |
| `internal/imageview`, `internal/browser` | adapter | 同じ | x/sys、x/term | 端末と OS |
| `internal/termsafe` | 共通 | 同じ | なし | 端末の制御文字の除去 |
| `internal/tui` | 入り口 | 同じ | bubbletea、bubbles、lipgloss、glamour、x/ansi、go-runewidth | 画面 |
| `internal/mcpserver` | 入り口 | 同じ | MCP go-sdk | MCP のツール |
| `internal/cli` | 入り口 | 同じ | cobra、bubbletea、MCP go-sdk、x/term | コマンド |
| `internal/wire` | 組み立て | (`internal/cli/root.go` の一部) | なし (adapter を通す) | adapter を作り、app と sync に渡す。adapter を import してよい唯一の場所 |

`domain/rich` などの名前は、その段階の PR で決め直してよい (ADR にする)。

## 依存の決まり

1. **ドメイン** (`internal/domain/...`、`internal/dates`) は、標準ライブラリとほかのドメインのパッケージだけを import する。`time.Now`、`os`、`net`、`database/sql` を使わない。時刻と乱数は引数で受ける。
2. **アプリ** (`internal/app`、`internal/sync`) は、ドメイン・`asana/model`・`termsafe` と、標準ライブラリだけを import する。外部ライブラリを (間接にも) 使わない。Asana・キャッシュ・時計・rich text の変換・送り方は、自分で定義したポートで受ける。
3. **adapter** は、ドメイン・`asana/model`・`termsafe`・自分の外部ライブラリを import してよい。アプリと入り口と、ほかの adapter は import しない。例外: `auth` は `auth/tokenstore`・`config`・`browser` を、`auth/tokenstore` は `config` を使ってよい (同じ認証のまとまりと、その設定)。`auth` が `asana` を使うのは、401 の判定 (`tokensource.go:56` の `*asana.Error`) だけで、段階 d で `asana/model` に移す型の 1 つにする。
4. **入り口** は、アプリ・ドメイン・`asana/model`・`termsafe`・自分の画面のライブラリを import してよい。adapter (store、asana、auth、richtext) は import しない。
5. **組み立て** (`internal/wire`、`cmd/asana-tui`) だけが、全部を import してよい。
6. 外部ライブラリは、上の表の「使ってよい外部ライブラリ」の場所でだけ使う。

## 今との違い (段階ごとに直す)

| 違反 (main, dac91b2) | 直す段階 |
|---|---|
| 入り口が `store` の `*Local` を直接呼ぶ (TUI・CLI・MCP) | a (書き込み)、c (読み取り) |
| 入り口が `richtext.Target.*FromMarkdown` と `dates.Parse` を直接呼ぶ (11 か所と 6 か所) | a |
| `store` が `richtext` を import する (`describe.go:104` の `ToEditableMarkdown`) | c |
| `store` が `asana` (HTTP のパッケージ) を import する (9 ファイル。使うのは型だけ) | d (`asana/model` に分ける) |
| `sync` が `x/oauth2` を import する (`push.go:614-616` の `*oauth2.RetrieveError`) | d |
| `sync` が `config` を import する (`config.SyncConfig`、`config.CacheDir`) | d |
| `cli` が `x/oauth2` を import し、`asana.NewClient` を組み立てる (`root.go:91-97`、`auth.go:120`) | d (`internal/wire` に移す) |
| TUI と MCP が `sync.Syncer{Store: ...}` を自分で組み立てる (5 か所) | d |
| ドメインの判断が SQL の関数の中にある (`UpdateTaskLocal`、`safeToResend`、`PruneTasks`) | b (判断)、c (SQL だけにする) |

## 読み取りの扱い

- 読み取り (一覧、検索、詳細) は、段階 a では今のまま入り口から `store` を呼んでよい。書き込みと違って、同じ手順が入り口ごとに重なっている量が少なく、振る舞いの違いの原因にもなっていない。
- 段階 c で、`app` に読み取りのポート (例: `TaskQuery`) を置き、入り口は `app` を通して読む。これで入り口から `store` の import がなくなる。
- 読み取りで返す型は `asana/model` の型のままにする (ADR 0005)。
