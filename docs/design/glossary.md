# 用語集 (ユビキタス言語)

コード・仕様・Issue・レビューで同じ言葉を使うための一覧。「コードの名前」は、移行 (Issue #69 の段階 a〜e1) が終わったときのもの。型や関数の名前が変わったら、ここも直す。

## 同期

| 用語 | 意味 | コードの名前 |
|---|---|---|
| キャッシュ | Asana のデータを手元に写した SQLite のファイル (`store.db`)。読み取りは常にここから | `store.Store` |
| 同期 (sync) | 送る (push) → 取り込む (pull) → その間に積まれた分をもう一度送る (catch-up)、の一続き | `sync.Syncer.Run` |
| 送る (push) | outbox の操作を積んだ順に Asana に送ること | `Syncer.Push` / `pushLocked` |
| 取り込む (pull) | Asana の変更をキャッシュに書くこと。全件・poll・Events の 3 つの取り方がある | `Syncer.Run` の後半 |
| 全件の同期 (full sync) | 期間を区切らずに取り、取れなかったタスクを片付ける (prune)。24 時間に 1 回、または初回 | `Result.Full` |
| poll | `modified_since` で、前回の poll から変わったものを取ること。1 時間に 1 回 | `Result.Poll` |
| Events の差分 | プロジェクトごとの Events API の token から、変わったタスクだけを取ること | `Syncer.readEvents` |
| 片付け (prune) | 全件の同期で、取れなかったタスクをキャッシュから消すこと。outbox に操作があるものは消さない | `store.PruneTasks` |
| Asana の時計 | Asana の応答の Date ヘッダーで測った時刻。`modified_since` と記録する時刻はこれで決める | `API.ClockOffset()` |
| push のロック | `store.db.push.lock` の排他ロック。送信・同期全体・初期化は 1 つのプロセスだけが行う | `store.TryLockPush`, `ErrPushBusy` |
| 当て直し (reapply) | 取り込んだタスクに、未送信の操作をもう一度当てること。取り込みでローカルの変更を消さないため | `reapplyUpdates` ほか |

## 送信待ち (outbox)

| 用語 | 意味 | コードの名前 |
|---|---|---|
| outbox | 送信待ちの操作の表。オフラインでも書き込めるのはこれがあるから | `outbox` 表, `outbox.Op` (`internal/domain/outbox`) |
| 操作 (op) | outbox の 1 行。種類 (kind)・対象 (`task_gid`)・payload・base を持つ | `outbox.Op` |
| 操作の種類 | `update_task`・`create_task`・`add_comment` など 18 個 | `outbox.Kind*` 定数 (store には別名 `store.Op*`) |
| base | 操作を積んだときの元の値。競合の判断と、捨てたときに戻す値に使う | `Op.Base` |
| 畳み込み | 新しい操作を積まずに、既にある未送信の操作に変更を合わせること。元の値に戻したら操作を消す | `outbox.Fold` (`UpdateTaskLocal` が呼ぶ) |
| 状態 | `pending` (未送信)・`sending` (送信中)・`conflict` (競合)・`failed` (失敗) | `outbox.State*` 定数 (store には別名 `store.Op*`) |
| 自動確認中 (verify) | 送れたか分からない操作の印。次に送る前に Asana を見て照合する。利用者の操作は要らない | `verify` 列, `MarkOpVerify` |
| 照合 (reconcile) | 自動確認中の作成について、Asana に既にできているかを探して決めること | `Syncer.reconcile` |
| 再送してよい | 同じ操作を 2 回送っても結果が変わらないこと。作成は再送してよくない | `outbox.SafeToResend` |
| 競合 (conflict) | 送る前に Asana を見たら、base ともローカルの値とも違う値になっていたこと | `MarkOpConflict` |
| 解決 (resolve) | 競合・失敗の操作に、利用者が `keep_local`・`keep_remote`・`retry`・`discard` を指示すること | `app.Outbox.Resolve` (b2 までは `sync.Resolve`) |
| 捨てた変更 (discard) | 自動または利用者の指示で捨てた操作の記録。payload ごと残す | `discarded_changes` 表 |
| 送った記録 (result) | ローカル gid と本物の gid の対応。`gid:`・`story:`・`att:`・`status:`・`tag:` | `sync_state` 表, `readmodel.Result*` (キーの接頭辞), `RecordedResults` |

## タスクとその周り

| 用語 | 意味 | コードの名前 |
|---|---|---|
| gid | Asana のものの ID (数字の文字列) | `GID` |
| ローカル gid | まだ Asana に作っていないものの仮の ID。`local:` + 16 桁の 16 進数 | `gid.Prefix`, `gid.IsLocal` (`internal/domain/gid`) |
| タスク | Asana のタスク。サブタスクも同じ。名前は空にできない | `model.Task` (`internal/asana/model`) |
| 種類 (subtype) | `default_task`・`milestone`・`approval` | `resource_subtype` |
| 承認の状態 | `pending`・`approved`・`rejected`・`changes_requested`。種類が `approval` のタスクだけ | `approval_status` |
| コメント (story) | タスクへのコメント。Asana の API では story | `model.Story` |
| status update | プロジェクトの状況報告 | `model.StatusUpdate` |
| 受信箱 (inbox) | Asana の通知 | `model.Notification` |
| 依存 | タスク同士の「先にやる」の関係 | `task_dependencies` 表 |
| 対象の参照 (task_refs) | 片付けたあとも、依存の相手の名前と状態だけ残したもの | `task_refs` 表 |

## 説明文 (rich text)

| 用語 | 意味 | コードの名前 |
|---|---|---|
| rich text | Asana の `html_notes` / `html_text`。HTML の一部のタグだけを使う | — |
| エディタ用の Markdown | 利用者が書く Markdown。メンションは `[名前](asana:user:GID)`、画像は `![画像](asana:GID)` | `richtext.ToEditableMarkdown` |
| plain | タグを除いた文字だけの説明 (`notes` / `text`)。rich text と組で送る | `Target.PlainFromMarkdown` |
| 送り先 (target) | rich text を書く場所。タスクの説明 (`TaskNotes`)・コメント (`Comment`)・status update (`StatusUpdate`) | `richtext.Target` |
| レイアウトのタグ | 見出し・区切り線・画像。タスクの説明でだけ使える (#63) | `Target.layout()` |
| 入れ子の決まり | 引用の中のリスト、リスト項目の中の引用・見出しを作らない (#60) | `asText` |

## 認証

| 用語 | 意味 | コードの名前 |
|---|---|---|
| PAT | Personal Access Token。更新しない | `auth login --pat` |
| OAuth | PKCE を使うログイン。refresh token で更新する | `auth.Login`, `persistingSource` |
| 保存先 (token store) | `keychain`・`1password`・`file` | `tokenstore.Store`, `auth.token_store` |
| ログインの置き換え | 更新したトークンを保存する前に、別のユーザーのログインに変わっていたこと | `auth.ErrLoginReplaced` |
| アカウントの変更 | キャッシュに記録したユーザー (`me_gid`) と、Asana が答えたユーザーが違うこと。同期は何もしない | `sync.ErrAccountChanged` |

## 構成 (Issue #69 で使う言葉)

| 用語 | 意味 |
|---|---|
| 入り口 | TUI・CLI・MCP サーバーの 3 つ |
| ユースケース | 入り口から呼ぶ、1 つの書き込みや読み取りの手順 (例: タスクを作る)。アプリケーション層に置く |
| アプリケーション層 | ユースケースを置く層。`internal/app` (ユースケースとポート) と `internal/sync` (同期) |
| ドメイン層 | Asana と outbox の決まりを、外部依存なしで書く層。`internal/domain/...` と `internal/dates` |
| リポジトリ | キャッシュの読み書きを SQL だけで行う部分。`internal/store` |
| ポート | アプリケーション層が使う外部のもの (Asana の API、キャッシュ、時計) のインターフェース |
| adapter | ポートを外部のライブラリで実装する部分 (SQLite、HTTP、goldmark、keychain など) |
| 組み立て (composition root) | adapter を作ってユースケースに渡す 1 か所。`internal/wire` (cmd/asagao が `cli.Wiring` で cli に渡す) |
