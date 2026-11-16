# ADR 0004: 外部ライブラリは adapter と入り口にだけ置き、ドメインとアプリは標準ライブラリだけにする

- 状態: 提案 (Issue #69 の設計の段階)
- 日付: 2026-11-16

## 背景

`go list` で確かめた、外部ライブラリを直接 import しているパッケージ (テストを除く。main, dac91b2):

| ライブラリ | パッケージ | 見立て |
|---|---|---|
| modernc.org/sqlite | store | 良い |
| github.com/zalando/go-keyring | auth/tokenstore | 良い |
| github.com/pelletier/go-toml/v2 | config | 良い |
| charm.land/bubbletea・bubbles・lipgloss・glamour、x/ansi、go-runewidth | tui (bubbletea と go-runewidth は cli も) | 入り口なので良い |
| github.com/spf13/cobra | cli | 入り口なので良い |
| github.com/modelcontextprotocol/go-sdk | mcpserver、cli | 入り口なので良い |
| github.com/yuin/goldmark、golang.org/x/net/html | richtext (frommd.go、richtext.go) | **決まりと混ざっている** |
| golang.org/x/oauth2 | auth (oauth.go、tokensource.go)、**cli** (auth.go、root.go)、**sync** (push.go) | **sync と cli が使っている** |
| golang.org/x/sys | store (lock_unix.go、lock_windows.go)、imageview | OS との境界なので良い |
| golang.org/x/term | imageview、cli | OS との境界なので良い |

## 決めたこと

1. **ドメインとアプリ (app・sync) は、外部ライブラリを間接にも使わない。** 標準ライブラリと、ドメイン・`asana/model`・`termsafe` だけ。
2. **richtext は「決まり」と「変換」に分ける (段階 b5)。**
   - 決まり: 送り先 (`Target`) と、送り先ごとに許すタグ (`layout()`、入れ子の決まり)。外部依存のない `internal/domain/rich` に置く。今の target.go (33 行) と、frommd.go の `isBlockElement`・`asText` の判断の部分。
   - 変換: goldmark の AST を歩く処理と、x/net/html での解析。`internal/richtext` に残し、決まりを読む。
   - 変換の処理まで決まりと分けることはしない。AST を歩く処理と、何を書くかの判断は細かく絡んでいて (frommd.go 692 行)、分けると、#60・#63 で積んだ大量の乱数のテスト (TestUserMarkdownFollowsNestingRules ほか) が守っている振る舞いを崩しやすい。
3. **x/oauth2 は auth の中にだけ置く (段階 d2・d4)。**
   - sync: `requestNotSent` が `*oauth2.RetrieveError` を見て「送る前に失敗した」と判断している (push.go:614-616)。auth の TokenSource が、トークンを得られなかったエラーを自前の印 (`auth.ErrNoToken` のような番兵か、`auth.IsTokenUnavailable(err)`) で包んで返すようにし、sync はそれだけを見る。
   - cli: `oauth2.NewClient(ctx, src)` と `oauth2.StaticTokenSource` を組み立ての中で使っている。auth に「トークンの供給元から `*http.Client` を作る」関数を置き、組み立て (`internal/wire`) はそれを呼ぶ。
4. **x/sys と x/term は、OS との境界として今の場所 (store のロック、imageview、cli の隠した入力) で使ってよい。** ドメインとアプリでは使わない。
5. 時刻・乱数・ファイル・ネットワークも外部依存とみなす。ドメインは `time.Now` と `crypto/rand` を呼ばず、引数で受ける (今も `UpdateTaskLocal` などは `now time.Time` を受けている)。

## 結果

- 依存の向きのテスト (ADR 0007) に、外部ライブラリごとに使ってよいパッケージの表を持たせる。
- goldmark を別のパーサーに替えるときに、触るのは `internal/richtext` だけになる。送り先の決まりを変える (Asana が受け付けるタグが変わる) ときは、`domain/rich` と仕様 (`openspec/specs/rich-text`) を直す。

## 選ばなかった案

- **richtext 全体をインターフェースの裏に隠し、ドメインに自前の Markdown の木を作る**: 大きく作り直すことになり、振る舞いを変えずに移すのが難しい。
- **x/oauth2 を sync で使うのを認める**: sync のテストが、認証のライブラリの内部の型に依存し続ける。認証の方法を足す (例: 別の OAuth のライブラリ) ときに sync を直すことになる。
