# outbox-push (送信待ちの操作・送信・照合)

## Purpose

ローカルの変更を outbox (送信待ちの操作) に積み、Asana に送り (push)、送れたか分からないときに Asana を見て照合する振る舞いの基準。操作の種類、ローカル gid、畳み込みの規則、送る順番、結果ごとの扱い、再送してよいかの判断、照合、利用者による解決を書く。コンテキストは「送信待ち」と「同期」(docs/design/context-map.md)。今の実装は `internal/store` (outbox.go、outbox_send.go、tags.go、relations.go、customfields.go、dependencies.go、sections.go、projects.go、notifications.go、results.go、discard*.go) と `internal/sync` (push.go、reconcile.go、resolve.go)。

## Requirements

### Requirement: 操作の種類
outbox の操作の種類は 18 個 (MUST): `update_task`、`create_task`、`add_comment`、`move_task`、`add_follower`、`remove_follower`、`add_project`、`remove_project`、`duplicate_task`、`upload_attachment`、`set_custom_field`、`add_tag`、`remove_tag`、`add_dependency`、`remove_dependency`、`rename_section`、`create_status`、`mark_notification`。
根拠: internal/store/outbox.go:28-33; relations.go:18-23; customfields.go:38-40; tags.go:15-16; dependencies.go:20-21; sections.go:16; projects.go:16; notifications.go:21

#### Scenario: タスクにタグを付ける
- **WHEN** 既にあるタグをタスクに付ける
- **THEN** `add_tag` の操作が積まれ、payload に `tag`・`tag_name`・`workspace` が入る

### Requirement: 操作の状態
操作の状態は `pending`・`sending`・`conflict`・`failed` の 4 つ (MUST)。ほかに「自動確認中」の印 `verify` と、`sent_at`・`last_sent_at`・`attempts` を持つ。`sending` は未送信の数に入れる。
根拠: outbox.go:35-37, :57-63, :406-411; outbox_send.go:29; store/describe.go:16-40

#### Scenario: タスク名を変える
- **WHEN** 送っていないタスクの名前を変える
- **THEN** `update_task` の操作が `pending` で積まれ、payload に `name`、base に元の名前が入る

### Requirement: ローカル gid
オフラインで作ったもの (タスク・コメント・複製・添付・status update・新しいタグ) には、`local:` と 16 桁の 16 進数 (crypto/rand の 8 バイト) のローカル gid を付ける (MUST)。判定は `store.IsLocalGID` で行う。作成が送れたら、タスクのローカル gid をキャッシュの表と outbox の `task_gid` で本物の gid に書き換え、`sync_state` に `gid:<local>` → 本物 を記録する。payload の中のローカル gid は書き換えず、送るときに記録から引く。
根拠: outbox.go:40-44, :602-682, :1004-1008; internal/sync/push.go:742, :838, :867, :896-901; TestPushSubtaskOfOfflineParent, TestReplaceLocalTaskGIDMovesCustomFields

#### Scenario: オフラインで親と子を作る
- **WHEN** オフラインで親タスクと、そのサブタスクを作ってから同期する
- **THEN** 親が先に作られ、子の `parent` は記録した本物の gid で送られる

### Requirement: 未送信の作成への畳み込み
一度も送っていない (`verify` なし、`sent_at` 空、`attempts` 0、`pending`) 作成があるタスクへの変更は、作成の payload に畳み込み、別の操作を積まない (MUST)。作成が送信中・送信済みで確認中・失敗のときは、別の `update_task` を積む。
根拠: outbox.go:102-114, :888-922; TestOfflineCreateCommentAndComplete, TestEditLocalTaskWhileItsCreateIsSendingOrFailed

#### Scenario: オフラインで作って完了にする
- **WHEN** オフラインでタスクを作り、そのまま完了にする
- **THEN** outbox は `create_task` の 1 件だけで、payload に `completed` が入る

### Requirement: 更新の畳み込みと取り消し
タスクの更新は、`pending` か `conflict` の一番新しい `update_task` に合わせる (MUST)。`sending` の操作には触らず、新しい操作を積む。base は欄ごとに最初の 1 回だけ取る。新しい値が base と同じなら、その欄を外す。欄がなくなった操作は消し、競合する欄がなくなった `conflict` は `pending` に戻す。
根拠: outbox.go:139-162, :925-936; TestLocalUpdateMergesAndReverts, TestEditWhileSendingQueuesNewEntry, TestEditRevertingConflictedFieldClearsConflict

#### Scenario: 名前を変えて元に戻す
- **WHEN** タスク名を変え、送る前に元の名前に戻す
- **THEN** outbox からその操作が消える

#### Scenario: 送信中に編集する
- **WHEN** `update_task` が `sending` の間に同じタスクを編集する
- **THEN** 新しい `update_task` が積まれ、その base は今のローカルの値になる

### Requirement: ほかの操作の畳み込みと打ち消し
移動・セクション名・カスタムフィールド・通知の既読も、元の値に戻したら操作を消す (MUST)。逆向きの未送信の操作 (タグ・フォロワー・プロジェクト・依存の追加と削除) は、積まずに打ち消し合う。ローカル gid のタスクの複製・依存・プロジェクトの変更は断る。
根拠: outbox.go:169-225; sections.go:19-57; customfields.go:673-749; notifications.go:350-361; tags.go:79-104; relations.go:54-66, :182; dependencies.go:147, :184-191; TestMoveTask, TestRenameSection, TestSetCustomFieldQueuesMergesAndReverts

#### Scenario: 元のセクションに戻す
- **WHEN** タスクを別のセクションに移し、送る前に元のセクションに戻す
- **THEN** `move_task` の操作が消える

### Requirement: 更新の検証
`UpdateTaskLocal` は、欄の名前が許可した一覧 (`completed`・`name`・`notes`・`html_notes`・`due_on`・`assignee`・`liked`・`resource_subtype`・`approval_status`) にあり、型と値が正しいときだけ積む (MUST)。名前は空にできない。`approval_status` は、種類が `approval` のタスクにだけ設定でき、断ったときは何も積まない。
根拠: outbox.go:93-101, :705-743; TestApprovalStatusAndType, TestApprovalOnlyForApprovalTasks

#### Scenario: 普通のタスクに承認の状態を付ける
- **WHEN** `default_task` のタスクに `approval_status` を設定する
- **THEN** エラーになり、outbox もキャッシュも変わらない

### Requirement: 送る順番と排他
push は `<db>.push.lock` を取り、アカウントが記録と同じことを確かめてから、中断された操作を戻し、操作を積んだ順 (id の順) に 1 件ずつ送る (MUST)。送る前に操作を読み直す。作成がまだのローカル gid に向けた操作は飛ばして `pending` のまま残す。送り終えたら、その間に積まれた操作をもう 1 回だけ送る。Asana の `/batch` は使わない (通知の既読だけは 50 件まで 1 回で送る)。
根拠: internal/sync/push.go:89-210, :333-349; store/outbox.go:363-383; sync/inbox.go:170-225; TestPushSkipsWhileAnotherProcessPushes, TestPushDoesNotDropWhenAccountChanged, TestPushChildWaitsForFailedParentCreate, TestPushSendsChangesQueuedMeanwhile

#### Scenario: 親の作成が失敗している
- **WHEN** 親タスクの作成が `failed` で、子の作成が積まれている
- **THEN** 子は送らず `pending` のまま残る

### Requirement: 成功と Asana の応答ごとの扱い
成功したら、記録・キャッシュの反映・操作の削除を 1 つのトランザクションで行う (MUST)。404 なら対象を確かめ、タスクが消えていればそのタスクとサブタスクの操作を捨てて記録する (1 回の push で 5 件まで)。ほかの 4xx は `failed` にして続ける。5xx は自動確認中にして同じ push で照合し、合わせて 5 回で `failed` にする。429 と 401 は `pending` に戻して止める。
根拠: push.go:291-434, :451-533; store/outbox_send.go:13-18, :165-173; TestPushDropsChangesToDeletedTask, TestPushLimitsDroppedTasksPerPush, TestPushRejectionsAreNotDropped, TestPushGivesUpAfterMaxSendAttempts, TestPushRateLimitedKeepsQueue

#### Scenario: 対象のタスクが消えていた
- **WHEN** 更新を送って 404 になり、確かめるとタスクが Asana から消えている
- **THEN** そのタスクとサブタスクと、それらの操作を消し、捨てた変更として記録する

### Requirement: 接続のエラーの扱い
接続のエラーでは、届いていないと分かれば (接続できない、DNS、ログインしていない、トークンの更新の失敗) `pending` に戻して試した回数を戻す (MUST)。それ以外は、再送してよい操作なら `pending`、再送してはいけない操作なら自動確認中にする。どちらも push はそこで止める。
根拠: push.go:605-625; TestPushOfflineKeepsQueue, TestPushConnectionDroppedDuringCreate, TestPushOfflineDoesNotCountAttempts

#### Scenario: オフラインで送る
- **WHEN** 接続できない状態で push する
- **THEN** 操作は `pending` のまま残り、`attempts` は増えない (TestPushOfflineDoesNotCountAttempts)

#### Scenario: 作成のあとに接続が切れた
- **WHEN** `create_task` を送ったあと、応答の前に接続が切れる
- **THEN** 再送せず自動確認中にする

### Requirement: 再送してよいかの判断
作成 (`create_task`・`duplicate_task`・`add_comment`・`upload_attachment`・`create_status`) は、届いたか分からないまま再送しない (MUST)。`add_tag` は、タグがローカルでまだ記録がないときだけ再送しない。それ以外は再送してよい。途中で止まった (`sending` のまま残った) 操作は `pending` に戻し、再送してはいけないものは自動確認中にする。
根拠: store/outbox_send.go:97-162; TestPushRecoversInterruptedCreate, TestPushRecoversInterruptedComment, TestPushNewTag5xx

#### Scenario: 作成の途中でプロセスが落ちた
- **WHEN** `create_task` が `sending` のままプロセスが終わり、次の push が始まる
- **THEN** その操作は自動確認中になり、送る前に Asana を見て照合する

### Requirement: 照合は Asana を見て自動で決める
自動確認中の作成は、Asana の時計で「最初に送った時刻 − 2 分」からあとに作られたものを探す (MUST)。タスクは親・プロジェクト・担当者の順に探し、名前・作った人・期日・担当・説明が同じで、ほかの操作がまだ使っていないものを探す。1 件なら記録して再送しない。0 件なら、最後に送ってから 1 分 (複製は 5 分) たつまで待ち、そのあと再送する。2 件以上か、探す場所がないときは `failed` にして利用者に知らせる。
根拠: internal/sync/reconcile.go:57-311; push.go:224-276; TestPushCreate5xxButCreatedIsNotResent, TestPushCreate5xxNotCreatedWaitsThenResendsOnce, TestPushSameNameCreatesAreNotMerged, TestPushCreateAmbiguousAsksUser, TestPushCreate5xxWithPCClockFarAhead

#### Scenario: 5xx だったが作られていた
- **WHEN** `create_task` が 5xx を返したが、Asana には作られている
- **THEN** 見つけたタスクを記録し、もう一度は作らない

#### Scenario: まだ反映されていないかもしれない
- **WHEN** 照合で見つからず、最後に送ってから 1 分たっていない
- **THEN** 「Asana への反映を待っています」として次の同期で確かめ直す

#### Scenario: 照合の途中でオフラインになった
- **WHEN** 照合の問い合わせが接続のエラーになる
- **THEN** 自動確認中のまま残し、次の push で確かめ直す (TestPushCreateVerifyOfflineRetriesOnNextPush)

### Requirement: 利用者による解決
TUI・CLI・MCP の解決は `sync.Resolve` を通す (MUST)。`keep_local` と `keep_remote` は `conflict` だけ、`retry` は `failed` だけ、`discard` はどの状態でもできる。`sending` の操作は断る。捨てた操作はローカルで元に戻し (作成ならローカルのタスクとサブタスクを消す)、取り直す対象のタスクを返す。自動で捨てた変更は、payload ごと `discarded_changes` に記録し、1 回だけ知らせる。
根拠: internal/sync/resolve.go:72-120; store/outbox.go:435-595; store/discard.go:135-154; TestResolveStateRules, TestResolveRefusesSending, TestDiscardOpByKind, TestDroppedChangeKeepsPayload

#### Scenario: 送信中の操作を捨てようとする
- **WHEN** `sending` の操作に `discard` を指示する
- **THEN** `ErrOpSending` で断り、何も変えない

### Requirement: 送った記録は使われている間残す
ローカル gid と本物の gid の対応 (`gid:`・`story:`・`att:`・`status:`・`tag:`) は、outbox のどこか (状態を問わず、payload と base の文字列の中も) か、キャッシュの行がそのローカル gid を使っている間と、使われなくなってから 30 日の間は残す (MUST)。消すかどうかは読み取りのトランザクションで決め、変えるものがあるときだけ書き込みのロックを取る。
根拠: store/results.go:19-298; TestPruneResultsKeepsReferenced, TestPruneResultsAfterGrace, TestPruneResultsWithNothingToChangeTakesNoWriteLock; results_prop_attack_test.go TestPruneResultsNeverDropsAGIDThatSubstringMatches

#### Scenario: payload の中にだけローカル gid がある
- **WHEN** ある操作の payload の `parent` にだけローカル gid が残っている
- **THEN** その gid の対応は消さない

### Requirement: 送り先の制限は変換のときにかける
push は `html_notes` と `html_text` を積んだとおりに送り、タグを絞らない (MUST)。送り先ごとのタグの制限は、Markdown から変換するとき (rich-text の仕様) にかける。カスタムフィールドは、編集できる型 (`text`・`number`・`enum`・`multi_enum`・`date`・`people`) で読み取り専用でないものだけを積み、Asana が 400 を返したら `failed` にする。
根拠: push.go:680-695, :787-789; store/projects.go:151-157; store/customfields.go:44-188, :685-797; TestPushHeadingsRulesImagesOnlyInTaskNotes, TestPushNastyLayoutInputsNeverRejected, TestCustomFieldAPIValue

#### Scenario: コメントを送る
- **WHEN** `Comment` で変換したコメントを送る
- **THEN** 偽の Asana (レイアウトのタグで 400 を返す) が受け付ける

## 未確認

- `safeToResend` を直接確かめる単体テストはない (push のテストを通して確かめている)。
- #54 (DB のエラーを ErrNotFound にしない) を確かめるテストの関数名。
- 本物の Asana での照合 (検索の反映の遅れが 1 分・5 分で足りるか)。
