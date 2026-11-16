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
| a3 | TUI のタスク作成とサブタスク作成を `Tasks.Create` に | `tui/write.go:334-378`、`tui/subtasks.go:115-127` | TestCreateTaskInProject、TestCreateSubtask、TestParseCreateInput |
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
