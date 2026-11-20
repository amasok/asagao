# sync-pull (同期: 取り込み)

## Purpose

Asana の変更をローカルのキャッシュに取り込む振る舞い (pull) の基準。全件・poll (modified_since)・Events API の差分の 3 つの取り方の選び方、記録 (sync_state) を進める時機、失敗したときにキャッシュを壊さないこと、未送信のローカルの変更を上書きしないこと、消えたタスクの片付け (prune) を書く。コンテキストは「同期」(docs/design/context-map.md)。今の実装は `internal/sync` (sync.go の `Syncer.Run`、events.go、inbox.go) と、`internal/store` の upsert と prune。

## Requirements

### Requirement: 同期は送ってから取り込む
`Syncer.Run` は、Asana のアカウントを確かめ、push の排他ロックを取り、未送信の操作を送ってから取り込む (MUST)。送るのに失敗したら、取り込まずに止める。取り込みのあと、その間にほかのプロセスが積んだ操作をもう一度送る (catch-up)。読み取り専用の MCP サーバー (`ReadOnly`) が使う `PullOnly` では送らず、未送信の数だけを数える。
根拠: internal/sync/sync.go:110-182, :397-401; internal/mcpserver/server.go:793; TestRunSendsChangesQueuedDuringPull

#### Scenario: 送れなかった
- **WHEN** 同期の最初の push が失敗する
- **THEN** 「未送信の変更を送れませんでした」のエラーで止まり、何も取り込まない

#### Scenario: ほかのプロセスが同期している
- **WHEN** push のロックをほかのプロセスが持っている
- **THEN** `ErrPushBusy` を返し、何も取り込まない (TestRunSkipsWhileLocked)

### Requirement: アカウントが変わったら何もしない
Asana が答えたユーザーの gid が、キャッシュに記録した `me_gid` と違うときは、ロックを取る前に `ErrAccountChanged` を返し、送りも取り込みもしない (MUST)。
根拠: sync.go:118-128; TestRunStopsWhenAccountChanged

#### Scenario: 別のアカウントでログインし直した
- **WHEN** 記録した `me_gid` と `Me` の答えが違う
- **THEN** 同期は `ErrAccountChanged` で止まり、キャッシュは変わらない

### Requirement: 取り方の選び方
次のどれかなら全件の同期にする (MUST): 指定された、`last_sync` か `last_full_sync` が空、`last_full_sync` から 24 時間以上たった、記録した時刻が Asana の時計より 2 分を超えて未来。全件か、`last_poll` から 1 時間以上たったときは poll にする。それ以外は、プロジェクトごとに Events API の差分を読む。
根拠: sync.go:140-167; TestRunFullThenIncrementalThenPrune, TestRunStampsInFutureForceFullSync, TestEventsMissedChangeCaughtByPoll

#### Scenario: 初めての同期
- **WHEN** `last_sync` が空
- **THEN** 全件の同期になる

#### Scenario: 記録した時刻が 3 時間未来
- **WHEN** `last_sync` が Asana の時計より 3 時間未来
- **THEN** 全件の同期になる (30 秒未来ならならない)

### Requirement: 時刻は Asana の時計で記録する
`modified_since` と、記録する時刻は、Asana の時計 (`Me` の応答の Date ヘッダーから測ったずれを足した時刻) で決める (MUST)。ずれが 5 秒未満なら 0 とみなす。`modified_since` は、前回 (poll なら前回の poll) の時刻から 2 分さかのぼる。
根拠: sync.go:129-131, :161-167; internal/asana/client.go:44-74; TestRunModifiedSinceUsesAsanaClock, TestClockOffsetFromDateHeader

#### Scenario: PC の時計が 1 時間進んでいる
- **WHEN** PC の時計が Asana より 1 時間進んでいる
- **THEN** `modified_since` は Asana の時計で計算される

### Requirement: Events の token は全部しまってから進める
プロジェクトごとの Events の token (`sync_state` の `events:<project gid>`) は、すべてのプロジェクト・受信箱・サブタスクをキャッシュにしまったあとで保存する (MUST)。途中で失敗したら、次の同期は同じ出来事を読み直す。1 回の同期で読むのは、1 プロジェクトあたり 100 件 × 20 ページまで。
根拠: sync.go:375-381; events.go:46-80; TestEventsSubtaskFailureIsRetriedNextSync, TestEventsBatchOfflineKeepsToken, TestEventsMorePagesThanOneSyncReads

#### Scenario: サブタスクの取得に失敗する
- **WHEN** 出来事を読んだあと、サブタスクの取得が失敗する
- **THEN** token は前のまま残り、次の同期で同じ出来事をもう一度読む

### Requirement: Events が使えないときは modified_since で取る
Events が 412 を返したら新しい token を保存し、初めての token なら全件、期限切れなら `modified_since` でそのプロジェクトを取る (MUST)。412 以外の失敗 (4xx・429・5xx・接続の切断) では、読めたページまでの token を残し (1 ページも読めなければ前の token)、`modified_since` で取る。失敗したプロジェクトだけがこの扱いになる。
根拠: events.go:55-74; sync.go:268-277; TestEventsFirst412ThenPagedEvents, TestEventsExpiredTokenFallsBack, TestEventsFailuresFallBack, TestEventsFailureOnSecondPage, TestEventsFailureFallsBackOnlyThatProject

#### Scenario: 1 つのプロジェクトの Events が 5xx
- **WHEN** プロジェクト A の Events が 5xx を返し、B は正常
- **THEN** A は `modified_since` で取り、B の token は進む

#### Scenario: 2 ページ目で失敗する
- **WHEN** Events の 1 ページ目は読め、2 ページ目が失敗する
- **THEN** 1 ページ目の出来事は使い、token は 1 ページ目のものを保存し、そのプロジェクトを `modified_since` で取る

### Requirement: batch で取れないときも modified_since で取る
Events に出たタスクを batch で取るのが Asana のエラー (再試行のあとの 5xx・429 や、いつもエラーになるタスク) で失敗したら、そのプロジェクトを `modified_since` で取り、同期を続ける (MUST)。接続のエラーと 401 は同期を止める。
根拠: sync.go:325-339; TestEventsBatchFailureFallsBack

#### Scenario: batch が 5xx
- **WHEN** batch の取得が再試行のあとも 5xx になる
- **THEN** そのプロジェクトを `modified_since` で取り直し、同期は成功する

### Requirement: 途中で失敗してもキャッシュを壊さない
タスクは 1 件ずつ丸ごと書くか書かないかで、約 500ms ごとのトランザクションに分けて書き、間を空けてほかの書き手を通す (MUST)。全件の同期が書き込みの途中で失敗したら、タスクの片付け (`PruneTasks`) をせず、`last_sync`・`last_full_sync`・`last_poll`・Events の token を変えない。ワークスペースとプロジェクトの片付け (`PruneWorkspaces`・`PruneProjects`) は、タスクを書く前に済んでいる。
根拠: internal/store/write.go:96-145; sync.go:213-217, :253-257, :383-403; pull_partial_test.go TestFullSyncFailingWhileWritingTasksKeepsTheCache; chunk_attack_test.go TestChunkAttackNoPruneBeforeAllWritten

#### Scenario: 全件の同期の途中でディスクのエラー
- **WHEN** 全件の同期でタスクを書いている途中に失敗する
- **THEN** 書けたタスクは新しい値、残りは古い値のままで、Asana から消えたタスクも消さない
- **AND** 次の同期ですべてそろう

### Requirement: 未送信の変更は取り込みで上書きしない
タスクを取り込むときは、Asana の値で書いたあと、同じトランザクションで、状態が `failed` でない未送信の操作 (タスクの更新・移動・タグ・フォロワー・プロジェクト・カスタムフィールド・依存) を当て直す (MUST)。ローカルで変えていない欄は Asana の値になる。
根拠: store/write.go:149-166; store/outbox.go:829; TestPullDoesNotOverwritePendingChange, TestFailedUpdateIsNotReappliedAfterPull, TestEventsUnsentEditWinsOnFallbackAndPoll

#### Scenario: 名前を変えて未送信のまま取り込む
- **WHEN** タスク名をローカルで変えて未送信のまま、Asana では期日が変わっている
- **THEN** 取り込んだあと、名前はローカルの値、期日は Asana の値になる

#### Scenario: 失敗した操作
- **WHEN** 状態が `failed` の更新がある
- **THEN** 取り込みでは当て直さない

### Requirement: 未送信のコメントの編集と削除は取り直しで戻さない
タスクのコメントと添付を Asana から取り直すときは、同じトランザクションで、状態が `failed` でない `update_story` の本文を当て直し、`delete_story`・`delete_attachment` の対象を消し直す (MUST, #34)。作成が送れてローカル gid が本物に替わったものにも当てる。コメントのリアクション (`reaction_summary`) もいっしょに取り込み、端末の制御文字を取り除いて表示する。
根拠: store/write.go, attachments.go, comment_edits.go; TestReplaceStoriesKeepsQueuedEdits, TestReplaceStoriesSkipsFailedEdits, TestStoryReactions

#### Scenario: 書き直したコメントを未送信のまま取り直す
- **WHEN** 自分のコメントを書き直して未送信のまま、タスクのコメントを取り直す
- **THEN** コメントは書き直した本文のままで、消したコメントや添付は戻らない

### Requirement: 消えたタスクの片付け
全件の同期で、ワークスペースが 1 つ以上あるときだけ、今回取れなかったタスクを消す (MUST)。outbox に操作があるタスク (状態を問わない) と、取れたタスクのサブタスクは消さない。依存でつながったタスクは、消す前に `task_refs` に名前と状態を写す。差分の同期では消さない。
根拠: sync.go:383-386; store/write.go:320-336; TestPruneKeepsLocalTasks, TestRunFullThenIncrementalThenPrune, TestFullSyncEmptyWorkspaceListKeepsTasks, TestOpenDependenciesAfterPrune

#### Scenario: ワークスペースの一覧が空
- **WHEN** 全件の同期で Asana がワークスペースを 1 つも返さない
- **THEN** タスク・プロジェクト・ワークスペースを消さず、`last_full_sync` を進めない

#### Scenario: ローカルで作ったタスク
- **WHEN** まだ送っていない作成 (ローカル gid) がある
- **THEN** 全件の同期でも消さない

### Requirement: Asana が見せないタスクは消す
batch の取得で 404 か 403 になったタスクは、どの同期でもサブタスクごと消す (MUST)。outbox に操作があるタスクは消さない。
根拠: internal/asana/events.go:136-137; internal/sync/events.go:139; store/write.go:347-374; TestEventsRemovedAndDeletedTasks, TestEventsKeepUnsentEdits

#### Scenario: 未送信の変更があるタスクが 404
- **WHEN** 未送信の更新があるタスクが batch で 404 になる
- **THEN** そのタスクは消えない

### Requirement: 受信箱の取り込み
ワークスペースごとに通知を 100 件まで取り、そのワークスペースの行を置き換える (MUST)。401 と接続のエラーは同期を止める。4xx の拒否でも同期を続ける。どれか 1 つのワークスペースが取れたら状態を OK にし、どれも取れず拒否があったときだけ、最初の拒否の状態 (forbidden・payment など) を記録する。5xx と 429 はキャッシュを残す。通知の対象のタスクは、保存した受信箱の状態が使える (`InboxState().Available()`) ときだけ、1 回に 50 件まで新しい順に取り、上限を超えた分も片付けから守る。
根拠: internal/sync/inbox.go:23-65 (状態は :56-63), :120-158; sync.go:345-355; TestInboxPerWorkspace, TestInboxUnauthorizedStopsSync, TestInboxRefusedKeepsSyncGoing, TestFullSyncKeepsNotificationTargetsBeyondFetchLimit

#### Scenario: 受信箱が 402
- **WHEN** ワークスペースが 1 つで、受信箱の取得が 402 を返す
- **THEN** 状態に payment を記録し、同期は続く

#### Scenario: 1 つのワークスペースだけ受信箱が拒否される
- **WHEN** ワークスペース A の受信箱は 403 で、B の受信箱は取れる
- **THEN** 状態は OK になり、A の拒否は記録しない (inbox.go:56-63)

### Requirement: 再試行
Asana クライアントは、429 をどのメソッドでも、5xx を POST 以外 (ただし POST /batch は含む) で、5 回まで再試行する (MUST)。待ち時間は `Retry-After` (0〜24 時間のとき) か、2 の累乗の秒。401・403・404 は再試行しない。
根拠: internal/asana/client.go:76-78, :174, :206-238; TestRetriesOn429, TestPostIsNotRetriedOn5xx, TestRetryAfterOutOfRange

#### Scenario: タスクの作成が 503
- **WHEN** POST /tasks が 503 を返す
- **THEN** クライアントは再試行しない (二重に作らないため)

### Requirement: マイタスクのセクションの取得
定期の取り直し (poll) のとき、ワークスペースごとに自分のマイタスクのリスト (`GET /users/me/user_task_list`) と、そのセクションを順番どおりに取り、キャッシュに入れる (MUST)。タスクは `assignee_section` を一緒に取り、自分のタスクのマイタスクのセクションを覚える。応答に項目がなければ今の値を保ち、null なら外す。
根拠: sync/my_sections.go; store/my_sections.go; asana/model (OptionalRef); TestSyncAndMoveMySections

#### Scenario: 同期でマイタスクのセクションを取る
- **WHEN** マイタスクに 今日やる・あとで があり、自分のタスクが あとで にある状態で同期する
- **THEN** キャッシュに 今日やる・あとで がこの順で入り、タスクのマイタスクのセクションは あとで になる

### Requirement: マイタスクのセクションが取れないとき
マイタスクのリストやセクションが取れないとき (402 以外) は同期を失敗にせず、今のキャッシュとタスクから分かったセクションを使い、次の取り直しでまた試す (MUST)。402 なら、そのワークスペースについて `plan_unavailable:my_section:<ワークスペースの gid>` を記録し、24 時間はそのワークスペースのマイタスクのセクションを取らず、出さない。そのあと試し直し、取れたら記録を消す。ほかのワークスペースは隠さない。
根拠: sync/my_sections.go; TestMySectionsPaymentRequired, TestMySectionsPlanPerWorkspace, TestMySectionsOldGlobalRecordIgnored, TestPushMoveMySection402PerWorkspace

#### Scenario: リストが取れない
- **WHEN** マイタスクのリストの取得に 404 が返る
- **THEN** 同期は成功し、機能は隠さない

### Requirement: チームとお気に入りの取り込み
同期は、ワークスペースごとに、参加しているチーム (組織のときだけ) と自分のお気に入りのプロジェクトを、ポーリングのときと、まだ取っていないとき (`teams:` + ワークスペースの gid が空) に取って置き換える (MUST)。402・403 は「無い」として空にする。ほかの失敗はキャッシュを残し、次の同期で取り直す。同期は止めない。プロジェクトの一覧はお気に入り (自分の並び順) → チーム名 → チームなしの順。
根拠: internal/sync/sync.go (syncTeamsAndFavorites); internal/store/project_fields.go; TestSyncTeamsAndFavorites, TestListProjectsFavoritesThenTeams

#### Scenario: お気に入りが取れなかった
- **WHEN** お気に入りの取得が 500 で失敗する
- **THEN** 前のお気に入りが残り、次の同期でもう一度取る

### Requirement: プロジェクトの概要は開いたときに取る
プロジェクトの概要 (TUI `O`・CLI `project show`・MCP `get_project`) は、オンラインなら開いたときにプロジェクト・タスクの数・ブリーフを取ってキャッシュし、表示はキャッシュから行う (MUST)。ブリーフが 402・403 なら出さない。取れなかったものはキャッシュの値を出す。
根拠: internal/sync/projects.go (FetchProjectOverview); TestFetchProjectOverview, TestProjectShowOffline, TestProjectFieldsOnline

#### Scenario: ブリーフがプランで使えない
- **WHEN** `GET /project_briefs/{gid}` が 402 を返す
- **THEN** 概要にブリーフを出さず、エラーにもしない

### Requirement: 作業時間の記録は開いたときに取る
タスクの作業時間の記録は同期では取らず、利用者が記録を見るとき (TUI の一覧、CLI の `time list`、MCP の `list_time_entries`) に `GET /tasks/{gid}/time_tracking_entries` で取ってキャッシュを置き換える (MUST)。送っていない記録 (ローカル gid)、取り始めより後に Asana が作った記録は残し、送っていない変更と削除はやり直す。取れなければキャッシュを出す。
根拠: sync/timetracking.go (FetchTimeEntries); store/time_entries.go (ReplaceTimeEntries); TestReplaceTimeEntries, TestUpdateTimeEntryLocal, TestTimeEntryChangedWhileSending, TestTimeAddAndList

#### Scenario: 送っていない変更のあとに取り直す
- **WHEN** 30 分の記録を 70 分に直し、送る前に Asana の一覧 (30 分のまま) を取る
- **THEN** キャッシュの記録は 70 分のまま

### Requirement: ワークスペースのカスタムフィールドの取得
定期の取り直し (poll) のとき、またはまだ取っていないとき、ワークスペースごとにカスタムフィールド (`GET /workspaces/{gid}/custom_fields`、選択肢つき) を取り、キャッシュに入れる (MUST)。プロジェクトに付けられるフィールドの一覧に使う。402 なら一覧を空にして `plan_unavailable:custom_fields:<workspace>` を記録し、403 なら一覧を空にする。ほかの失敗では同期を失敗にせず、今のキャッシュを保って次の同期でまた試す。
根拠: sync/push_fields.go (syncWorkspaceCustomFields); store/field_ops.go (ReplaceWorkspaceCustomFields); TestSyncWorkspaceCustomFields

#### Scenario: プランにない
- **WHEN** ワークスペースのカスタムフィールドの取得に 402 が返る
- **THEN** 同期は成功し、付けられるフィールドの一覧は空になり、フィールドの付け外しと選択肢の追加は出さない

### Requirement: ゴールとポートフォリオは表示するときに取る
ゴール・期間・ポートフォリオは同期では取らず、表示するとき (TUI で開いたとき、CLI `goal`・`portfolio`、MCP `list_goals`・`get_goal`・`list_portfolios`・`get_portfolio`) にオンラインなら取り直してキャッシュし、表示はキャッシュから行う (MUST)。402 以外の失敗はキャッシュを残してエラーを返す。期間だけが取れなかったときもゴールは置き換える。一覧から消えたゴールはキャッシュからも消し、1 件を取って 404 ならそのゴール (ポートフォリオ) を消す。
根拠: internal/sync/goals.go; TestFetchGoalsAndGoal, TestGoalsFailuresKeepTheCache, TestFetchGoneGoal, TestFetchPortfolios

#### Scenario: 期間だけが取れない
- **WHEN** `GET /goals` は答え、`GET /time_periods` が 500 を返す
- **THEN** ゴールはキャッシュに入り、期間は前のものが残り、エラーが返る

### Requirement: プランにない機能は 1 日隠して、また試す
`GET /goals` などが 402 を返したら、`plan_unavailable:<機能>:<ワークスペースの gid>` (readmodel.PlanKey) に時刻を記録し、そのワークスペースでその機能を 1 日 (PlanRecheckAfter) 隠して問い合わせない (MUST)。1 日たったら再び出して試し、答えが返れば記録を消す。402 はエラーにせず、読む側が「プランで使えない」と伝える。
根拠: internal/sync/plan.go; internal/readmodel/plan.go; TestGoals402HidesThemForADay, TestGoals402IsPerWorkspace, TestPlanOffFor

#### Scenario: お試しを始めたワークスペース
- **WHEN** ゴールが 402 で隠れてから 1 日たち、Asana がゴールを返すようになっている
- **THEN** TUI のメニューにゴールが戻り、開くとゴールが取れて、402 の記録が消える

#### Scenario: 無料のワークスペースの 402
- **WHEN** 別のワークスペース (無料) でゴールが 402 を返した記録がある
- **THEN** 既定のワークスペース (有料の組織) ではゴールを隠さない。ワークスペースが消えれば、その記録も消える

#### Scenario: 読めない記録
- **WHEN** 記録が時刻として読めない、または未来の時刻である
- **THEN** 隠さずに試す

### Requirement: カスタムタイプの取得
プロジェクトのカスタムフィールドを取るとき (全件の同期、変更のイベント、まだ取っていないとき) に、そのプロジェクトで使える種類とステータス (`GET /custom_types?project=`) を取ってキャッシュに入れる (MUST)。403 なら一覧を空にする。ほかの失敗では同期を失敗にせず、次の同期でまた試す。
根拠: sync/sync.go (syncCustomTypes); store/customtypes.go (ReplaceProjectCustomTypes); TestSyncCustomTypesOfProject

#### Scenario: 全件の同期で取り直す
- **WHEN** 種類を取ったプロジェクトを、全件でない同期のあとに全件で同期する
- **THEN** 全件でない同期では問い合わせず、全件の同期で取り直す

### Requirement: カスタムタイプの取得の 402
種類の取得に 402 が返ったら、一覧を空にし、ワークスペースについて `plan_unavailable:custom_types:<workspace>` を記録して、24 時間はそのワークスペースのどのプロジェクトでも問い合わせない (MUST)。そのあと試し直し、答えが来たら印を消す。ほかのワークスペースは隠さない。
根拠: sync/sync.go (syncCustomTypes); sync/plan.go; TestSyncCustomTypesPaymentRequired

#### Scenario: プランにない
- **WHEN** 種類の取得に 402 が返る
- **THEN** 同期は成功し、そのワークスペースでは種類の選択肢を出さず、24 時間は問い合わせない

### Requirement: タスクの種類を読む
タスクの `custom_type`・`custom_type_status_option` は省ける欄として読む (MUST)。断られたら、まずこの 2 つだけを外して読み直し、それでも断られたら省ける欄を全部外す。種類を読まなかったタスクでは、キャッシュの種類を保ち、競合の比べにも使わない。
根拠: asana/customfields.go (readTasks, fieldTier); store/write.go; readmodel/fieldvalues.go; TestCustomTypeFieldsRefusedAlone, TestCustomTypeNotFetchedIsKept, TestCustomTypePartialAndCleared

#### Scenario: 種類の欄だけを断るプラン
- **WHEN** タスクの読み取りが、opt_fields に `custom_type` があるときだけ 402 になる
- **THEN** カスタムフィールドと依存関係は読まれ、キャッシュの種類は消えない

### Requirement: 種類の gid を名指しして読む
タスクの種類とステータスは、gid と名前を名指しして (`custom_type.gid`・`custom_type.name`・`custom_type_status_option.gid`・`custom_type_status_option.name`) 読む (MUST)。Asana は `.name` だけを頼むと gid を返さない。種類かステータスが gid 無しで来たタスクでは、キャッシュの種類を保つ。
根拠: asana/resources.go (TaskFields); store/customtypes.go (upsertTaskCustomType); TestTaskFieldsHaveCustomType, TestCustomTypeReadWithGid, TestCustomTypeWithoutGidIsKept

#### Scenario: gid を名指しして読む
- **WHEN** 種類「バグ」・ステータス「対応中」のタスクを読む (Asana は名指しした欄だけを返す)
- **THEN** キャッシュのタスクに種類とステータスの gid と名前が入り、そのステータスを変えられる

### Requirement: 種類を読めていなかったキャッシュ
種類を `custom_type.name` だけで読んでいた版のキャッシュに、種類の無いカスタムタイプのタスクがあれば、マイグレーション (23 番) で最後の全件の同期の記録を消し、次の同期を全件にして種類を読み直す (MUST)。そのタスクが無いキャッシュは、全件の同期の記録を保つ。
根拠: store/schema.go (migrations); TestMigrateStoreWithCustomTasksWithoutType

#### Scenario: 種類の無いカスタムタイプのタスク
- **WHEN** 種類の無いカスタムタイプのタスクがあるキャッシュを、新しい版で開く
- **THEN** 次の同期は全件になる

### Requirement: タスクを取るのは同期するプロジェクトだけ
同期はワークスペースのプロジェクトの一覧を全部取り込むが、セクション・ステータス・タスク・Events を取るのは同期するプロジェクトだけにする (MUST)。同期するプロジェクトは、一度開いたもの、お気に入り、前の版で取っていたもの (Events のトークンがある)、`all_projects = true` なら全部、`lazy_projects = false` なら参加しているもの。マイタスク (自分の担当) は、どのプロジェクトのタスクでもいつも取る。
根拠: sync/lazy.go (toSync, pullMyTasks); sync/sync.go (run); TestLazySyncFetchesProjectListOnly, TestLazyStoreSyncedBefore, TestLazyOff

#### Scenario: 最初の同期
- **WHEN** `lazy_projects` が既定のまま、参加しているプロジェクト p1 (お気に入りでない) と、お気に入りの p2 がある状態で初めて同期する
- **THEN** p2 のタスクとマイタスクを取り、p1 には 1 回もリクエストしない (Events も読まない)。p1 は一覧に「未取得」として出る

#### Scenario: 前の版で同期していたストア
- **WHEN** 前の版で同期した (Events のトークンがある) プロジェクトがあるストアで同期する
- **THEN** そのプロジェクトは Events の差分で今までどおり取り込み、初めからは取り直さない

### Requirement: 初めて取るものは未完了を先に取る
`lazy_projects` のとき、初めて取るプロジェクトとマイタスクは、まず未完了のタスクだけを取る (`completed_since` = 同期を始めた時刻) (MUST)。全部の未完了とそのサブタスクを取り込んでから、完了済み (`completed_since` と `modified_since` = `completed_days` 日前) を取る。完了済みで新しく取るサブタスクは、完了したタスクのものだけ。
根拠: sync/lazy.go (pullProject, pullMyTasks, pullCompleted); TestLazyFetchesIncompleteFirst

#### Scenario: お気に入りを初めて取る
- **WHEN** 初めての同期でお気に入りの p2 を取る
- **THEN** マイタスクと p2 の未完了を取る問い合わせが全部済んでから、完了済みを取る問い合わせが 2 回 (マイタスクと p2) 出る

### Requirement: どこまで取れたかを覚える
取れたところまでを `project_tasks:<gid>` / `my_tasks:<workspace gid>` に `open` (未完了まで) / `all` として覚える (MUST)。`open` のものは、次の同期か `FetchCompleted` で完了済みを取る。`DeferCompleted` の同期 (TUI) は完了済みを取らずに `completed_pending` を返す。`completed_days` が 0 (負は 0) なら完了済みは取らず、すぐ `all` にする。
根拠: sync/lazy.go (finishPull, completedPending, FetchCompleted); TestLazyDeferCompleted, TestLazyCompletedDaysZero, TestTUIFetchesCompletedAfterSync

#### Scenario: TUI の最初の同期
- **WHEN** `DeferCompleted` で初めて同期する
- **THEN** 未完了だけを取って `completed_pending` を返し、続く `FetchCompleted` で完了済みを取る

### Requirement: 開いたときに取る
プロジェクトを開いたとき (TUI でカーソルがとどまる、CLI・MCP でタスクの一覧や検索にプロジェクトを指定する)、それを同期するプロジェクトとして記録し、タスクが未取得ならその場で取る (MUST)。取り込むだけで、未送信の変更は送らず、上書きもしない。TUI でカーソルが通り過ぎただけのプロジェクトは取らない。
根拠: sync/lazy.go (OpenProject); TestOpenProjectFetchesAndTracks, TestOpenProjectWithCompleted, TestTaskListFetchesUnfetchedProject, TestListTasksFetchesUnfetchedProject, TestTUIOpenProjectFetchesIncompleteThenCompleted, TestTUIPassingByFetchesNothing

#### Scenario: CLI で未取得のプロジェクトを一覧する
- **WHEN** オンラインで `asagao task list --project 仕事` を実行し、仕事のタスクがまだキャッシュにない
- **THEN** 仕事のタスクを Asana から取ってから一覧を出す。もう一度実行しても Asana には聞かない

### Requirement: 開いても取れないとき
開いたプロジェクトを取れないとき (オフライン、Asana に届かない、`--offline`、ほかの同期の最中で ErrPushBusy) は、記録だけを残して次の同期で取る (MUST)。CLI は標準エラー、MCP は `note`、TUI は一覧の注記で「まだ取得していません」と伝える。
根拠: sync/lazy.go (OpenProject); TestOpenProjectWhileBusy, TestTaskListUnfetchedProjectOffline, TestListTasksUnfetchedProjectOffline, TestTUIUnfetchedProjectOffline

#### Scenario: オフラインで開く
- **WHEN** `asagao task list --project 仕事 --offline` を実行する
- **THEN** Asana には聞かず、標準エラーに「まだ取得していません…次の同期で取得します」と出し、次の同期で仕事のタスクを取る

### Requirement: 未取得のプロジェクトを見せる
タスクが未取得のプロジェクトは、TUI のサイドバーで `◌`、`project list` で `[タスク未取得]`、MCP の `list_projects` で `unfetched: true` と示す (MUST)。キャッシュだけの検索 (プロジェクトの指定なし) は、参加している未取得のプロジェクトの数を添える (CLI は標準エラー、MCP は `note`)。`sync` は Asana へのリクエスト数を出す。
根拠: store/project_tasks.go (fetchedSQL, CountUnfetchedProjects); cli/lazy.go (noteUnfetched); cli/sync.go; TestProjectTaskStates, TestTaskListUnfetchedProjectOffline, TestListTasksUnfetchedProjectOffline, TestLazySyncFetchesProjectListOnly

#### Scenario: 検索
- **WHEN** 未取得のプロジェクトが 2 件ある状態で `asagao search 何か` を実行する
- **THEN** 標準エラーに「タスクをまだ取得していないプロジェクトが 2 件あり、そこは探していません」と出る

### Requirement: タスクの並びを取り込む
取り込みは、セクションの中のタスクの並びとサブタスクの並びを Asana の順で保存する (MUST)。プロジェクトを全部取ったときは返った順を、一部だけ取ったとき (Events、modified_since、完了済みの取得) は、取ったタスクのセクションを `GET /sections/{gid}/tasks` で取り直した順を、サブタスクは `GET /tasks/{gid}/subtasks` の順を使う。Events がサブタスクを挙げたら、その親のサブタスクを取り直す。
根拠: sync/lazy.go (pullProject, refreshSections, refreshParents, storeSubtasks, pullCompleted); store/order.go; TestPullKeepsAsanaOrder, TestPullCompletedTakeTheirPlace, TestOrderFromListings

#### Scenario: Events で並べ替えが届く
- **WHEN** 同じセクションの中でタスクが並べ替えられ、Events がそのタスクを挙げる
- **THEN** そのセクションの並びを取り直し、`list_tasks` と TUI がその順で並べる

### Requirement: 並びを取れないとき
セクションや親の並びが 404・403 なら、今の並びのまま同期を進める (MUST)。オフラインと 401 は、ほかの取得と同じく同期の失敗とし、Events の token を進めない。それ以外の失敗 (再試行しても続く 5xx・429、400 など) は同期を止めず、そのセクションや親を覚えておき、次の同期で取り直す。
根拠: sync/lazy.go (refreshSections, refreshParents, orderFailed); TestPullOrderGone, TestPullOrderFailsLater

#### Scenario: セクションが消えていた
- **WHEN** 並びを取り直すセクションが 404 を返す
- **THEN** 同期は成功し、そのセクションの並びは前のまま

#### Scenario: Asana が一時的に答えない
- **WHEN** 並びを取り直すセクションが 500 を返し続ける
- **THEN** 同期は成功して token が進み、次の同期でそのセクションを取り直す

### Requirement: 並びの分からないタスク
並びの分からないタスクは、分かるタスクのあとに、今までの順 (一覧は完了・期日・名前、サブタスクは作った順) で並べる (MUST)。一覧は、セクションで絞ったときは位置の順。プロジェクトで絞ったときは、位置の分かるタスクが 1 つでもあればセクションの順にまとめ、その中は位置の順。
根拠: store/read.go (ListTasks, GetTask); TestOrderUnknownKeepsOldOrder, TestOrderFromListings

#### Scenario: 並びが分からない
- **WHEN** #51 より前に取り込んだタスクを、全件の同期の前に一覧する
- **THEN** 今までの順 (完了・期日・名前) で並ぶ

## 未確認

- カスタムフィールドの記録 `custom_fields:<gid>` の時刻は、Asana の時計ではなく PC の時計 (sync.go:438)。意図したものか。

## テストで確かめていない振る舞い (コードから読める)

- catch-up の push が失敗したとき、Events の token・`last_full_sync`・`last_poll` は保存済みで、`last_sync` だけ保存されない (sync.go:375-403)。
- token のないプロジェクトの Events が 412 以外で失敗したとき、全件ではなく `modified_since` で取る (events.go:61-68 で `fresh` にならない; sync.go:270-277)。
- `TestRunUnauthorizedDuringPull` は、`Me` のあとのどこかの取得で 401 になると、`Run` が 401 のエラーを返すことだけを確かめている。キャッシュが変わらないことは見ていない。
