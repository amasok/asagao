# auth-token (認証とトークンの保存)

## Purpose

Asana にログインし、トークンを保存して使う振る舞いの基準。OAuth (PKCE) と Personal Access Token (PAT)、トークンの保存先 (keychain / 1Password / ファイル) の選び方、更新したトークンの保存、ログインしていないときの知らせ方を書く。コンテキストは「認証」(docs/design/context-map.md)。今の実装は `internal/auth` (oauth.go、tokensource.go)、`internal/auth/tokenstore`、`internal/cli/auth.go`、`internal/config`。

## Requirements

### Requirement: OAuth は PKCE と state で守る
OAuth のログインは、PKCE (S256) の verifier と、24 バイトの乱数の state を使う (MUST)。リダイレクト先は `http://localhost:<auth.redirect_port>/callback` (既定 8976)。state の合わない応答は 400 を返して無視し、待ち続ける。最初に合った応答だけを使い、そのあとの応答には 409 を返す。5 分で打ち切る。`--manual` では、利用者が貼り付けたコードを使う。
根拠: internal/auth/oauth.go:23-171; internal/config/config.go:98,122; TestCallbackHandlerMismatchThenOK, TestCallbackHandlerSecondCallback, TestReceiveCode, TestLoginManual

#### Scenario: state の違う応答が先に来る
- **WHEN** state の違うコールバックが来て、そのあと正しいコールバックが来る
- **THEN** 最初の応答は 400 で無視され、2 つ目のコードでログインが終わる

#### Scenario: scope が空
- **WHEN** `auth.scopes` が空
- **THEN** 認可の URL に `scope=default` を付ける

### Requirement: client ID と client secret
OAuth の client ID は `auth.client_id` から取り、環境変数 `ASANA_CLIENT_ID` があればそちらを使う (MUST)。client secret は、環境変数 `ASANA_CLIENT_SECRET`、保存したログイン、端末での隠した入力の順に探し、端末でなければエラーにする。
根拠: internal/config/config.go:95, :146-160; internal/cli/auth.go:26, :208-225; TestClientIDFromEnv

#### Scenario: 環境変数の client ID
- **WHEN** `auth.client_id` と `ASANA_CLIENT_ID` の両方がある
- **THEN** `ASANA_CLIENT_ID` を使う

### Requirement: PAT は確かめてから保存する
`auth login --pat` (または `client_id` が空のとき) は、PAT を引数ではなく、隠した入力か標準入力 (64 KiB まで) から読む (MUST)。空や空白を含むものは拒む。`/users/me` で確かめ (20 秒、再試行 1 回)、受け付けられたときだけ保存する。前の OAuth の client secret は残す。
根拠: internal/cli/auth.go:56-178; TestAuthLoginPAT, TestAuthLoginPATFailuresKeepEarlierLogin, TestAuthLoginPATAfterOAuth

#### Scenario: Asana が PAT を拒む
- **WHEN** `/users/me` が 401 か 403 を返す
- **THEN** 「Asana がこのトークンを受け付けませんでした」と知らせ、前のログインを残す (TestAuthLoginPATFailuresKeepEarlierLogin は 403 で確かめている)

#### Scenario: Asana が 5xx
- **WHEN** `/users/me` が 5xx を返す
- **THEN** トークンを保存せず、そのことを知らせる (応答の本文は出さない) (TestAuthLoginPATServerErrorFailsFast, TestAuthLoginPAT5xxBodyDoesNotLeak)

### Requirement: トークンの選び方
環境変数 `ASANA_TOKEN` があれば、保存先より優先し、更新しない PAT として使う (MUST)。保存した PAT も更新しない。OAuth のトークンは、期限が来たら refresh token で更新する。
根拠: internal/auth/tokensource.go:75-113; TestTokenSource, TestAuthPATWithEnvToken

#### Scenario: 環境変数と保存したトークンの両方がある
- **WHEN** `ASANA_TOKEN` があり、keychain にも OAuth のトークンがある
- **THEN** `ASANA_TOKEN` を使う

### Requirement: 更新したトークンを保存する
OAuth のトークンを更新したら、refresh token が変わったとき (または、アクセストークンを保存する保存先でアクセストークンが変わったとき) だけ保存する (MUST)。保存の前に保存先を読み直し、同じユーザーの新しいログインがあればそれを使う。別のユーザーなら `ErrLoginReplaced`、何もなければ `ErrNotLoggedIn` にする。`invalid_grant` でも読み直す。
根拠: tokensource.go:132-221; TestPersistingSource, TestPersistingSourceAdoptsNewLogin, TestPersistingSourceRefusesOtherUser, TestPersistingSourceRefreshErrors

#### Scenario: 別のプロセスで別のユーザーとしてログインした
- **WHEN** 更新したトークンを保存する前に、保存先が別のユーザーのものになっている
- **THEN** 保存せず `ErrLoginReplaced` を返す

### Requirement: 保存先の選び方
保存先は `auth.token_store` で選ぶ (MUST): `keychain` (既定。サービス `asana-tui`、ユーザー `default`)、`1password` (`op` コマンド。秘密は標準入力で渡し、アクセストークンは保存しない)、`file` (`$XDG_CONFIG_HOME/asana-tui/token.json`)。ほかの値は拒む。
根拠: internal/config/config.go:20-22,102-124,172-175; internal/auth/tokenstore/tokenstore.go:59-69; keychain.go; onepassword.go; TestNew, TestOnePasswordRoundTrip

#### Scenario: 1Password に保存する
- **WHEN** 保存先が `1password`
- **THEN** refresh token・PAT・client secret・ユーザー情報だけを保存し、アクセストークンは空にする

### Requirement: ファイルの保存先は自分だけが読める
`file` の保存先は、排他で作った一時ファイルを 0600 にして fsync し、rename で置き換える (MUST)。トークンのファイル自体と古い `token.json.tmp` がシンボリックリンクでも、その先には書かない。フォルダは 0700 にするが、フォルダの確かめと、読み込み (`Load`) はシンボリックリンクをたどる。
根拠: internal/auth/tokenstore/file.go:25-107; TestFileSaveDoesNotFollowSymlinks, TestFileSaveMakesExistingFolderPrivate, TestFileConcurrentSaves

#### Scenario: 既存のフォルダが 0755
- **WHEN** 保存先のフォルダが 0755 で既にある
- **THEN** 保存のときに 0700 にする

### Requirement: ログインしていないときの知らせ方
保存先に何もなければ `ErrNotLoggedIn` にし、ログインの方法を案内する (MUST)。CLI の書き込みのコマンドは失敗にせず、未送信として積んだことを知らせる。TUI は「未ログイン」と表示し、MCP の書き込みは「ローカルに保存したが送っていない」と返す。push はこれを「送っていない」(再送してよい) とみなす。
根拠: tokensource.go:19-20,55-91; internal/cli/write.go:83-131; internal/tui/model.go:1040-1043; internal/mcpserver/server.go:722-744; internal/sync/push.go:614-616; TestLoginHelp, TestLoginProblemsAreExplainedAndNewLoginIsUsed

#### Scenario: ログインせずに CLI でコメントする
- **WHEN** ログインしていない状態で `comment` を実行する
- **THEN** コメントは outbox に積まれ、コマンドは 0 で終わり、未送信だと知らせる

### Requirement: 保存先を移す
`auth migrate --to` は、移し先が今と同じなら拒む (MUST)。移し先がアクセストークンを保存しないなら外し、移し先に保存してから元を消す。
根拠: internal/cli/auth.go:305-346; TestAuthMigrateKeepsPAT

#### Scenario: ファイルから keychain へ移す
- **WHEN** 保存先が `file` のときに `auth migrate --to keychain` を実行する
- **THEN** keychain に保存してからファイルを消し、PAT は残る (TestAuthMigrateKeepsPAT)

## 未確認

- 本物の Asana・本物の keychain・本物の `op` での動き (テストは偽物だけ)。

## テストで確かめていない振る舞い (コードから読める)

- トークンの保存を、プロセスをまたいで排他する仕組みはない。保存の前に保存先を読み直すだけ (tokensource.go:132-176)。
