# 移行の順番

目標の形 (`architecture.md`) へ、小さな PR を積んで移る。判断の理由は ADR 0007。

## どの PR にも共通の決まり

1. **振る舞いを変えない。** `openspec/specs/` に差分がないこと。振る舞いを変えたくなったら、別の Issue にして、`openspec/changes/` に変更の提案を置く。
2. **既存のテストは、import のパスと呼び出し先の書き換え以外で変えない。** 入り口のテスト (`internal/cli/*_test.go`、`internal/mcpserver/*_test.go`、`internal/tui/*_test.go`) が、そのまま通ることが、振る舞いを変えていない証拠になる。
3. **`go build ./... && go vet ./... && go test -race ./...` と `npm run spec:validate` が通る。**
4. **トランザクションの単位を変えない。** 同じトランザクションで行っていたことを、2 つに分けない (#50・#55)。逆に、別だったものを 1 つにもしない (例: MCP の作成とタグ付けは、今は別のトランザクション。まとめるのは振る舞いの変更)。
5. **1 つの PR は、1 つのユースケースか、1 つの判断の移動。** 差分の目安は 500 行まで (テストを除く)。
6. マイグレーション (スキーマの変更) をしない。
7. PR の説明に、関係する ADR と仕様の要件の名前を書く。

## 段階 a: アプリケーション層 (`internal/app`)

ねらい: 3 つの入り口に重なっている書き込みの手順を、1 か所にする。

| PR | 範囲 | 入り口の変更 | 確かめるテスト (今あるもの) |
|---|---|---|---|
| **a1** | 作成の型 (`NewTask`・`TagEdit`・`TagRef`) を `internal/domain/task` に移す (store には型の別名)。`internal/app` を作る。ポート (`TaskStore`、`Clock`、`RichText`) と、ユースケース `Tasks.Create`。CLI と MCP のタスク作成をこれに置き換える | `cli/write.go:166-199`、`mcpserver/server.go:464-513` | TestCreateTaskAssignee、TestCreateTaskInvalidWritesNothing、TestCreateSubtaskAndAssignByName、TestMarkdownTagsAndComments、TestCLIRichTextTagsPerTarget、TestMCPRichTextTagsPerTarget |
| a2 | 依存の向きのテスト (`internal/archtest`)。今の違反を許可リストに入れる | なし | 新しいテストだけ |
| a3 | TUI のタスク作成とサブタスク作成を `Tasks.CreateHere` (解決済みの gid を受ける。ADR 0008) に | `tui/write.go:334-378`、`tui/subtasks.go:115-127` | TestCreateTaskInProject、TestCreateSubtask、TestParseCreateInput |
| a4 | コメントと status update (`Comments.Add`、`Status.Create`)。説明文の変換の組 (11 か所のうち 7 か所) がここに集まる | tui/write.go:388、tui/edit.go:334、tui/projects.go:247、cli/write.go:603、cli/project.go:115、mcpserver/server.go:701, :965 | TestTUICommentAndStatusWithoutLayoutTags、TestCLIRichTextTagsPerTarget、TestMCPRichTextTagsPerTarget |
| a5 | タスクの更新 (`Tasks.Update`: 名前・説明・期日・担当・完了・種類・承認)。期日の解釈 (6 か所) が集まる | tui/edit.go:269-301、tui/pickers.go:64-83、tui/write.go:237、tui/subtasks.go:198、cli/write.go:150, :482、mcpserver/server.go:520-629 | TestEditTaskInEditor、TestEditTaskErrorsAndNoChange、TestFailedWritesChangeNothing |
| a6 | 残りの書き込み (移動、セクション名、タグ、フォロワー、プロジェクト、依存、カスタムフィールド、添付、複製、いいね、通知の既読、解決) を、まとまりごとに 1〜3 PR で | write-flow の仕様の表 | 各入り口の書き込みのテスト |

段階 a が終わると、入り口は `store` の `*Local` を呼ばなくなる (許可リストから消える)。読み取りは、まだ `store` を呼んでよい。

### a1 の中身 (最初の PR の提案)

- 先に、作成のメソッドの引数と戻り値の型を、外部依存のないパッケージに移す。`CreateTaskLocal(ctx, store.NewTask, time.Time)` と `ResolveTagEdit(...) (store.TagEdit, error)` が store の型を使っていて (store/outbox.go:67, :260、store/names.go:14-30)、このままでは app のポートが store を import することになるため (依存の決まり 2)。
  - `internal/domain/task` に `NewTask`・`TagEdit`・`TagRef` を移す (中身はそのまま。`TagEdit.Empty` も)。
  - store には `type NewTask = task.NewTask` などの**型の別名**を残す。別名なので、store の関数の形も、ほかの呼び出し元も変わらない。別名は、入り口がすべて app を通るようになったら (段階 a の終わり) 消す。
- 新しいパッケージ `internal/app`:
  - `type TaskStore interface` — 今の `*store.Store` のうち、作成に要るメソッドだけ (`ResolveProject`・`ResolveSection`・`FirstSection`・`DefaultWorkspace`・`ResolveUser`・`ResolveTagEdit`・`CreateTaskLocal`・`ApplyTagEdit`)。引数と戻り値は標準の型と `domain/task` の型だけになるので、app は store を import せず、`*store.Store` がそのまま満たす。
  - `type RichText interface { TaskNotes(md string) (html, plain string) }` — 送り先ごとのメソッドにする (a4 で `Comment`・`StatusUpdate` を足す)。app が `richtext.Target` (adapter の型) を import しないため。実装は `internal/richtext` に足す小さな型 (`richtext.Converter{}`) で、中で今の `richtext.TaskNotes.FromMarkdown` と `PlainFromMarkdown` を呼ぶ。組み立て (今は cli と mcpserver を作る場所) がこれを app に渡す。b5 で送り先の型が `domain/rich` にできたら、`Convert(md, rich.Target)` にまとめてよい。
  - `type Clock func() time.Time`。
  - `type CreateTaskInput struct { Name, Parent, Project, Section, Due, Assignee, Notes string; Tags []string }`。
  - `func (t *Tasks) Create(ctx, in CreateTaskInput) (gid string, err error)`。
  - エラーの包み方の違い (CLI は `--due:`・`--assignee:`、MCP は `due_on:` だけ) は、ユースケースが**欄の名前を持つエラー型** (`*app.FieldError{Field: "due", Err}`) を返し、入り口が自分の言葉で包み直して、今と同じ文字列にする。
  - タグは、今の MCP と同じく、作成の**前に**解決し (解決できなければ何も書かない)、作成の**あとに**別のトランザクションで付ける。CLI はタグを渡さない (空の `Tags`)。空のとき、`ResolveTagEdit` と `ApplyTagEdit` はどちらも何もしない (store/names.go:30-34, :69-80) ので、呼んでも呼ばなくても振る舞いは同じ。
  - 依存の向き: app → domain/task だけ。a2 の許可リストに app の行は要らない。
- `Sender` (すぐ送る) は a1 には入れない。CLI の `reportWrite` と MCP の `s.push` は今のまま、入り口で呼ぶ。送り方のポートは a3 (TUI) のときに形を決める。a1 を小さく保つため。
  - a3 で、ポートは作らず入り口に残すと決めた (ADR 0008)。TUI は名前を解決しない入力の形 (`Tasks.CreateHere`、gid と解釈済みの期日を受ける) で作成し、送るのは今のまま `afterWrite` の同期。
- テスト: `internal/app` に、偽の `TaskStore` を使った単体テストを足す (担当の既定、プロジェクトとセクションの解決の順番、期日のエラーで何も書かないこと)。入り口のテストは変えない。
- 差分の見込み: app 約 150 行、テスト約 200 行、cli と mcpserver はそれぞれ 30 行ほど減る。

### a4 の中身

- コメントの作成者 (`store.Me` の戻り値、`AddRichCommentLocal`・`CreateStatusLocal` の引数) の型 `UserRef` を `internal/readmodel` に移す (ADR 0006 の 4。store には型の別名)。ほかの読み取りの型は c2 のまま。
- ポートは書き込みごとに小さく分ける: `CommentStore` (`Me`・`AddRichCommentLocal`) と `StatusStore` (`ResolveProject`・`Me`・`CreateStatusLocal`)。`TaskStore` には足さない (a1 の偽の store を直さないため。ADR 0008 と同じ考え)。`RichText` に `Comment`・`StatusUpdate` を足す。
- `Comments.Add(ctx, taskGID, md)`: タスクはどの入り口も gid で渡すので、形は 1 つ。`Status.Create` (プロジェクトを名前か gid で受ける。CLI・MCP) と `Status.CreateHere` (gid をそのまま。TUI。ADR 0008 の 2 と同じ形)。
- 呼ぶ順番は今と同じ (`ResolveProject` → `Me` → 変換 → 時計 → `*Local`)。エラーは store のものをそのまま返す (どの入り口も包んでいないため)。TUI はキーを押したときの時刻を返す `Clock` を渡す。

### a5 の中身

- 種類と承認の状態の読み方 (`ParseSubtype`・`ParseApprovalStatus` と、それが使う `SubtypeLabel`・`ApprovalLabel`、`TaskSubtypes`・`ApprovalStatuses`) を `internal/domain/task` に移す (中身はそのまま)。app が利用者の入力を読むのに要るため。store には呼ぶだけの関数と変数を残す (別名と同じ扱い。段階 a の終わりに消す)。
- `TaskStore` に `UpdateTaskLocal` を足す (a4 のように別のポートにしないのは、`Tasks.Update` が `ResolveUser` も使い、作成と同じ `Tasks` に置くため)。a1 の偽の store には、新しいテストのファイルでメソッドを足す (既存のテストのファイルは変えない)。
- `app.ParseDue(s, now)`: 期日の読み方の 1 か所。`Tasks.Create`・`Tasks.ResolveUpdate` と、TUI の作成の入力 (`parseCreateInput`)・期日の入力 (`submitDue`) がこれを呼ぶ。CLI の `parseDate` (テストだけが使っていた) は消した。入り口は `dates` を import しなくなる。
- 入力は 2 つの形 (ADR 0008 の考えと同じ):
  - `UpdateTaskInput` (利用者が打ったまま。nil の欄は変えない): `Due` は `ParseDue`、`Assignee` は `ResolveUser`、`Subtype`・`Approval` は domain/task の読み方で読む。
  - `TaskUpdate` (解決済み): 期日は YYYY-MM-DD、担当は gid、種類と承認は Asana の値。TUI の選択肢 (期日・担当・種類・承認の chooser、完了の切り替え、いいね) と、CLI の完了・いいねはこちらを使う。
- `Tasks.ResolveUpdate(ctx, in) (TaskUpdate, error)`: 書かずに読むだけ。読めない値は `*FieldError` (`resource_subtype` → `approval_status` → `due` → `assignee` の順に確かめ、最初の 1 つを返す。今の MCP と TUI の順番)。`Tasks.UpdateHere(ctx, gid, u)`: 説明文を変換して `UpdateTaskLocal` を 1 回呼ぶ (変える欄がなければ呼ばない)。`Tasks.Update` はこの 2 つを続けて呼ぶ。
  - 2 つに分けたのは、入り口が読むことと書くことの間に、ほかの確認や書き込みを挟んでいるため: TUI のエディタでの編集は、読んだあとにセクションの移動を書いてから欄を書く。MCP は、読んだあとにタグ・フォロワー・プロジェクト・依存・カスタムフィールドを解決し、「変えるものがない」「タスクがない」を確かめてから書く。CLI の承認は、読んだあとに承認タスクかを確かめる。どれも順番とトランザクションの単位は今のまま。
- いいね (`Liked`) も同じ欄の組に入れる (a6 の表にあるが、MCP では他の欄と同じ `UpdateTaskLocal` のトランザクションで送るため、分けられない)。TUI と CLI のいいねも `UpdateHere` にした。
- エラーの包み方は入り口が今と同じにする: TUI は `*FieldError` のまま (`due: `・`assignee: ` がそのまま今の文字列)、MCP は `due_on: `・`resource_subtype: `・`approval_status: ` で包み、担当は中のエラーだけ、CLI (位置の引数) は中のエラーだけ (`fieldErr`)。
- 時刻: MCP と TUI は 1 つの時刻を返す `Clock` を渡す (期日を読む時刻と書く時刻が、今と同じく同じになる)。CLI は `time.Now`。
- 結果: 入り口から `dates.Parse` と Markdown → html の変換 (`richtext.*.FromMarkdown`・`PlainFromMarkdown`) がなくなったので、`dependency-rules.md` の「関数の決まり」の 2 つを `internal/archtest/calls_test.go` に入れた (許可リストは空)。import の許可リスト (`allowed`) から消せる行はない (入り口は読み取りでまだ store を使う)。

### a6 の中身

- ポートは、a4 と同じく書き込みのまとまりごとに分けた: `RelationStore` (タグ・フォロワー・プロジェクト・依存・カスタムフィールド)、`SectionStore` (移動・セクション名)、`CopyStore` (複製・添付)、`NotificationStore` (通知の既読)。`CustomFieldView`・`EnumOptionView` を readmodel に移した (store には別名)。
- 形は a5 と同じく「解決だけ」と「解決済みの値で書く」の 2 つ:
  - `Relations.Resolve` (名前を解決するだけ。解決できなければ `*FieldError`、Field は MCP の引数の名前 `add_followers` など。タグは store のエラーのまま)、`ResolveFields` (カスタムフィールド。`custom_fields`)、`Apply` (タグ → カスタムフィールド → 依存 → フォロワー → プロジェクトの順に書く。今の MCP の順番)。TUI の選択肢は 1 件ずつの `SetTag`・`AddNewTag`・`SetFollower`・`SetProject`・`SetDependency`・`SetFieldHere` を使う。CLI は `flagErr` で Field を `--add` などに読み替える。
  - MCP の update_task は、解決 (`ResolveUpdate` → `Relations.Resolve`) → 「変えるものがない」とタスクの確認 → `ResolveFields` → `UpdateHere` → `Apply` の順で、今と同じ。
  - `Sections.Move` / `MoveHere` / `ResolveMove`、`Rename` / `RenameHere`。`Copies.Duplicate`・`Attach` (MCP のパスの制限は入り口に残す)。`Notifications.Resolve` / `Mark` / `MarkHere` (「すべて」の一覧の作り方は入り口ごとに違うので入り口の読み取りに残す)。
- 時刻: TUI と MCP の update_task は 1 つの時刻、CLI は書き込みごとに `time.Now` (今と同じ)。
- 結果: `internal/archtest/calls_test.go` に「入り口は store の書き込みを呼ばない」(`Local` で終わる公開のメソッドと `ApplyTagEdit`、`storeWrites`) を足し、まとまりごとに許可リストから消して、空になった。

## 段階 b: ドメイン層 (`internal/domain/...`)

ねらい: Asana と outbox の決まりを、SQL と外部ライブラリから離して、単体でテストできる関数にする。

| PR | 範囲 |
|---|---|
| b1 | `domain/gid`: ローカル gid の型・判定・作り方。`store.IsLocalGID` と `store.LocalPrefix` は、しばらく `domain/gid` を呼ぶ別名として残す |
| b2 | `domain/outbox`: 操作の種類と状態の定数、`SafeToResend`。`store.safeToResend` は呼ぶだけになる |
| b3 | `domain/outbox`: 畳み込み (`Fold(op, cur, changes) (op, deleteOp bool)`)。`UpdateTaskLocal` は読む → `Fold` → 書く、になる |
| b4 | `domain/task`: 欄の検証と承認の状態の決まり (`validateFields` と `UpdateTaskLocal` の承認の確認) |
| b5 | `domain/rich`: 送り先 (`Target`) と許すタグ。`internal/richtext` はこれを読む |
| b6 | `domain/outbox`: 照合で同じとみなす条件 (`sameTaskFields`、`sameComment`) |

- 移した関数には、表の形の単体テストを足す。今の store と sync のテストは変えない。

## 段階 c: リポジトリ (store を SQL だけに)

| PR | 範囲 |
|---|---|
| c1 | `store/describe.go` の表示用の変換 (`richtext.ToEditableMarkdown`) を、呼ぶ側 (app か入り口) に移す。store から richtext の import がなくなる |
| c2 | 表示用の読み取りの型 (`store.TaskSummary`・`TaskDetail`・`CustomFieldView` など約 50 種類)、表示の文言の関数 (`StatusLabel`・`ConflictLines`・`DescribeOp` など)、入り口が判定に使うエラーの値 (`ErrNotFound`・`ErrPushBusy`・`ErrUnsentChanges` など) を `internal/readmodel` に移す。store には別名を残す。そのあと読み取りのポート (`app.TaskQuery` ほか) を作り、入り口の読み取りを app 経由にする (型の移動で 1〜2 PR、入り口ごとに 1 PR) |
| c3 | `PruneTasks` の「消してはいけない」条件を、`domain/task` の文書とテストにする。SQL の WHERE は残す (1 回の DELETE で行うことが、途中で止まっても壊れないことの前提のため) |

- トランザクションの単位 (`Store.tx`、`UpsertTasks` の 500ms ごとの分け方、`PruneResults` の読み取りの計画) は変えない。TestUpsertTasksLetsOtherWritersIn、TestFullSyncFailingWhileWritingTasksKeepsTheCache、TestPruneResultsWithNothingToChangeTakesNoWriteLock が、そのまま通ること。

## 段階 d: ポート (Asana と認証)

| PR | 範囲 |
|---|---|
| d1 | `internal/asana/model` に型を移す。`internal/asana` に型の別名を残し、使う側を少しずつ移す |
| d2 | 認証が「送る前に失敗した」を自前のエラーで返す。sync から x/oauth2 の import を外す |
| d3 | `sync.Syncer` の `API` と `Store` を、sync で定義したインターフェースにする。`config.SyncConfig` を sync 自身の型にする |
| d4 | `internal/wire` (組み立て) を作り、`cli/root.go` の `apiClient`・`newClient`・`openStore` と、TUI・MCP の `sync.Syncer{...}` の組み立て (5 か所) を移す。cli から x/oauth2 の import がなくなる |

- 段階 d が終わると、依存の向きのテストの許可リストが空になる。

## 並行してよい作業

- richtext の不具合 (#58、#59、#61、#64〜#66) は、`internal/richtext` の中で閉じているので、いつ直してもよい。ただし b5 と同じ時期にぶつからないよう、リーダーが順番を決める。
- 機能 (#28〜#36) は、段階 a が終わってから始める。始めるときは、`openspec/changes/` に変更の提案を置く。
