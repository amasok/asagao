# ADR 0003: 入り口・アプリ・ドメイン・adapter の 4 層にし、パッケージの名前は今のものを残す

- 状態: 提案 (Issue #69 の設計の段階)。決めたことの 2 のうち「送り方」をポートで受けることと、結果の「送り方は `Sender` ポートで受ける」は置き換え済み (ADR 0008)
- 日付: 2026-11-16

## 背景

- 3 つの入り口 (TUI・CLI・MCP) が、store の `*Local` を直接呼んでいる。タスク作成の組み立ては 3 か所 (cli/write.go:166-199、mcpserver/server.go:464-513、tui/write.go:334-378)、説明文の変換の組は 11 か所、`dates.Parse` は 6 か所にある (write-flow の仕様)。
- Asana と outbox の決まり (畳み込み、再送してよいか、片付けてよいか) は、SQL と同じ関数の中にある (`UpdateTaskLocal`、`safeToResend`、`PruneTasks`)。
- 外部ライブラリは、ほぼパッケージごとに閉じているが、例外がある (richtext の goldmark と決まりの混在、sync と cli の x/oauth2)。

## 決めたこと

1. 層を 4 つにする: **入り口** (tui・cli・mcpserver)、**アプリ** (app・sync)、**ドメイン** (domain/...・dates)、**adapter** (store・asana・auth・tokenstore・richtext・config・imageview・browser)。ほかに、どこからでも使える **共通** (termsafe、asana/model) と、**組み立て** (wire、cmd) を置く。詳しくは `docs/design/architecture.md`。
2. 依存してよい向きは、入り口 → アプリ → ドメイン、adapter → ドメイン。アプリが使う外部のもの (キャッシュ、Asana、時計、送り方、rich text の変換) は、**アプリの側で定義したインターフェース (ポート)** で受ける。Go の習慣どおり、インターフェースは使う側に置く。
3. **今のパッケージの名前と場所を、できるだけ残す。** `internal/store` はそのままリポジトリ (adapter) に、`internal/asana` はそのまま腐敗防止層にする。`internal/adapter/sqlite` のような改名はしない。新しく作るのは `internal/app`、`internal/domain/...`、`internal/asana/model`、`internal/wire` だけ。
4. `internal/sync` はアプリ層 (同期のユースケース) とみなす。新しい `internal/app` に入れない。同期は大きく (2724 行)、すでにまとまっていて、移す利点がない。
5. 読み取り (一覧・検索・詳細) は、段階 c までは入り口から store を直接呼んでよい。

## 結果

- import のパスが変わるのは、新しいパッケージに移した関数を呼ぶところだけになる。PR の差分が小さく、並行する作業 (richtext の修正など) とぶつかりにくい。
- 層はディレクトリの名前からは分からない。`architecture.md` の表と、依存の向きのテスト (ADR 0007) の表が、層の定義になる。
- `internal/app` と `internal/sync` は、同じ層にある 2 つのパッケージになる。app から sync を import しない (送り方は `Sender` ポートで受ける。ADR 0006)。

## 選ばなかった案

- **ディレクトリを層の名前で作り直す** (`internal/adapter/...`、`internal/entry/...`): 見た目で層が分かるが、全ファイルの import が変わり、1 つの大きな PR になる。#69 の「一度に作り直さない」に反する。
- **層を分けず、重複だけを関数にまとめる**: 段階 a だけなら足りるが、ドメインの判断を SQL から出す (段階 b・c) ときの置き場所が決まらない。
- **ヘキサゴナルを厳密にして、ドメインの型 (自前の `Task`) を作る**: ADR 0005 で扱う。
