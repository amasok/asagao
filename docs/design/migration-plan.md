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
| a6 | 残りの書き込み (移動、セクション名、タグ、フォロワー、プロジェクト、依存、カスタムフィールド、添付、複製、通知の既読) を、まとまりごとに 1〜3 PR で。いいねは a5 で済んだ。解決 (`sync.Resolve`) は段階 b に回した | write-flow の仕様の表 | 各入り口の書き込みのテスト |

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
- 結果: `internal/archtest/calls_test.go` に「入り口は store の書き込みを呼ばない」(`Local` で終わる公開のメソッドと `ApplyTagEdit`、`storeWrites`) を足し、まとまりごとに許可リストから消して、空になった。import の名前で修飾したもの (`sync.KeepLocal`) は数えないが、その名前をファイルの中で変数などに束縛し直していれば数える (`rebound`)。
- 解決 (outbox の keep_local など) は a6 から外した。入り口はすでに `sync.Resolve` を通っていて、store の書き込みを直接呼んでいないため。app に移すには `store.Op`・状態と種類の定数・`IsLocalGID` を store の外に出す必要があり、それは b1・b2 の仕事なので、そこで行う。

## 段階 b: ドメイン層 (`internal/domain/...`)

ねらい: Asana と outbox の決まりを、SQL と外部ライブラリから離して、単体でテストできる関数にする。

| PR | 範囲 |
|---|---|
| b1 | `domain/gid`: ローカル gid の判定・作り方 (乱数は引数)。gid は今どこでも string なので、型は作らない (作ると署名が広く変わる)。`store.IsLocalGID` と `store.LocalPrefix` は、しばらく `domain/gid` を呼ぶ別名として残す |
| b2 | `domain/outbox`: 操作の種類と状態の定数、`SafeToResend`。`store.safeToResend` は呼ぶだけになる。解決 (`sync.Resolve`) を app に移すのはここで (b1 の `domain/gid` のあと。a6 から回した) |
| b3 | `domain/outbox`: 畳み込み (`Fold(op, cur, changes) (op, deleteOp bool)`)。`UpdateTaskLocal` は読む → `Fold` → 書く、になる |
| b4 | `domain/task`: 欄の検証と承認の状態の決まり (`validateFields` と `UpdateTaskLocal` の承認の確認) |
| b5 | `domain/rich`: 送り先 (`Target`) と許すタグ。`internal/richtext` はこれを読む |
| b6 | `domain/outbox`: 照合で同じとみなす条件 (`sameTaskFields`、`sameComment`) |

- 移した関数には、表の形の単体テストを足す。今の store と sync のテストは変えない。

### b5 の中身

- `internal/domain/rich` に、送り先 (`Target`: `TaskNotes`・`Comment`・`StatusUpdate`) と、置き場所が決まっている要素 (`Element`: 見出し・区切り線・画像・表・リスト・引用・コード) を置いた。決まりは 3 つ: 送り先ごとに受け付けるか (`Target.Accepts`、`Target.Layout`、`Element.Layout`)、html のタグがどの要素か (`TagElement`)、入れ子にしてよいか (`Nests`: 引用の中のリスト、リストの項目の中の引用・見出し・コード・区切り線は書かない。#60・#80)。
- `richtext.Target` は `rich.Target` を元にした型にして (`type Target rich.Target`)、`FromMarkdown`・`PlainFromMarkdown`・`Fit` はそのまま。呼び出し元は変わらない。`layout()` はなくなり、Markdown から書くとき (見出し・区切り線・画像を要素にするか、文字にするか) と `Fit` の判定 (`hasLayoutTag`) は domain/rich を読む。
- a1 にある `Convert(md, rich.Target)` へのまとめは、しなかった。`app.RichText` の形を変えると、既存のテストにある偽の変換を書き直すことになるため。要るときに別の PR で行う。

## 段階 c: リポジトリ (store を SQL だけに)

| PR | 範囲 |
|---|---|
| c1 | `store/describe.go` の表示用の変換 (`richtext.ToEditableMarkdown`) を、呼ぶ側 (app か入り口) に移す。store から richtext の import がなくなる |
| c2 | 表示用の読み取りの型 (`store.TaskSummary`・`TaskDetail`・`CustomFieldView` など約 50 種類)、表示の文言の関数 (`StatusLabel`・`ConflictLines`・`DescribeOp` など)、入り口が判定に使うエラーの値 (`ErrNotFound`・`ErrPushBusy`・`ErrUnsentChanges` など) を `internal/readmodel` に移す。store には別名を残す。そのあと読み取りのユースケース `app.Query` とポート `app.QueryStore` を作り (ADR 0009)、入り口の読み取りを app 経由にする (型の移動で 1〜2 PR、入り口ごとに 1 PR) |
| c3 | `PruneTasks` の「消してはいけない」条件を、`domain/task` の文書とテストにする。SQL の WHERE は残す (1 回の DELETE で行うことが、途中で止まっても壊れないことの前提のため) |

- トランザクションの単位 (`Store.tx`、`UpsertTasks` の 500ms ごとの分け方、`PruneResults` の読み取りの計画) は変えない。TestUpsertTasksLetsOtherWritersIn、TestFullSyncFailingWhileWritingTasksKeepsTheCache、TestPruneResultsWithNothingToChangeTakesNoWriteLock が、そのまま通ること。

### c1 の中身

- `DiscardDetails(d, toMarkdown)`: 破棄した変更の rich text を Markdown にする変換を、呼ぶ側 (TUI と CLI) が渡す (`richtext.ToEditableMarkdown`)。どの欄を変換し、空なら平文にする、は store のまま。
- store から richtext の import は、まだ残る。`FitQueuedRichText` (outbox_richtext.go、#71。この表を書いたあとに入った) が `richtext.Target.Fit` を使うため。呼んでいるのは `sync.Push` で、sync は richtext を import できないので、変換を外から渡すには sync のポートと組み立てが要る。それは d3 (sync のポート) で行う。許可リストの行の理由を書き換えた。

### c2 の中身

- 最初の PR (読み取りの型の移動、その 1): タスクとプロジェクトの表示の型を `internal/readmodel` に移した。`tasks.go` に `TaskSummary`・`TaskDetail`・`MembershipView`・`StoryView`・`TaskRef`・`AttachmentView`・`DependencyView`・`TagCount`・`FieldOption` と、一覧の条件 `TaskFilter` (読み取りのポートの引数になるため)。`projects.go` に `ProjectView`・`SectionView`・`StatusUpdateView`・`Stats`。中身はそのまま (JSON の形を `readmodel/views_test.go` で押さえた)。store には型の別名を残したので、store の関数の形も、入り口も変わらない。
- 2 つ目 (その 2): 通知の型 (`Notification`・`InboxState`・`NotificationFilter` と、そのメソッド、`Inbox*` の値、`NotificationLabel`) を `readmodel/inbox.go` に、一覧の条件 `FilterSpec` (と `FilterError`・`MaxModifiedWithinDays`) を `filter.go` に、入り口が判定に使うエラーの値 (`ErrNotFound`・`ErrAmbiguous`・`ErrPushBusy`・`ErrUnsentChanges`・`ErrSectionWithoutProject`) を `errors.go` に移した。store には別名 (エラーは同じ値の変数、関数は呼ぶだけのもの) を残した。`FilterError` を別のパッケージの型にしたので、store の中の書き方をキーつきにした (`go vet` の composites)。
- 3 つ目 (その 3): 表示の文言 (`store/describe.go` のすべて: `OpLabel`・`OpNote`・`DescribeOp`・`DescribeChange`・`ConflictLines`・`FieldLabel`・`DiscardLine`・`DiscardDetails`・`AccountChangedHelp`・`TaskBadges` ほか、と `StatusLabel`・`StatusTypes`)、破棄した変更の型 `Discard` と `ResetReason` を `readmodel/describe.go` に移した。`Op` は b2 で `domain/outbox` に移ったので、文言の関数は `outbox.Op` を受ける (readmodel は domain を import してよい)。store には呼ぶだけの関数を残した。`DiscardDetail` だけは、store のテストがキーなしで書いているので (別名にすると `go vet` の composites に引っかかる)、store に同じ形の型を残して変換する。
- 4 つ目 (その 4): 読み取りのポート。`app.Query` (各メソッドは store の同じ名前のものを呼ぶだけ) と、そのポート `app.QueryStore` を置いた (ADR 0009)。入り口はまだ変えない。
- 5 つ目 (MCP): MCP の読み取りを `app.Query` 経由にした (`Server.query()`)。表示の型・一覧の条件・エラー・文言も、store の別名ではなく readmodel (と `outbox.Op`) を使う。MCP に残る store は、`*store.Store` (書き込みのユースケースと `sync.Syncer` に渡す。d3・d4)、`ReplaceInlineAssets`・`IsImageName` (表示の手伝い。readmodel に移すかは CLI・TUI の単位で決める) だけ。
- 6 つ目 (CLI): CLI の読み取りを `app.Query` 経由にした (`query(st)`)。表示の型・一覧の条件・エラー・文言は readmodel、`SubtypeLabel`・`ApprovalLabel` は `domain/task` を使う。CLI に残る store は、`*store.Store` と `store.Open` (組み立て。d4)、書き込み (`MarkDiscardsNotified`・`SetInboxWriteForbidden`・`ResetWithBackup` と `AnyUnsent`)、`IsLocalGID`、`auth` のログインのあとの確認 (`SyncState(KeyMeGID)`) だけ。最後の 1 つは、"" (まだ同期していない) を許す読み方で、`MeGID` では置き換えられない。ログインの書き込み (`SetInboxWriteForbidden`) と同じ所にあるので、d4 で組み立てを移すときに一緒に形を決める。
- 7 つ目 (TUI): TUI の読み取りを `app.Query` 経由にした (`queryOf(st)`)。d1 で `TaskTags` が `model.Ref` を返すようになったので、`QueryStore` に足した。表示の型・エラー・文言は readmodel、outbox の型と状態は `domain/outbox`、種類の名前は `domain/task` を使う。TUI に残る store は、`*store.Store` (組み立て。d4)、書き込み (`ResetWithBackup` と `ResetResult`、`MarkDiscardsNotified`)、画面の状態の保存 (`SyncState`・`SetSyncState` と `KeyCollapsed`)、カスタムフィールドの種類 (`CF*`)、`IsLocalGID`・`InlineAssets`・`ReplaceInlineAssets`。
  - 画面の状態の保存は、キャッシュの読み取りではなく、画面が自分の状態を書いて読み戻すもの。CLI の `SyncState(KeyMeGID)` と同じく、d4 で組み立てを移すときに形を決める (`QueryStore` には入れない)。
  - `CF*`・`IsLocalGID`・`InlineAssets`・`ReplaceInlineAssets` は、store の中の型や決まり (`domain/gid` など) を呼ぶだけの関数と定数なので、入り口から store の import をなくすとき (d4) に、行き先 (readmodel か domain) と一緒に移す。

## 段階 d: ポート (Asana と認証)

| PR | 範囲 |
|---|---|
| d1 | `internal/asana/model` に型を移す。`internal/asana` に型の別名を残し、使う側を少しずつ移す |
| d2 | 認証が「送る前に失敗した」を自前のエラーで返す。sync から x/oauth2 の import を外す |
| d3 | `sync.Syncer` の `API` と `Store` を、sync で定義したインターフェースにする。`config.SyncConfig` を sync 自身の型にする。outbox_richtext.go の `FitQueuedRichText` の変換 (`richtext.Target.Fit`) を sync のポートにし、store→richtext の許可リストの行を消す |
| d4 | `internal/wire` (組み立て) を作り、`cli/root.go` の `apiClient`・`newClient`・`openStore` と、TUI・MCP の `sync.Syncer{...}` の組み立て (5 か所) を移す。cli から x/oauth2 の import がなくなる |

- 段階 d が終わると、依存の向きのテストの許可リストが空になる。

## 並行してよい作業

- richtext の不具合 (#58、#59、#61、#64〜#66) は、`internal/richtext` の中で閉じているので、いつ直してもよい。ただし b5 と同じ時期にぶつからないよう、リーダーが順番を決める。
- 機能 (#28〜#36) は、段階 a が終わってから始める。始めるときは、`openspec/changes/` に変更の提案を置く。
