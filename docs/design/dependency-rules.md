# 依存の向きを守らせる方法

`architecture.md` の「依存の決まり」を、人の目ではなくテストで守る。段階 a2 で `internal/archtest` に入れた (移行の順番は `migration-plan.md`)。

## 方針

- **`go test ./...` の中で確かめる。** CI に新しい道具を足さない。開発者も手元の `go test` で気づける。Go 以外の道具 (depguard、go-arch-lint など) は使わない。依存を増やさずに、`go list` で足りるため。
- **今の違反は許可リストに書き、段階ごとに減らす (ラチェット)。** 最初の PR で全部を直すことはしない。許可リストにない新しい違反は、テストが落ちる。許可リストに書いた違反がなくなったら、それもテストが落ちる (「古い許可が残っている」)。これで、直したあとに許可を消し忘れない。
- **外部ライブラリは、間接の依存まで見る。** ドメイン・アプリ・共有の型・termsafe から import をたどって、外部のモジュールに着いたら落ちる。たとえば sync が `config` を import すると、go-toml が間接に入るので落ちる。ただし、たどるときに許可リストの import は通らない (下の「間接の依存」)。

## テストの形

置き場所: `internal/archtest` (テストのファイルだけのパッケージ。プロダクトのコードからは import しない。層の表では `test` にして、だれも import できないようにしてある)。

| ファイル | 中身 |
|---|---|
| `rules_test.go` | 表。段階ごとに直すのはここだけ。`layers` (パッケージ → 層)、`adapterExceptions` (決まり 3 の例外)、`entryAdapters` (決まり 4)、`entryToEntry` (決まり 5)、`external` (外部モジュール → 直接 import してよいパッケージ。ADR 0004)、`allowed` (今ある違反。`architecture.md` の表で「直す」にした行と、直す段階) |
| `deps_test.go` | 確かめる処理。`TestDependencyDirection` と `TestTablesFollowTheRules` |
| `checker_test.go` | 作り物の import のグラフで、それぞれの確かめ方が落ちるべきときに落ちることを確かめる (ドメインから adapter、app から sync、入り口から store、古い許可、間接の外部モジュール、層のないパッケージ、など)。自分用の許可リスト (`sampleAllowed`、a2 の時点の `allowed` の写し) を使うので、段階で `allowed` の行を消しても、このファイルは直さなくてよい |

`TestDependencyDirection` は、`go list -deps -json ./...` をモジュールのルートで `GOOS` = linux・darwin・windows の 3 回 (並行して) 実行し、import を合わせてから、次を確かめる。

1. **層** (`layers`): モジュールのパッケージに、層が決まっていないものがない。新しいパッケージを作ったら、表に足す。`internal/domain/...` のように `/...` で終わるものは、その下をまとめて指す。
2. **import** (`architecture.md` の決まり 1〜7): パッケージ同士の import、外部モジュールの直接の import (`external` にない場所で使っていないか)、ドメインが `os`・`net`・`net/http`・`database/sql`・`syscall`・`crypto/rand`・`math/rand` (とその下) を import していないか。`allowed` にない違反があると落ちる。 共有の型 (`asana/model`・`readmodel`) はドメイン・ほかの共有の型・termsafe だけを、termsafe は標準ライブラリだけを import する (`architecture.md` の層の図の「どこからでも」)。
3. **古い許可**: `allowed` の行のうち、もう起きていないもの。直したら、その行を消し、`architecture.md` の表も直す。
4. **間接の依存** (ADR 0004 の 1): ドメイン・アプリ・共有の型・termsafe のパッケージから import をたどり、外部のモジュールに着いたら、いちばん短い import の鎖を出して落ちる。`allowed` にある import は通らない。sync は今、store・auth・config を通して sqlite・oauth2・go-toml などに届くが、それはその import の許可として数えてあり、その許可を消す段階で一緒になくなるため。許可を消すと、その import もたどるようになる。間接のものは許可リストに入れられない。
5. **ドメインの時計** (下の「ドメインの『時刻は引数』を守らせる」)。

`TestTablesFollowTheRules` は、表そのものが決まりに反していないことを確かめる (表を書き換えるだけで決まりを緩められないように): `external` に出てくるのは adapter と入り口だけ、`entryAdapters` に store・asana・config・tokenstore がない、`allowed` に、決まりがもう許している import や、同じ行の重複や、段階の書いていない行がない、など。

- `go` が見つからないとき (普通はない) は `t.Skip` する。
- テストのファイル (`_test.go`) の import は見ない (`go list` の `Imports` だけを見て、`TestImports` は見ない)。テストは偽サーバーなどで adapter を使ってよい。
- `GOOS` を 3 つ合わせるのは、ビルドの条件で分かれたファイル (`store/lock_windows.go`・`open_windows.go`、`mcpserver/nlink_other.go` など) の import も見るため。どの OS で動かしても結果が同じになる。
- 手元 (macOS、ビルドのキャッシュあり) で、`go list` 3 回で 0.15 秒ほど (2026-11-17 に測った)。
- 落ちたときは、1 行に 1 つ、「どのパッケージが、何を import していて、どの決まり (番号) に反しているか」を出す。決まりの番号は `architecture.md` の「依存の決まり」。

## 関数の決まり (import だけでは分からないもの)

`richtext` は、入り口から「表示の変換」のために import してよいが、Markdown → html の変換は app だけが呼ぶ。こうした決まりは import では見分けられないので、`go/parser` で入り口のファイルを読み、名前で確かめる。型までは調べない (`go/types` を使わない) ので、名前は store と richtext にしかないものに限る。

| 決まり | 調べる名前 | 入れる時期 |
|---|---|---|
| 入り口は Markdown → html を呼ばない | `richtext.TaskNotes`・`richtext.Comment`・`richtext.StatusUpdate` の `FromMarkdown`・`PlainFromMarkdown` | a5 のあと (a1 からは、移した入り口のファイルごとに) |
| 入り口は store の書き込みを呼ばない | `Local` で終わるメソッド (`CreateTaskLocal` など。store にしかない) | a6 のあと |
| 入り口は期日を解釈しない | `dates.Parse` | a5 のあと |

これも許可リスト (ファイルと名前の組) を持ち、段階ごとに減らす。a2 ではまだ入れていない (入れる時期は上の表のとおり)。入れるときは `internal/archtest` に足す。

## ドメインの「時刻は引数」を守らせる

- ドメインの層のパッケージ (`internal/domain/...` と `internal/dates`) の Go のファイルを `go/parser` で読み、時計を読む・待つ関数 (`time.Now`・`Since`・`Until`・`Sleep`・`After`・`AfterFunc`・`Tick`・`NewTicker`・`NewTimer`) を使っていないことを、同じテストで確かめる。`time` を別の名前で import していても見つける。`time` の dot import も落とす (名前で見分けられないため)。`time.Time` や `time.Duration` の型は使ってよい。
- 乱数 (`crypto/rand`・`math/rand`) と、`os`・`net`・`net/http`・`database/sql`・`syscall` は、import そのものを上の 2 で落とす。

## CI

- 今の CI (`go vet ./...` と `go test ./...`) の中で動くので、ワークフローは変えない。
- 落ちたときの出力は、「どのパッケージが、何を import していて、どの決まりに違反しているか、architecture.md のどこを見ればよいか」を 1 行ずつ出す。

## 選ばなかった案

- **depguard (golangci-lint)**: 設定は短いが、golangci-lint を CI と手元に入れる必要がある。間接の依存 (sync → config → go-toml) は見られない。
- **internal を細かく切って Go の internal の決まりで防ぐ**: `internal/app/internal/...` のように置くと、見える範囲は Go が守るが、入り口から adapter への import は防げない。ディレクトリが深くなる。
- **レビューで見るだけ**: 段階 a〜d の間に、TUI・CLI・MCP の 3 か所で同時に作業が進むので、見落とす。
