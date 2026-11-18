# asagao

ターミナルで Asana のタスクを確認・操作するための TUI / CLI / MCP サーバー。

- 要件定義: [docs/requirements.md](docs/requirements.md)
- 現在の状態: 最初のリリース v0.2.0 の準備中（ボード表示・セクション移動/名前変更/折りたたみ・タグ絞り込み・詳細の全画面表示・画像表示）

## 旧名 asana-tui から

asana-tui から asagao に名前を変えました。手で移すものはありません。

- 起動すると、設定・データ・キャッシュのフォルダ（`~/.config/asana-tui` など）を `asagao` に移し、元の場所には新しいフォルダへのリンクを残します。古い `asana-tui` が MCP サーバーとして登録されたままでも、同じローカルストアと未送信の変更を使います。`asagao` のフォルダがすでにあれば、そちらを使い、古いフォルダはそのまま残します。
- キーチェーン（サービス `asana-tui`）と 1Password（設定で項目名を変えていなければ項目 `asana-tui`）に保存したログインは、`asagao` の名前で保存し直して使います。古い方は残します（`auth logout` では両方を消します）。
- MCP サーバーは、都合のよいときに `claude mcp add asana -- asagao mcp` で登録し直してください。

## インストール

macOS / Linux（amd64・arm64）向けです。

### Homebrew

```sh
brew install amasok/tap/asagao
```

`amasok/homebrew-tap` の cask で入れます。バイナリは署名していないので、macOS で初めて動かすときに止められないよう、入れるときに quarantine（ダウンロードした印）を外します。

### go install

```sh
go install github.com/amasok/asagao/cmd/asagao@latest   # または @v0.2.0 のようにバージョンを指定
```

Go 1.27.4 以上が要ります。

入ったバージョンは `asagao --version` で確かめられます。

## セットアップ

ログインの方法は 2 つあります。設定ファイルを作らずにすぐ使えるのは Personal Access Token (PAT) です。

| 方法 | 手間 | トークン |
|------|------|----------|
| Personal Access Token | トークンを作って貼るだけ | 期限なし（Asana で取り消すまで有効） |
| OAuth | OAuth アプリの登録と `auth.client_id` の設定が必要 | 1 時間ごとに自動更新 |

### Personal Access Token でログインする（かんたん）

1. https://app.asana.com/0/my-apps の「Personal access tokens」でトークンを作り、コピーする
2. ログインして同期する

```sh
asagao auth login --pat   # トークンを貼り付ける（入力は表示されません）
asagao sync
```

- 標準入力からも渡せます: `pbpaste | asagao auth login --pat`、`op read "op://Private/Asana/token" | asagao auth login --pat`。トークンはシェルの履歴に残らないよう、コマンドの引数では受け付けません。
- `auth.client_id` が設定されていなければ、`--pat` を付けなくても PAT でのログインになります。
- ログイン時に `/users/me` で確かめてから、OAuth と同じ保存先（`auth.token_store`。既定はキーチェーン）に保存します。保存先を変えるときだけ下の設定ファイルを作ってください。
- トークンを取り消した・失効した場合は、TUI / CLI / MCP にその旨と `asagao auth login --pat` が表示されます。新しいトークンでログインし直せば、未送信の変更はそのまま送られます（TUI と MCP サーバーは再起動不要）。
- OAuth と PAT はいつでも乗り換えられます（あとからログインした方に置き換わります）。前と違うアカウントでログインすると、ローカルのデータを守るため同期を止めて案内を出します。

### OAuth でログインする

#### 1. Asana に OAuth アプリを登録する

1. https://app.asana.com/0/my-apps で「Create new app」
2. **OAuth** の設定で Redirect URL を 2 つ追加する
   - `http://localhost:8976/callback`（通常のログイン用。ポートは `auth.redirect_port` に合わせる）
   - `urn:ietf:wg:oauth:2.0:oob`（`--manual` 用。SSH 先などブラウザがない環境向け）
3. **Permission scopes** で「Full permissions」を選ぶ
4. Client ID と Client secret を控える

#### 2. 設定ファイルを作る

`~/.config/asagao/config.toml`:

```toml
[auth]
client_id = "1234567890"
token_store = "keychain"   # keychain / 1password / file
# redirect_port = 8976

# token_store = "1password" のとき
[auth.onepassword]
vault = "Private"
item = "asagao"
# account = "my.1password.com"   # 複数アカウントがある場合

[sync]
completed_days = 14     # 完了済みタスクを何日分保存するか
# all_projects = true   # 参加していないプロジェクトも同期する
```

PAT だけで使う場合は `[auth]` の `client_id` は不要です（保存先を変えるときだけ `token_store` を書きます）。

1Password に保存する場合は、[1Password CLI](https://developer.1password.com/docs/cli/get-started/) をインストールし、デスクトップアプリとの連携を有効にしてください。リフレッシュトークン（PAT でログインした場合は PAT）と Client secret が指定した保管庫のアイテムに保存されます。

#### 3. ログインして同期する

```sh
asagao auth login     # ブラウザが開く。Client secret を聞かれたら入力
asagao sync           # ローカルストアに同期
asagao sync status
```

## TUI

```sh
asagao            # TUI を起動（バックグラウンドで同期。5 分ごとに自動同期）
asagao --offline  # 同期せず、ローカルストアだけで起動
```

左から「プロジェクト / タスク一覧 / 詳細」の 3 ペイン。幅が 100 桁未満の端末では 1 ペインずつ表示します。

### キー操作

キーは 3 層に整理しています。迷ったら **`space`**（その画面でできる操作のメニュー）か **`?`**（ヘルプ）を押してください。

1. **移動（どの画面でも同じ）**: `j/k` `↑↓` 上下、`h/l` `←→` 閉じる・開く、`g/G` 先頭・末尾、`ctrl+d/u` 半ページ、`enter` 開く・決定、`esc` `backspace` 戻る、`tab` ペイン切り替え、`q` 終了
2. **よく使う操作（どの画面でも同じ）**: `x` 完了、`a` 追加、`e` 編集（エディタ）、`c` コメント、`/` 検索
3. **それ以外は `space` メニュー**: `space` を押すと、画面の下にその場で使える操作が一覧で出ます（which-key 方式）。続けてキーを押すと実行します。

| space のあと | 操作 | 直接押すキー |
|---|---|---|
| **タスク** | | |
| `a` / `s` | タスク / サブタスクを追加 | `a` / `S` |
| `x` | 完了 / 未完了 | `x` |
| `e` | 名前・説明をエディタで編集 | `e` |
| `d` | 期日を変更（一覧から選ぶ） | |
| `u` | 担当者を変更（一覧から選ぶ） | `@` |
| `t` | タグを付ける / 外す（一覧から選ぶ・新規作成） | |
| `m` | セクションを移動 | `m` |
| `c` / `C` | コメント（1 行 / エディタ） | `c` / `C` |
| `L` / `w` | いいね / フォロワーを追加・外す | |
| `P` | ほかのプロジェクトに追加 / 外す | |
| `p` | 親タスクを付け替える（同じプロジェクトのタスクから選ぶ・親から外す） | |
| `Z` | タスクを削除（サブタスクも。確認してから積む。送るまでは「未送信の変更」から取り消せる） | |
| `D` / `A` | 複製 / ファイルを添付 | |
| `h` | 作業時間を見る / 記録する（[タイムトラッキング](#タイムトラッキング)。プランで使えなければ出さない） | |
| `i` / `o` | 画像を全画面で見る / ブラウザで開く | `i` / `o` |
| **表示** | | |
| `f` / `T` | 絞り込み / タグで絞り込む（一覧から選ぶ） | `f` / `T` |
| `/` | 保存した検索を呼び出す（下の「保存した検索」。サイドバーで移動するか「解除」で戻る） | |
| `v` / `z` | 表示を切り替え（リスト→ボード→カレンダー） / 詳細を全画面 | `v` / `z` |
| `-` / `_` / `+` | このセクションを折りたたむ / すべて折りたたむ / すべて開く | 同じ |
| `b` / `<` / `>` | プロジェクト一覧を隠す / 詳細を狭く / 広く | 同じ |
| `V` / `*` | 印を付ける・外す / 表示中のタスクすべてに印・外す（まとめて操作） | 同じ |
| **プロジェクト** | | |
| `E` | セクション名を変更 | `E` |
| `O` | プロジェクトの概要（説明・オーナー・期間・進み具合・最新のステータス・ブリーフ）。概要の中で `e` を押すとエディタで説明・オーナー・日付・色を変える（オフラインでも積む） | |
| `U` / `W` | ステータス更新を見る / 書く（エディタ） | |
| `N` / `M` / `X` | 新しいプロジェクト（チームが複数あれば選ぶ） / メンバー / アーカイブ（オンラインのみ） | |
| **その他** | | |
| `r` / `!` | 今すぐ同期 / 未送信の変更・競合 | `r` / `!` |
| **受信トレイ** | | |
| `R` / `Y` | 通知を既読 / 未読にする / すべて既読にする | `R` |

### 画面ごとの動き

- **タスク一覧**: `l` でサブタスクをツリー表示、`h` で閉じる・親へ。`enter` で詳細を全画面で開きます。My Tasks は期日ごと、プロジェクトはセクションごとに表示します。
- **まとめて操作**（タスク一覧）: `V` でカーソルのタスクに印（`●`）を付けて次へ進み、`*` で表示中のタスクすべてに印を付けます（全部に付いていれば外します）。印があるあいだは `x`（完了。全部完了済みなら未完了に戻す）・`m`（セクションを移動）・`@`（担当者）・`space d`（期日）が印の付いたタスクに効き、「N 件を…します (y/n)」と確かめてから 1 件ずつ未送信の変更として積みます（オフラインでも使えます）。すでにその状態のタスクは数えません。積めなかったタスクがあっても残りは積み、件数と理由を下の行に出します。実行すると印は外れます。`esc` で印をすべて外します。検索・絞り込み・折りたたみ・再読み込みで一覧から見えなくなったタスクの印は外れるので、見えていないタスクは変わりません。ボード・カレンダー・詳細では印は効かず、今までどおり 1 件に効きます。
- **タスクの追加**（`a` / `S`）: 名前の末尾に担当・期日・タグを書けます（[クイック追加](#クイック追加)）。
- **ボード**（`v`）: `h/l` で列、`j/k` でカード、`H/L` でカードを左右の列へ移します。
- **カレンダー**（`v` をもう一度。My Tasks では 1 回）: 期日のあるタスクを月表示します。`h/l` で日、`j/k` で週、`n/p` で月、`.` で今日、`enter` でその日のタスク一覧（選ぶと詳細）、`a` でその日が期日のタスクを追加します。
- **受信トレイ**（プロジェクト一覧の 2 番目）: Asana の通知（コメント・担当・メンションなど）を新しい順・日付ごとに表示します。`●` が未読、`↑` は既読・未読の変更がまだ送られていない印です。カーソルを合わせると詳細ペインに対象のタスクが出ます。`enter` でそのタスクを開き、通知を既読にします（プロジェクトの通知はそのプロジェクトへ移ります）。`R` で既読 / 未読を切り替え、`space Y` ですべて既読にします。通知は同期のたびに最新 100 件（ワークスペースごと）を保存するので、オフラインでも見られます。既読・未読の変更はほかの変更と同じく未送信の変更として送ります（同じ向きの変更は 50 件ずつまとめて送ります）。Asana が既読の変更を受け付けないとき（ログインに `notifications:write` がない）は、変更を元に戻して案内を一覧の先頭に出し、開いただけでは既読にしなくなります（`R` などで自分で既読にすると、もう一度送ってみます）。ログインし直すか、送った変更が受け付けられれば元に戻ります。
- **最近の更新**: 通知を取得できないとき（ログインに通知の権限 `notifications:read` がない、プランで使えないなど）は、受信トレイの代わりに直近 7 日に変更されたタスクを表示し、理由と対処を一覧の先頭に出します。`config.toml` で `auth.scopes` を指定していない（Full permissions の）場合は、`asagao auth login` でログインし直せば次の同期から受信トレイになります。
- **詳細**: `j/k` で項目（担当・期日・プロジェクト/セクション・タグ・フォロワー・いいね・状態・親タスク・説明・添付ファイル・サブタスク・「＋追加」）を選び、`enter` で変更します。添付ファイルは `enter` で開きます（画像はビューア、それ以外は OS のアプリ）。macOS ではダウンロードしたファイルとして印（quarantine）を付けてから開くので、Gatekeeper が確認します。アプリ・スクリプト・`.terminal`・`.webloc`・`.jar` など開くとプログラムが動く種類のファイルは、開かずに Finder（Linux ではファイルマネージャー）で場所を表示します。担当・期日・セクション・タグは一覧から選びます（期日は「日付を入力…」で自由入力も可）。名前と説明はエディタで編集します。サブタスクは `enter` で開き、`backspace` で戻ります。
- 折りたたんだセクション、詳細ペインの幅、プロジェクト一覧の表示状態は記憶します。

### クイック追加

タスクの追加（`a`、サブタスクは `S`）では、名前の末尾に担当・期日・タグを書けます。CLI では `asagao task create --quick` のときに読みます。

```
牛乳を買う @hanako 明日 #買い物
```

- `@名前`: 担当。`@me` は自分。名前（空白を除いた全体か、空白で区切った 1 語）かメールの `@` より前に一致する人（2 文字以上なら先頭の一致も）。1 人に決まらないときは読みません。
- 期日: `今日`・`明日`・`明後日`・`昨日`・`today`・`tomorrow`・`+3d`・`-1d`・`2026-12-01`・`due:明日`。
- `#名前`: 既存のタグ（新しいタグは作りません）。いくつでも。
- 末尾から読み、ほかの語にぶつかったら止めます。「明日の準備をする」「#12 の件」のような名前の途中の語は読みません。
- 読めないもの（該当なし・複数に一致など）があれば、そこで止めて名前に残し、ステータス行（CLI は出力）に理由を出します。
- 全角の `＠`・`＃` も同じに読みます。`\@`・`\#` と書くと、記号をそのまま名前に入れます。
- 名前の途中の `due:明日` は読みません（末尾に書いてください。読まなかったことはステータス行に出ます）。

### 見た目とキーの設定

```toml
[ui]
theme = "auto"       # auto（端末の背景に合わせる）/ dark / light
accent = "#7aa2f7"   # 強調色（省略で既定のピンク）

[keys]               # 直接押すキーを変える（操作名は `asagao keys` で確認）
complete = "d"
due = ["D", "ctrl+t"]
```

## コマンド

| コマンド | 内容 |
|----------|------|
| `auth login --pat` | Personal Access Token でログイン（貼り付けるか標準入力から渡す） |
| `auth login [--manual] [--no-browser]` | OAuth でログイン（`auth.client_id` が未設定なら PAT でのログインになる） |
| `auth status` | ログイン状態と方式（OAuth / Personal Access Token） |
| `auth logout` | 保存したトークン（OAuth / PAT）を削除 |
| `auth migrate --to <store>` | トークンの保存先を移す |
| `sync [--full]` | Asana からローカルストアへ同期（通常は前回以降の変更分。24 時間ごとに自動で全件） |
| `sync status` | ローカルストアの状態 |
| `task list` | タスク一覧（デフォルトは自分の未完了タスク。[`.asana.json`](#ai-エージェントから使う) があればそのプロジェクトの未完了タスク） |
| `task show [<gid>] [--offline]` | タスク詳細（サブタスク・コメント込み）。オンラインなら最新を取得。gid を省くと `.asana.json` のタスク |
| `search <キーワード>...` | タスク名・説明・コメントを全文検索 |
| `project list [--all] [--team チーム]` | プロジェクトとセクションの一覧（お気に入り → チームの順） |
| `project show <プロジェクト> [--offline]` | プロジェクトの概要（説明・オーナー・期間・進み具合・最新のステータス・ブリーフ） |
| `project edit <プロジェクト> [--notes] [--owner] [--start] [--due] [--color]` | 説明（マークダウン）・オーナー・開始日・期日・色を変える（オフラインでも積む） |
| `project teams` | プロジェクトを作れるチーム |
| `task complete <gid>...` / `task reopen <gid>...` | 完了にする / 未完了に戻す |
| `task create <名前> [--project] [--section] [--due] [--assignee] [--notes]` | タスクを作成（プロジェクト未指定なら `.asana.json` のプロジェクト、それもなければ自分に割り当て） |
| `task create --quick <名前 @担当 期日 #タグ>` | 名前の末尾の担当・期日・タグを読んで作成（[クイック追加](#クイック追加)） |
| `task move <gid> --section <セクション> [--project]` | 別のセクションへ移動 |
| `task move <gid> [--section <セクション>] --before\|--after <gid>` | セクションの中で、指定したタスクの前 / 後に置く（並び順は Asana にだけ反映） |
| `task parent <gid> <親の gid> [--before\|--after <gid>]` / `task parent <gid> --none` | 親タスクを付け替える / 親から外す |
| `task rm <gid> --yes` | タスクをサブタスクごと削除（`--yes` がないと内容を出して止まる。送るまでは `outbox discard` で取り消せる） |
| `task assign <gid> <me\|none\|名前>` | 担当者を変更 |
| `task tag <gid> --add <タグ> --remove <タグ>` | タグを付ける / 外す（ないタグは作る） |
| `task like <gid> [--off]` | いいね / 取り消し |
| `task follow <gid> --add <ユーザー> --remove <ユーザー>` | フォロワーを追加 / 外す |
| `task project <gid> --add <プロジェクト> --remove <プロジェクト>` | ほかのプロジェクトに追加 / 外す |
| `task duplicate <gid> [--name]` | 複製（サブタスク・添付・フォロワーも） |
| `task attach <gid> <ファイル>` | ファイルを添付（100MB まで） |
| `task create <名前> --parent <gid>` | サブタスクを作成 |
| `section rename <セクション> <新しい名前> --project <プロジェクト>` | セクションの名前を変更 |
| `tag list` | タグと、そのタグが付いたタスクの数 |
| `comment add <gid> <コメント>` | コメントする |
| `time list <gid> [--offline]` | タスクに記録した作業時間と合計（[タイムトラッキング](#タイムトラッキング)） |
| `time add <gid> <時間> [--date 日]` / `time edit <記録の gid> [--duration] [--date]` / `time delete <記録の gid> --yes` | 作業時間を記録する / 直す / 削除する |
| `git scan [<コミット>\|<範囲>...]` / `git install-hook` | コミットメッセージのタスク ID でコメント・完了（[git 連携](#git-連携)） |
| `outbox list` | 未送信の変更・競合・失敗を一覧表示 |
| `outbox resolve <id> --keep local\|remote` | 競合を解決（こちらの値で上書き / Asana の値を採用） |
| `inbox [--unread] [--limit N]` | 受信トレイ（Asana の通知）を新しい順に表示（`●` が未読。取得できないときは理由と最近の更新を表示） |
| `inbox read <ID>... [--all]` / `inbox unread <ID>...` | 通知を既読にする / 未読に戻す（ID は一覧の右端。末尾の数桁でも可） |
| `outbox retry <id>` / `outbox discard <id>` | 失敗した変更を再送 / 変更を取り消す |
| `mcp [--read-only]` | MCP サーバーとして起動（stdio） |
| `keys` | キー設定で使える操作名の一覧 |
| `context` | このフォルダの `.asana.json`（作業中のプロジェクト・セクション・タスク）を表示 |
| `agent-guide` | AI エージェント向けの使い方（CLAUDE.md に貼るブロック）を表示 |
| `project status <プロジェクト>` / `project status post` | ステータス更新を見る / 投稿する |
| `project create <名前> [--team]` / `project archive <プロジェクト> [--undo]` | プロジェクトを作る / アーカイブ（オンラインのみ） |
| `project members <プロジェクト> [--add] [--remove]` | メンバーを表示 / 追加 / 外す（変更はオンラインのみ） |
| `api <パス> [-X メソッド] [-f/-F key=value] [--input]` | Asana の API を直接呼ぶ（[API を直接呼ぶ](#api-を直接呼ぶ)）。キャッシュと outbox を通らない |

`task list` と `search` の絞り込み:

```sh
asagao task list --due-before +7d          # 7 日以内に期限
asagao task list --overdue                 # 期限切れ
asagao task list --project 新居 --section 家具 --assignee any
asagao task list --tag 日用品 --all        # 完了済みも含む
asagao search 転居 届 --json               # 複数語は AND
```

日付は `YYYY-MM-DD` / `today` / `tomorrow` / `yesterday` / `+Nd` / `-Nd` で指定できます。

### 保存した検索

よく使う絞り込みに名前を付けて保存し、CLI・TUI（`space` `/`）・MCP から呼び出せます。保存先は `~/.config/asagao/searches.toml`（config.toml とは別のファイル）です。

```sh
asagao searches save 今週 --due-before +7d             # task list と同じフラグ
asagao searches save 請求 --query 請求書 --open         # --query があると search と同じ（全員の・完了済みも）
asagao searches save 今週 --due-before +14d --force     # 同じ名前は --force で上書き
asagao searches list                                   # 一覧（--json も可）
asagao searches run 今週                               # 実行（--limit で件数を変える）
asagao searches rm 今週
```

- 期日の相対指定（`+7d` など）はそのまま保存し、実行した日から数えます。プロジェクト名や担当者は実行するときに探します。
- `--query` のない検索は `task list` と同じく自分の未完了のタスク（`--project` なら全員の）を、ある検索は `search` と同じく全員の・完了済みも含むタスクを探します。`--assignee`・`--open`・`--completed`・`--all` で変えられます。
- searches.toml は手で書き換えてもかまいません（`[[search]]` の表を並べ、キーは `name`・`query`・`assignee`・`project`・`section`・`tag`・`field`・`due_before`・`due_after`・`completion`・`overdue`・`modified_within_days`・`limit`）。読めない内容や知らないキーがあるとエラーになり、ファイルは書き換えません。保存すると書き直すので、コメントは残りません。

### git 連携

コミットメッセージに `#<タスクID>` があればそのタスクにコメントし、`fixes #<タスクID>` なら完了にします。

```sh
asagao git scan                    # HEAD のコミットを読む
asagao git scan main..HEAD         # 範囲（古い順）。--dry-run で何をするかだけ表示、--offline で outbox に積むだけ
asagao git install-hook            # コミットのたびに読む post-commit hook を入れる（-C <フォルダ> で別のリポジトリ）
asagao git uninstall-hook          # 入れた hook を外す
```

- **タスク ID と読む条件**: `#` のあとに **10 桁以上**の数字（Asana のタスク ID は 16 桁ほど）。`#123` のような短い番号は GitHub の Issue・PR 番号として読みません。短い ID は `asana#<タスクID>` と書きます。`#` の直前が英数字・`_`・`&`・`/`・`#` のとき（`abc#…`、`&#…;`、`owner/repo#…`）と、数字のすぐあとに英字が続くときは読みません。
- **完了にするキーワード**: `fix` `fixes` `fixed` `close` `closes` `closed` `resolve` `resolves` `resolved`（大文字小文字は問わない）のあと、同じ行の空白か `:` に続く ID。GitHub と同じく、キーワードはすぐあとの 1 つにだけ効きます（`fixes #A, #B` は A を完了、B はコメントだけ）。`Revert "…"` のコミットはコメントだけで、完了にはしません（revert をさらに戻した `Reapply "…"` は、ふつうのコミットと同じく完了にします）。
- コメントには、コミットの短い ID、リポジトリ名、メッセージ全文と、末尾に `asagao-git: <コミットの ID>` が入ります。**同じコミットを何度読んでも**、この行があるタスクには何もしません（コメントが二重にならず、あとで未完了に戻したタスクをまた完了にもしません）。メッセージの中に引用された `asagao-git: …` は数えません。このコンピューターで付けたコメントは、ローカルストアにも記録します（そのため、`outbox discard` で取り消したコメントは、同じコミットを読み直しても付け直しません）。オンラインなら、判断の前にタスクを Asana から取り直します。ただし、見分けるのはコミットの ID なので、rebase・amend・squash で ID が変わったコミットを範囲で読み直すと、もう一度コメントします（`fixes` なら、未完了に戻したタスクもまた完了にします）。範囲の scan は、まだ push していない新しいコミットに使ってください。
- 変更はほかの書き込みと同じく outbox に積み、オンラインならすぐ送ります。ローカルストアにないタスクは「見つかりません」と表示します（`asagao sync` のあとでもう一度）。
- **hook**: `install-hook` はすでにある post-commit hook を上書きしません（足す 1 行を表示します）。`core.hooksPath` に従います。hook は `git commit`（`--amend` を含む）で作られたコミットだけを読み（HEAD の reflog で見分けます。`core.logAllRefUpdates=false` で reflog がないときは、参照があるコミットは読まずに `asagao git scan HEAD` を案内します）、rebase・cherry-pick・revert で作られたコミットは飛ばします。amend では、amend 前のメッセージになかった参照だけを扱います。失敗してもコミットは止めず、理由を表示します。hook は Asana を待ちません。タスクを取り直さずにローカルストアで判断して outbox に積み、送信は 3 秒だけ試します（送れなかった分は次の sync で送ります）。そのため、ローカルストアにないタスクは「見つかりません」になり、あとで自動では拾いません（`asagao sync` のあと `asagao git scan <コミット>` で拾えます）。hook と手動の scan が同時に同じコミットを読んでも、コメントは 1 つです。

すべてのコマンドで `--json` を付けると JSON で出力します。書き込み系のコマンドは `--dry-run` で、何も変えずに行われる変更だけを表示します（[AI エージェントから使う](#ai-エージェントから使う)）。

### API を直接呼ぶ

asagao にまだ無い操作は、`gh api` のように Asana の API をログイン中の認証で直接呼べます。

```sh
asagao api /users/me
asagao api /tasks/123 -f opt_fields=name,notes            # GET ではフィールドがクエリになる
asagao api /projects/456/tasks --paginate                 # 全ページの data を 1 つの配列にまとめる
asagao api -X PUT /tasks/123 -f name=新しい名前 -F completed=true
asagao api -X POST /tasks -f name=買い物 -f projects[]=456 --dry-run   # 送らずに中身を見る
asagao api -X POST /tasks --input task.json               # 本文の JSON をそのまま送る（- で標準入力）
```

- **キャッシュと outbox を通りません。** その場で Asana に送り、書き込み（`-X POST` / `PUT` / `DELETE`）はすぐ Asana を書き換えます。オフラインでの取り消しや同期の照合はありません。ローカルのキャッシュに反映するには、あとで `asagao sync` を実行してください。
- メソッドは既定で GET です。フィールドは GET と DELETE ではクエリに、POST と PUT では本文の `data` に入ります。`-f` は文字列、`-F` は `true` / `false` / `null` / 数値をその型にします。`key[]=値` で配列に足し、`a[b]=値` で入れ子にします（例: `-f custom_fields[789]=高`）。
- パスは API の中（`https://app.asana.com/api/1.0` の下）だけです。ほかのホストの URL や `..` は送らず、リダイレクトもたどりません（トークンがよそへ行かないように）。
- 応答の JSON は整形して標準出力に出します。端末の制御文字は `\u` のエスケープにするので、`jq` にもそのまま渡せます。Asana がエラーを返したときも本文を出し、終了コードは 1 になります。
- レート制限（429）は待って送り直します。5xx は GET / PUT / DELETE だけ送り直します（POST は二重に作らないように送り直しません）。

## MCP サーバー（AI から使う）

Claude Code などの MCP クライアントから、ローカルストアの検索や、タスクの作成・完了・コメントができます。

```sh
claude mcp add asana -- asagao mcp               # 読み書き
claude mcp add asana -- asagao mcp --read-only   # 読み取りだけ
```

| ツール | 内容 |
|--------|------|
| `list_tasks` | タスク一覧（デフォルトは自分の未完了。プロジェクト・セクション・担当者・期日・期限切れ・タグで絞り込み。`modified_within_days` で最近の更新） |
| `search_tasks` | タスク名・説明・コメントを全文検索 |
| `get_task` | タスク詳細。説明とコメントをマークダウンで返す（サブタスク・タグ・添付込み。オンラインなら最新を取得） |
| `get_attachment` | 添付ファイル。画像は画像として返すので、AI が中身を見られる |
| `list_projects` | プロジェクトとセクション（お気に入り → チームの順。`team` で絞る） |
| `sync_status` | 最終同期の時刻と、未送信の変更・競合 |
| `sync` | 未送信の変更を送り、Asana の変更を取り込む（`--read-only` では取り込むだけで、未送信の変更は送らない） |
| `create_task` | タスクを作成（`--read-only` では無効） |
| `update_task` | 完了 / 未完了・名前・説明（マークダウン）・期日・担当者・タグ・フォロワー・いいね・所属プロジェクトを変更（`--read-only` では無効） |
| `duplicate_task` / `upload_attachment` | 複製 / ファイルを添付（添付できるフォルダは下記。`--read-only` では無効） |
| `resolve_outbox` | 送れなかった変更を解決（こちらを採用 / Asana を採用 / 再送 / 取り消し。`--read-only` では無効） |
| `move_task` | 別のセクションへ移動・セクションの中で並べ替え（`insert_before` / `insert_after`。`--read-only` では無効） |
| `set_parent` | 親タスクを付け替える / 親から外す（`--read-only` では無効） |
| `delete_task` | タスクをサブタスクごと削除（`dry_run` で消えるものを確かめられる。`--read-only` では無効） |
| `rename_section` | セクションの名前を変更（`--read-only` では無効） |
| `list_tags` | タグとタスク数 |
| `list_saved_searches` / `run_saved_search` | 保存した検索の一覧 / 名前を指定して実行（読み取りだけ。保存・削除は CLI から） |
| `get_project` | プロジェクトの説明（マークダウン）・オーナー・期間・色・進み具合・ブリーフ・セクション・メンバー・ステータス更新 |
| `create_status_update` | ステータス更新を投稿（`--read-only` では無効） |
| `create_project` / `update_project` | プロジェクトを作る（`team`） / 説明・オーナー・日付・色を変える（オフラインでも積む）、アーカイブ・メンバー（オンラインのみ）。`--read-only` では無効 |
| `list_users` | ワークスペースのメンバー（担当者の指定に使う） |
| `list_inbox` | 受信トレイ（Asana の通知）。取得できないときは理由と最近の更新を返す |
| `mark_notifications` | 通知を既読 / 未読にする（`--read-only` では無効） |
| `add_comment` | コメント（マークダウン。`--read-only` では無効） |
| `list_time_entries` | タスクに記録した作業時間と合計（[タイムトラッキング](#タイムトラッキング)。プランで使えなければ出さない） |
| `add_time_entry` / `update_time_entry` / `delete_time_entry` | 作業時間を記録する / 直す / 削除する（`--read-only` では無効） |
| `get_context` | 起動したフォルダの `.asana.json`（作業中のプロジェクト・セクション・タスク）。gid と名前を引いて返す |

定型のプロンプトもあります（Claude Code では `/mcp__asana__standup` のように呼べます）。どれも引数 `project` を省くと `.asana.json` のプロジェクト、それもなければ自分のタスク全体が対象です。

| プロンプト | 内容 |
|------------|------|
| `standup` | 昨日やったこと・今日やること・困っていること |
| `triage_overdue` | 期限切れのタスクを 1 件ずつ見て、新しい期日・完了・任せる先を提案（変更は了承を得てから） |
| `weekly_review` | 1 週間で終わったこと・遅れていること・来週やること。プロジェクトならステータス更新の下書きも |

- 読み取りはローカルストアから行うので、高速でオフラインでも動きます。
- 書き込みは TUI・CLI と同じ「未送信の変更」を通ります。オンラインならすぐに送り、結果の `sent` で送れたかどうかが分かります。
- 期日は `YYYY-MM-DD` のほか、`today` / `tomorrow` / `+7d` などで指定できます。
- 説明とコメントはマークダウンで読み書きし、Asana のリッチテキストに変換します。画像は `![画像](asana:<ID>)` で表し、`get_attachment` で中身を見られます。
- TUI でできる操作は、MCP でもすべてできます（表示の切り替えなど画面の操作を除く）。
- `upload_attachment` で Asana に送れるのは、次のフォルダの中のファイルだけです（シンボリックリンクはたどった先で判定します）。AI が読んだ文章に仕込まれた指示で、鍵やトークンのファイルを送らされないようにするためです。CLI の `task attach` には制限はありません。
  - MCP サーバーを起動したフォルダ（`/`、ホームフォルダ、ホームの隠しフォルダの中のときは除く）
  - `~/Downloads`、`~/Desktop`
  - `config.toml` の `[mcp] upload_dirs` に書いたフォルダ
  - ただし、隠しファイル・隠しフォルダ（名前が `.` で始まる）の中、asagao 自身の設定・データ・キャッシュのフォルダ、ハードリンクが複数あるファイルは送れません。

```toml
[mcp]
upload_dirs = ["~/Documents/見積", "/Volumes/share/資料"]
```

## AI エージェントから使う

Claude Code などの AI エージェントに、リポジトリの作業と Asana のタスクを結び付けて使わせるための仕組みです。

### `.asana.json`

リポジトリのルート（またはその下のフォルダ）に `.asana.json` を置くと、そのフォルダで作業中のプロジェクト・セクション・タスクを決められます。

```json
{"project": "新居", "section": "家具", "task": "1203455468597469"}
```

- `project` と `section` は gid か名前（`--project` / `--section` と同じ）、`task` はタスクの gid（数字）。どれも省略できます（`section` は `project` と一緒に）。
- git のリポジトリの中では、カレントディレクトリからリポジトリのルート（`.git` のあるフォルダ）まで上に向かって探し、最初に見つかったものを使います。リポジトリの外では、カレントディレクトリだけを見ます。ホームフォルダより上は探しません。ほかのユーザーのファイル（共有の `/tmp` に置かれたものなど）はエラーにします。
- `asagao context` で、書かれた内容をキャッシュで引いて表示します（`--json` も可）。MCP では `get_context` です。
- `task create` は `--project`（と `--parent`）がないとき、`.asana.json` のプロジェクト（と、`--section` がなければセクション）に作ります。`--project ""` なら使いません。
- `task list` は `--project` がないとき、`.asana.json` のプロジェクトの未完了タスク（全員の）を表示します。全体を見るには `--project ""` です。
- `task show` は gid を省くと、`.asana.json` のタスクを表示します。
- `.asana.json` を使ったときは、エラー出力にそのファイルを書きます（`--json` の出力は変わりません）。`--no-context` を付けると `.asana.json` を使いません（`asagao mcp --no-context` も同じ）。
- 知らないキーや読めない内容があるとエラーにして、何もしません（書き間違いのせいで、別のプロジェクトで作業してしまわないように）。

### `--dry-run`

書き込み系のコマンド（`task create` / `complete` / `move` / `assign` / `tag` / `field` / …、`comment add`、`section rename`、`project status post` / `create` / `archive` / `members`、`inbox read`、`outbox resolve` / `retry` / `discard`、`git scan`）は、`--dry-run` を付けると何も変えずに、行われる変更だけを表示します。

```sh
$ asagao task complete 1203455468597469 --dry-run
--dry-run: 次の変更をするところでした（何も変えていません）:
  - ソファを買う: 完了にする
```

- ローカルストアの使い捨ての写し（自分だけが読める一時フォルダ）の上で同じ処理をするので、本番と同じ検査（日付の形式、セクションの有無など）が行われます。写しは終わったら消し、Ctrl-C で止めたときも消します（強制終了で残ったものは、次の `--dry-run` が消します）。
- Asana には何も送らず、ローカルストアや未送信の変更（outbox）、添付ファイルのキャッシュにも何も書きません。
- `--json` では `{"dry_run": true, "planned": [{"kind": "update_task", "task_gid": "…", "description": "…", "payload": {…}}]}` を返します。取り消される未送信の変更は `cancelled` に入ります。

### CLAUDE.md に貼るブロック

`asagao agent-guide` で、エージェント向けの使い方を表示します。リポジトリの `CLAUDE.md`（や `AGENTS.md`）に貼ってください。

```sh
asagao agent-guide >> CLAUDE.md
```

貼られる内容:

````markdown
## Asana（asagao）

このリポジトリの作業は Asana で管理している。Asana の操作には `asagao` を使う。

- 作業中のプロジェクト・タスクは `.asana.json` にある。最初に `asagao context --json` で確かめる。
- 読む: `asagao task list --json`（.asana.json のプロジェクトの未完了タスク）、`asagao task show [<gid>] --json`（gid を省くと .asana.json のタスク）、`asagao task search <語> --json`。
- 書く前に、同じコマンドに `--dry-run` を付けて、何が変わるかを確かめる。`--dry-run` は Asana にも outbox にも何も書かない。
- 書く: `asagao comment add <gid> <本文>`（進捗の報告）、`asagao task complete <gid>`、`asagao task create <名前> [--due <日付>]`（.asana.json のプロジェクトに作る）、`asagao task move <gid> --section <セクション>`。
- 書き込みはオフラインでも受け付け、次の同期で送る。`--json` の `sent` が false なら未送信。勝手に再送したり `asagao outbox` を操作したりしない。
- プロジェクトのアーカイブ、ほかの人への割り当て、ほかの人が担当するタスクの完了は、ユーザーに頼まれたときだけ行う。
- MCP サーバー（`asagao mcp`）が使えるなら、`get_context` で .asana.json を読み、定型の手順はプロンプト（standup・triage_overdue・weekly_review）を使う。
````

## 詳細画面

- リストやボードで `enter` を押すと、タスクの詳細を全画面で開きます。
- 詳細では `J` / `K` でサブタスクを選び、`enter` でそのサブタスクを開きます（`backspace` で親に戻る）。`x` で選んだサブタスクを完了、`S` で表示中のタスクにサブタスクを追加します。
- `c`・`@`・`m`・`e`・`C` は、詳細画面では表示中のタスクに対して働きます。

## エディタで編集する

`e` を押すと、タスクを次のようなマークダウンファイルにして `$EDITOR` で開きます。保存して閉じると、変えた項目だけを反映します（オフラインでも可）。

```markdown
---
name: ソファを買う
due: 2026-11-20
assignee: me
completed: false
section: 家具
---

候補は **ニトリ**

![画像](asana:1220507679870013)
```

- 説明はマークダウンから Asana のリッチテキストに変換して送るので、太字・リスト・見出しなどが Asana 側でも保たれます。画像の行は消さずに残してください。
- ファイルの拡張子は `.md` なので、エディタのマークダウン用プラグイン（Neovim の markview.nvim や markdown-preview.nvim など）がそのまま使えます。
- エディタは `config.toml` の `[ui] editor`、`$VISUAL`、`$EDITOR`、nvim、vi の順に探します。

## 画像の表示

タスクの説明やコメントに貼り付けた画像は、本文の中のその位置に表示します。添付ファイルの画像と合わせて `i` で全画面表示もできます（`n` / `p` で切り替え、`o` で OS のビューアで開く、`q` で戻る）。

- 本文中への表示は kitty 方式の「Unicode プレースホルダー」を使います（kitty・Ghostty、herdr 経由も可）。使えない端末では、その位置に `[画像1]` と表示します。
- エディタで編集するとき、画像はダウンロード済みのファイルのパスで書き出すので、snacks.nvim などの画像表示プラグインでも見られます。

- 対応している端末では、ターミナルの中に表示します: iTerm2・WezTerm（iTerm2 方式）、kitty・Ghostty（kitty 方式）
- それ以外の端末や、tmux・zellij などのマルチプレクサの中では、OS の画像ビューア（macOS ならプレビュー）で開きます（マルチプレクサは画像データを通さないことが多いため。herdr は kitty 方式を通すので端末内に表示します）
- 一度表示した画像は `~/.cache/asagao/attachments/` に保存し、オフラインでも表示できます
- 自動判定がうまくいかない場合は `config.toml` で指定できます:

```toml
[ui]
image_protocol = "kitty"   # auto / iterm / kitty / open
inline_images = "auto"     # 本文中に画像を表示するか: auto / on / off
```

- 本文の途中に貼った画像は、kitty・Ghostty（herdr 経由も含む）のときに、詳細画面のその位置に表示します（kitty の Unicode placeholder 方式）。tmux・zellij などのマルチプレクサの中では、`i` の全画面表示は映っても本文中の画像は空白になることがあるため、`image_protocol = "kitty"` を指定していても `auto` では表示せず、`[画像N]（i で表示）` と出します。使っている環境で本文中に表示できる場合は `inline_images = "on"` にしてください。
- 本文中の画像を読み込めなかったときは、`[画像N]（表示できません: 理由）` と出し、ステータスバーに理由を表示します。
- 端末に送る前に画像の大きさを確かめ、5000 万画素を超える画像は表示しません。大きさを確かめられない形式（HEIC・BMP など）は、全画面表示では端末に送らず、OS のビューアで開きます。

## タイムトラッキング

Asana の有料プラン（Advanced から）では、タスクに作業時間を日ごとに記録できます。TUI はスペースメニューの `h`、CLI は `time`、MCP は `*_time_entry` のツールで使います。

- 時間は `90`（分）・`90m`・`1h30m`・`1.5h`・`1:30` で書けます（1 件は 24 時間まで）。日は期日と同じ書き方（`2026-11-12`・`昨日`・`-2d`）で、省くと今日です。TUI では `1h30m 昨日` のように続けて入れます。
- 記録は見るときに Asana から取ります（同期では取りません）。記録・修正・削除はほかの変更と同じく、オフラインでも積んで次の同期で送ります。
- 送るのは時間と日だけです。説明・請求の区分・分類は Timesheets & Budgets のアドオンが要るので扱いません（読めた分類の名前は表示します）。
- Asana が 402（プランにない機能）を返したら覚えておき、以後は問い合わせず、メニューやツールにも出しません（この記憶は `asagao reset` でローカルデータを初期化すると消えます）。

## オフライン編集

完了・作成・コメントは、まずローカルストアに反映してから「未送信の変更」として保存し、すぐに Asana へ送ります。オフラインのときは保存しておき、次の同期で送ります。

- 送る前に Asana 側の現在の値を確認します。こちらが変えた項目を Asana 側でも別の値に変えていたら**競合**として送らずに残し、どちらを採用するか選べます（TUI の `!` または `outbox resolve`）。別の項目の変更なら、そのまま送ります。
- オフラインで作ったタスクには仮の ID（`local:...`）を振り、送信後に本物の ID に置き換えます。
- 未送信の変更があるタスクは、同期で Asana の値を取ってきてもローカルの値を優先して表示します。
- 参加しているチームとお気に入りのプロジェクトは、定期の取り込みのときにだけ取ります。取れなかったとき（プランで使えない 402・403 を除く）は、次の同期のたびに取り直します（そのたびに 1〜2 回のリクエストが増えます）。

## 環境変数

| 変数 | 内容 |
|------|------|
| `ASANA_TOKEN` | Personal Access Token。設定されていれば保存したトークンより優先（CI・ヘッドレス用。ふだんは `auth login --pat` で保存する方が手軽） |
| `ASANA_CLIENT_ID` | `auth.client_id` を上書き |
| `ASANA_CLIENT_SECRET` | ログイン時に Client secret を聞かずにこれを使う（例: `op run` と組み合わせる） |
| `XDG_CONFIG_HOME` / `XDG_DATA_HOME` | 設定 / データの置き場所 |

## データの置き場所

- 設定: `~/.config/asagao/config.toml`
- 保存した検索: `~/.config/asagao/searches.toml`（権限 600）
- ローカルストア: `~/.local/share/asagao/store.db`（SQLite、権限 600）

## 開発

```sh
go test ./...
go run ./cmd/asagao --help
```
