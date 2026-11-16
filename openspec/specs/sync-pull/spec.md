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
ワークスペースごとに通知を 100 件まで取り、そのワークスペースの行を置き換える (MUST)。401 と接続のエラーは同期を止める。4xx の拒否は状態を記録して同期を続ける。5xx と 429 はキャッシュを残す。通知の対象のタスクは、保存した受信箱の状態が使える (`InboxState().Available()`) ときだけ、1 回に 50 件まで新しい順に取り、上限を超えた分も片付けから守る。
根拠: internal/sync/inbox.go:23-65, :120-158; sync.go:345-355; TestInboxPerWorkspace, TestInboxUnauthorizedStopsSync, TestInboxRefusedKeepsSyncGoing, TestFullSyncKeepsNotificationTargetsBeyondFetchLimit

#### Scenario: 受信箱が 402
- **WHEN** 受信箱の取得が 402 を返す
- **THEN** 状態に payment を記録し、同期は続く

### Requirement: 再試行
Asana クライアントは、429 をどのメソッドでも、5xx を POST 以外 (ただし POST /batch は含む) で、5 回まで再試行する (MUST)。待ち時間は `Retry-After` (0〜24 時間のとき) か、2 の累乗の秒。401・403・404 は再試行しない。
根拠: internal/asana/client.go:76-78, :174, :206-238; TestRetriesOn429, TestPostIsNotRetriedOn5xx, TestRetryAfterOutOfRange

#### Scenario: タスクの作成が 503
- **WHEN** POST /tasks が 503 を返す
- **THEN** クライアントは再試行しない (二重に作らないため)

## 未確認

- カスタムフィールドの記録 `custom_fields:<gid>` の時刻は、Asana の時計ではなく PC の時計 (sync.go:438)。意図したものか。

## テストで確かめていない振る舞い (コードから読める)

- catch-up の push が失敗したとき、Events の token・`last_full_sync`・`last_poll` は保存済みで、`last_sync` だけ保存されない (sync.go:375-403)。
- token のないプロジェクトの Events が 412 以外で失敗したとき、全件ではなく `modified_since` で取る (events.go:61-68 で `fresh` にならない; sync.go:270-277)。
- `TestRunUnauthorizedDuringPull` は、`Me` のあとのどこかの取得で 401 になると、`Run` が 401 のエラーを返すことだけを確かめている。キャッシュが変わらないことは見ていない。
