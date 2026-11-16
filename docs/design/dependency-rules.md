# 依存の向きを守らせる方法 (提案)

`architecture.md` の「依存の決まり」を、人の目ではなくテストで守る。ここに書くのは提案で、実装は段階 a の 2 つ目の PR で行う (移行の順番は `migration-plan.md`)。

## 方針

- **`go test ./...` の中で確かめる。** CI に新しい道具を足さない。開発者も手元の `go test` で気づける。Go 以外の道具 (depguard、go-arch-lint など) は使わない。依存を増やさずに、`go list` で足りるため。
- **今の違反は許可リストに書き、段階ごとに減らす (ラチェット)。** 最初の PR で全部を直すことはしない。許可リストにない新しい違反は、テストが落ちる。許可リストに書いた違反がなくなったら、それもテストが落ちる (「古い許可が残っている」)。これで、直したあとに許可を消し忘れない。
- **外部ライブラリは、間接の依存まで見る。** ドメインとアプリは、`go list -deps` で外部のモジュールが 1 つも出てこないこと。たとえば sync が `config` を import すると、go-toml が間接に入るので落ちる。

## テストの形 (案)

置き場所: `internal/archtest/deps_test.go` (テストだけのパッケージ。プロダクトのコードからは import しない)。

```go
package archtest

// layer は、パッケージの層。決まりは architecture.md の「依存の決まり」。
var layers = map[string]string{
	"internal/domain/...": "domain",
	"internal/dates":      "domain",
	"internal/app":        "app",
	"internal/sync":       "app",
	"internal/store":      "adapter",
	"internal/asana":      "adapter",
	// ...
	"internal/tui":        "entry",
	"internal/cli":        "entry",
	"internal/mcpserver":  "entry",
	"internal/wire":       "wire",
}

// external は、外部のモジュールを使ってよいパッケージ。
var external = map[string][]string{
	"modernc.org/sqlite":          {"internal/store"},
	"github.com/zalando/go-keyring": {"internal/auth/tokenstore"},
	"github.com/yuin/goldmark":    {"internal/richtext"},
	"golang.org/x/net":            {"internal/richtext"},
	"golang.org/x/oauth2":         {"internal/auth"},
	// ...
}

// allowed は、今ある違反。段階ごとに減らす。理由と、直す段階を書く。
var allowed = []violation{
	{"internal/sync", "golang.org/x/oauth2", "段階 d: push.go の RetrieveError"},
	{"internal/cli", "golang.org/x/oauth2", "段階 d: root.go の NewClient を wire へ"},
	{"internal/store", "internal/richtext", "段階 c: describe.go"},
	{"internal/store", "internal/asana", "段階 d: 型を asana/model へ"},
	{"internal/sync", "internal/config", "段階 d"},
	{"internal/tui", "internal/store", "段階 c"},
	// ...
}

func TestDependencyDirection(t *testing.T) {
	// go list -deps -json ./... を 1 回だけ実行し、ImportPath・Imports・Deps・Module を読む。
	// 1. 直接の import を、層の決まりと allowed で確かめる。
	// 2. domain と app の Deps に、標準ライブラリ以外のモジュールがないこと。
	// 3. 外部モジュールを直接 import するパッケージが external にあること。
	// 4. allowed のうち、もう起きていないものを「古い許可」として報告する。
}
```

- `go list` は `os/exec` で呼ぶ。`go` が見つからないとき (普通はない) は `t.Skip` する。
- テストのファイル (`_test.go`) の import は見ない (`Imports` だけを見て、`TestImports` は見ない)。テストは偽サーバーなどで adapter を使ってよい。
- `go list -deps -json ./...` は、手元 (macOS、ビルドのキャッシュあり) で 0.45 秒だった (2026-11-16 に測った)。

## ドメインの「時刻は引数」を守らせる

- `internal/domain/...` の Go のファイルを `go/parser` で読み、`time.Now`・`time.Since`・`time.Until`・`rand.Read` の呼び出しがないことを、同じテストで確かめる。
- `os`・`net`・`database/sql`・`net/http` の import がないことは、上の 1 で確かめる。

## CI

- 今の CI (`go vet ./...` と `go test ./...`) の中で動くので、ワークフローは変えない。
- 落ちたときの出力は、「どのパッケージが、何を import していて、どの決まりに違反しているか、architecture.md のどこを見ればよいか」を 1 行ずつ出す。

## 選ばなかった案

- **depguard (golangci-lint)**: 設定は短いが、golangci-lint を CI と手元に入れる必要がある。間接の依存 (sync → config → go-toml) は見られない。
- **internal を細かく切って Go の internal の決まりで防ぐ**: `internal/app/internal/...` のように置くと、見える範囲は Go が守るが、入り口から adapter への import は防げない。ディレクトリが深くなる。
- **レビューで見るだけ**: 段階 a〜d の間に、TUI・CLI・MCP の 3 か所で同時に作業が進むので、見落とす。
