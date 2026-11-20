# outbox-push (送信待ちの操作・送信・照合)

## Purpose

ローカルの変更を outbox (送信待ちの操作) に積み、Asana に送り (push)、送れたか分からないときに Asana を見て照合する振る舞いの基準。操作の種類、ローカル gid、畳み込みの規則、送る順番、結果ごとの扱い、再送してよいかの判断、照合、利用者による解決を書く。コンテキストは「送信待ち」と「同期」(docs/design/context-map.md)。今の実装は `internal/store` (outbox.go、outbox_send.go、tags.go、relations.go、customfields.go、dependencies.go、sections.go、projects.go、notifications.go、results.go、discard*.go) と `internal/sync` (push.go、batch.go、reconcile.go、resolve.go)。

## Requirements

### Requirement: 操作の種類
outbox の操作の種類は 37 個で、そのうちタスク (とコメント・添付・時間記録) への操作は次の 23 個 (MUST): `update_task`、`create_task`、`add_comment`、`update_story`、`delete_story`、`move_task`、`delete_task`、`set_parent`、`add_follower`、`remove_follower`、`add_project`、`remove_project`、`duplicate_task`、`upload_attachment`、`delete_attachment`、`set_custom_field`、`add_tag`、`remove_tag`、`add_dependency`、`remove_dependency`、`add_time_entry`、`update_time_entry`、`delete_time_entry`。
根拠: internal/domain/outbox/outbox.go (種類の定数。store の Op* はその別名)

#### Scenario: タスクにタグを付ける
- **WHEN** 既にあるタグをタスクに付ける
- **THEN** `add_tag` の操作が積まれ、payload に `tag`・`tag_name`・`workspace` が入る

### Requirement: タスク以外への操作の種類
タスク以外 (マイタスクのセクション・セクション・タグ・状況報告・通知・プロジェクト・カスタムフィールド) への操作の種類は次の 14 個 (MUST): `move_my_section`、`rename_section`、`create_section`、`delete_section`、`insert_section`、`update_tag`、`delete_tag`、`create_status`、`mark_notification`、`update_project`、`update_project_brief`、`create_enum_option`、`add_custom_field_setting`、`remove_custom_field_setting`。
根拠: internal/domain/outbox/outbox.go (種類の定数。store の Op* はその別名)

#### Scenario: 選択肢を足す
- **WHEN** 選択式のフィールドに無い選択肢「低」を足す
- **THEN** `create_enum_option` の操作が積まれ、`task_gid` は選択肢のローカル gid、payload に `field`・`field_name`・`name` が入る

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
`UpdateTaskLocal` は、欄の名前が許可した一覧 (`completed`・`name`・`notes`・`html_notes`・`due_on`・`assignee`・`liked`・`resource_subtype`・`approval_status`・`custom_type`・`custom_type_status_option`) にあり、型と値が正しいときだけ積む (MUST)。名前は空にできない。`approval_status` は、種類が `approval` のタスクにだけ設定でき、断ったときは何も積まない。
根拠: outbox.go:93-101, :705-743; TestApprovalStatusAndType, TestApprovalOnlyForApprovalTasks

#### Scenario: 普通のタスクに承認の状態を付ける
- **WHEN** `default_task` のタスクに `approval_status` を設定する
- **THEN** エラーになり、outbox もキャッシュも変わらない

### Requirement: カスタムの種類の検証
`custom_type` と `custom_type_status_option` (#59) は `resource_subtype` が `custom` のタスクにだけ設定でき、`custom` には種類が要る (MUST)。ステータスはそのタスクの (同じ変更で変える種類があればその) 種類のものだけ。断ったときは何も積まない。
根拠: domain/task/fields.go (CheckCustomType); store/customtypes.go (customTypeChanges); TestCheckCustomType, TestCustomTypeNeedsCustomSubtype

#### Scenario: ほかの種類のステータス
- **WHEN** 種類「バグ」のタスクに、種類「依頼」のステータスを設定する
- **THEN** エラーになり、outbox もキャッシュも変わらない

### Requirement: 送る順番と排他
push は `<db>.push.lock` を取り、アカウントが記録と同じことを確かめてから、中断された操作を戻し、操作を積んだ順 (id の順) に送る (MUST)。送る前に操作を読み直す。作成がまだのローカル gid に向けた操作は飛ばして `pending` のまま残す。送り終えたら、その間に積まれた操作をもう 1 回だけ送る。続く操作は、要件「まとめて送る」のとおり `POST /batch` にまとめることがある。通知の既読は別に、50 件まで 1 回で送る。
根拠: internal/sync/push.go:89-210, :327-349; store/outbox.go:363-383; sync/inbox.go:170-225; TestPushSkipsWhileAnotherProcessPushes, TestPushDoesNotDropWhenAccountChanged, TestPushChildWaitsForFailedParentCreate, TestPushSendsChangesQueuedMeanwhile

#### Scenario: 親の作成が失敗している
- **WHEN** 親タスクの作成が `failed` で、子の作成が積まれている
- **THEN** 子は送らず `pending` のまま残る

### Requirement: まとめて送る
Asana にもうあるタスクだけを変える操作 (更新・カスタムフィールド・セクションへの移動・タグ・フォロワー・プロジェクト・依存の追加と外し) が列で続くときは、10 件まで 1 回の `POST /batch` にまとめて送る (MUST)。1 つのまとまりに同じタスクへの操作を 2 つ入れない。作成・コメント・添付・状況報告・先に照合する操作・ローカル gid に向けた操作は、まとめずに 1 件ずつ送る。各リクエストの答えは、その操作を 1 件で送ったときと同じに扱う。batch 全体が失敗したら、各操作がそれぞれその失敗にあったものとして扱い、Asana が batch を断ったか答えが読めなければ 1 件ずつ送り直す。
根拠: sync/batch.go; sync/push.go (pushBatch); TestPushBatchesUpdatesOfManyTasks, TestPushBatchKeepsOrder, TestPushBatchPerRequestResults, TestPushBatchFails

#### Scenario: 多くのタスクを完了にする
- **WHEN** Asana にある 12 個のタスクを続けて完了にして push する
- **THEN** 書き込みは 10 件と 2 件の `POST /batch` で送られ (送る前の照合の読み取りも batch でまとめる)、12 件とも送れたことになる

### Requirement: 1 回の push の上限
1 回の push で、1 つの操作に使う手順は 4 回まで、自動確認中の操作の照合は 1 回までにする (MUST)。残りは次の push で行う。
根拠: push.go:185-225, :327-328

#### Scenario: 照合のあと再送して、また 5xx
- **WHEN** 照合で見つからずに再送し、その再送も 5xx になる
- **THEN** 同じ push ではもう照合せず、自動確認中のまま次の push に残す

### Requirement: 作成が成功したときの記録
作成 (タスク・複製・コメント・添付・status update・タグ) が成功したら、送った記録・ローカル gid の書き換え・キャッシュの反映・操作の削除を、`Complete*` の 1 つのトランザクションで行う (MUST)。セクション付きのタスクの作成は、同じ行を `pending` の `move_task` に変えて、すぐに送る。送れなければ、そのまま積んでおく。
根拠: store/outbox_send.go:13-18, :165-210; push.go:756-769; TestPushCreateRetriesFailedSectionMove, TestPushFoundCreateMovesToSectionAtOnce

#### Scenario: セクション付きで作る
- **WHEN** セクションを指定したタスクの作成を送り、セクションへの移動が失敗する
- **THEN** タスクは作られ、同じ操作が `move_task` として残る

### Requirement: 作成以外が成功したときの記録
更新・カスタムフィールド・移動・セクション名などが成功したら、先に `CompleteOp` で操作を消す (MUST)。キャッシュへの反映は、そのあとに別に行い、失敗してもエラーにしない (次の同期で直る)。`CompleteOp` が失敗したときは、送ったが記録できなかったものとして照合に回す。
根拠: push.go:56-68, :656-659, :699-702, :804-809, :826-829

#### Scenario: 名前の変更が送れた
- **WHEN** `update_task` が成功する
- **THEN** 操作が消え、そのあと Asana の応答でキャッシュのタスクを書き直す

### Requirement: 競合の判断
`update_task` を送る前に Asana のタスクを取り、payload の欄ごとに、Asana の値が base ともローカルの値とも違えば競合にする (MUST)。`html_notes` は比べない (`notes` で比べる)。取れなかった欄 (Partial) は比べない。比べるときは、両方を termsafe に通す。競合なら送らず `conflict` にする。
根拠: push.go:627-651, :976-978; TestPushConflictThenKeepLocal, TestPushServerAlreadyHasLocalValue, TestPushWithControlCharactersOnServer

#### Scenario: Asana がすでにローカルの値
- **WHEN** Asana のタスク名が、ローカルで変えた名前と同じになっている
- **THEN** 競合にせず送る

### Requirement: 404 の扱い
404 なら、まずアカウントをもう一度確かめ、確かめられないか違えば何も捨てない (MUST)。同じなら対象を確かめ、タスクが消えていれば、そのタスクとサブタスクと、それらの操作を捨てて記録する (1 回の push で 5 件まで)。タスクが残っていれば、その操作だけを捨てる。
根拠: push.go:451-533; TestPushDropsChangesToDeletedTask, TestPushDropsOnlyChangeWhoseTargetIsGone, TestPushDoesNotDropWhenAccountChanged, TestPushLimitsDroppedTasksPerPush

#### Scenario: 対象のタスクが消えていた
- **WHEN** 更新を送って 404 になり、確かめるとタスクが Asana から消えている
- **THEN** そのタスクとサブタスクと、それらの操作を消し、捨てた変更として記録する

#### Scenario: 依存の相手だけが見えない
- **WHEN** `add_dependency` が 404 になり、タスク自体は Asana にある
- **THEN** その操作だけを捨てる

### Requirement: 404 以外の応答の扱い
通知の既読の 403 は、既読と未読の変更をすべて捨て、書き込みが禁じられている印を立てる (MUST)。ほかの 4xx は `failed` にして続ける。5xx は自動確認中にし、合わせて 5 回で `failed` にする。429 と 401 は `pending` に戻して止める。
根拠: push.go:291-434; sync/inbox.go:193-202; TestMarkReadForbiddenDropsWithoutFailures, TestPushRejectionsAreNotDropped, TestPushGivesUpAfterMaxSendAttempts, TestPushRateLimitedKeepsQueue, TestPushUnauthorizedKeepsEverything

#### Scenario: 429 が返る
- **WHEN** 送った操作に Asana が 429 を返す
- **THEN** 操作は `pending` に戻り、push はそこで止まる

### Requirement: 接続のエラーの扱い
接続のエラーでは、届いていないと分かれば (接続できない、DNS、ログインしていない、トークンの更新の失敗) `pending` に戻して試した回数を戻す (MUST)。それ以外は、再送してよい操作なら `pending`、再送してはいけない操作なら自動確認中にする。どちらも push はそこで止める。
根拠: push.go:392-405, :419-425, :605-625; TestPushOfflineKeepsQueue, TestPushConnectionDroppedDuringCreate, TestPushOfflineDoesNotCountAttempts

#### Scenario: オフラインで送る
- **WHEN** 接続できない状態で push する
- **THEN** 操作は `pending` のまま残り、`attempts` は増えない (TestPushOfflineDoesNotCountAttempts)

#### Scenario: 作成のあとに接続が切れた
- **WHEN** `create_task` を送ったあと、応答の前に接続が切れる
- **THEN** 再送せず自動確認中にする

### Requirement: 自分のコメントだけを変える
コメントの編集と削除は、自分 (`created_by` がログイン中の利用者) のコメントだけを受け付ける (MUST, #34)。ほかの人のコメント、活動の記録、同期の前 (自分が分からない) は断り、何も積まない。編集は `update_story` (payload に `story`・`text`・`html_text`、base に元の本文)、削除は `delete_story`、添付の削除は `delete_attachment` を積む。削除の base には消した行の全体を入れ、`task_gid` にはタスクを入れる。同じコメントの未送信の編集は 1 つにまとめ、Asana の本文に戻したら操作を消す。破棄すると、編集は元の本文に、削除は消したものを元に戻す。
根拠: store/comment_edits.go; TestEditCommentLocal, TestEditCommentNeedsMe, TestReplaceStoriesKeepsQueuedEdits, TestDeleteAttachmentLocal

#### Scenario: ほかの人のコメントを直す
- **WHEN** ほかの人が書いたコメントを編集しようとする
- **THEN** 「自分のコメントだけを編集・削除できます」のエラーになり、outbox に何も積まない

### Requirement: 送る前のコメントと添付への変更
一度も送っていないコメント (`add_comment`) の編集は、その payload の書き換えにし、削除は `add_comment` ごと捨てる (MUST, #34)。未送信の添付の削除も `upload_attachment` を捨てる。作成が送信中・確認中・失敗のときは、ローカル gid のまま操作を積み、作成が送れるまで送らずに待つ。送れたら `story:` / `att:` の記録から本物の gid を引く。作成を捨てたら、待っていた操作も捨てる。
根拠: store/comment_edits.go; sync/push.go (waitsForCreate); TestEditUnsentComment, TestEditCommentBeingSent, TestDiscardCommentDropsItsEdits, TestDeleteAttachmentBeingUploaded, TestPushEditWaitsForComment

#### Scenario: 送る前のコメントを直す
- **WHEN** オフラインで書いたコメントを、送る前に書き直す
- **THEN** outbox は `add_comment` の 1 件のままで、payload の本文が新しい本文になる

### Requirement: 消した添付のファイルは残さない
添付を削除したら、ダウンロード済みのファイルと、まだ送っていない添付のコピーを、送る前でもすぐに消す (MUST, #34)。利用者が選んだ元のファイルには触らない。送信中・確認中の添付のコピーだけは、送り直しに要るので残し、削除を送れたあとに消す。削除を破棄して添付を元に戻したときは、ファイルなしで戻し、開くときにもう一度ダウンロードする。
根拠: store/comment_edits.go (DeleteAttachmentLocal); sync/push.go (pushDeleteItem); TestDeleteAttachmentLocal, TestDeleteAttachmentBeingUploaded, TestPushDeleteRemovesUploadCopy

#### Scenario: ダウンロードした添付を消す
- **WHEN** 開いたことのある (ダウンロード済みの) 添付を、オフラインで削除する
- **THEN** そのファイルはすぐに消え、outbox に `delete_attachment` が積まれる

### Requirement: コメントと添付の変更の送り方
編集 (PUT /stories) と削除 (DELETE) は何度送っても同じなので、届いたか分からなければ照合せずに再送する (MUST, #34)。削除の 404 は「もう消えている」ので、済んだものとして消し、捨てた変更としては記録しない。編集の 404 は、ほかの操作と同じく捨てて記録する。
根拠: sync/push.go (pushUpdateStory, pushDeleteItem); TestPushEditAndDeleteComment, TestPushDeleteGoneIsDone, TestStoryEditsSafeToResend

#### Scenario: もう消えているコメントを削除する
- **WHEN** `delete_story` を送って 404 が返る
- **THEN** 操作は済んだものとして消え、失敗にも捨てた変更にもならない

### Requirement: 再送してよいかの判断
作成 (`create_task`・`duplicate_task`・`add_comment`・`upload_attachment`・`create_status`) は、届いたか分からないまま再送しない (MUST)。`add_tag` は、タグがローカルでまだ記録がないときだけ再送しない。それ以外は再送してよい。途中で止まった (`sending` のまま残った) 操作は `pending` に戻し、再送してはいけないものは自動確認中にする。
根拠: store/outbox_send.go:97-162; TestPushRecoversInterruptedCreate, TestPushRecoversInterruptedComment, TestPushNewTag5xx

#### Scenario: 作成の途中でプロセスが落ちた
- **WHEN** `create_task` が `sending` のままプロセスが終わり、次の push が始まる
- **THEN** その操作は自動確認中になり、送る前に Asana を見て照合する

### Requirement: 照合は Asana を見て自動で決める
自動確認中の作成は、Asana の時計で「最初に送った時刻 − 2 分」からあとのものを探す (MUST)。見つかったものが 1 件なら記録して再送しない。0 件なら、最後に送ってから 1 分 (複製は 5 分) たつまで待ち、そのあと再送する。2 件以上か、探す場所がないときは `failed` にして利用者に知らせる。何を同じとみなすかは、次の要件のとおり操作の種類ごとに違う。
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

### Requirement: 照合で同じとみなす条件
`create_task` は、親・プロジェクト・担当者の順に探す場所を決め、名前・作った人 (分からなければ問わない)・期日・担当・説明が同じで、ほかの操作がまだ使っていないものを同じとみなす (MUST)。`duplicate_task` は、元のタスクのある場所で、同じ名前で元のタスクでないもの。コメントは、自分のコメントで、文字と数字が同じもの (絵文字だけなら完全に同じもの)。`add_tag` は、ワークスペースに同じ名前のタグがあれば、誰が作ったかを問わずそれを使う。
根拠: reconcile.go:91-264; TestPushSameNameCreatesAreNotMerged, TestPushCreateIgnoresSameNameWithOtherFields, TestPushRichComment5xxButCreated, TestPushEmojiCommentMatchesExactly, TestPushNewTag5xx

#### Scenario: 同じ名前で期日の違うタスク
- **WHEN** 照合で、同じ名前だが期日の違うタスクが見つかる
- **THEN** 同じとみなさない

### Requirement: 利用者による解決
TUI・CLI・MCP の解決は `sync.Resolve` を通す (MUST)。`keep_local` と `keep_remote` は `conflict` だけ、`retry` は `failed` だけ、`discard` はどの状態でもできる。`sending` の操作は断る。`retry` は試した回数を 0 に戻し、Asana を確かめずに `pending` にする。捨てた操作はローカルで元に戻し (作成ならローカルのタスクとサブタスクを消す)、取り直す対象のタスクを返す。
根拠: internal/sync/resolve.go:72-120; store/outbox.go:435-595; TestResolveStateRules, TestResolveRefusesSending, TestRetryResetsAttempts, TestDiscardOpByKind

#### Scenario: 送信中の操作を捨てようとする
- **WHEN** `sending` の操作に `discard` を指示する
- **THEN** `ErrOpSending` で断り、何も変えない

### Requirement: 自動で捨てた変更を残す
自動で捨てた変更は、要約・理由・payload ごと `discarded_changes` に記録し、利用者に 1 回だけ知らせる (MUST)。一覧では、知らせたかどうかを問わず新しい順に出す。
根拠: store/discard.go:135-154; store/discard_list.go:12; TestDroppedChangeKeepsPayload, TestRecentDiscards

#### Scenario: 消えたタスクへの変更を捨てた
- **WHEN** push が、Asana で消えたタスクへの変更を捨てる
- **THEN** 次に入り口が結果を出すとき、捨てたことを 1 回だけ知らせる

### Requirement: 送った記録は使われている間残す
ローカル gid と本物の gid の対応 (`gid:`・`story:`・`att:`・`status:`・`tag:`) は、outbox のどこか (状態を問わず、payload と base の文字列の中も) か、キャッシュの行がそのローカル gid を使っている間は残す (MUST)。その接頭辞を使う種類の作成が outbox にある間も残す。要らないと最初に分かったときに印を付け、そこから 30 日たったら消す。消すかどうかは読み取りのトランザクションで決め、変えるものがあるときだけ書き込みのロックを取る。
根拠: store/results.go:19-298; TestPruneResultsKeepsReferenced, TestPruneResultsKeepsWhileSameKindQueued, TestPruneResultsAfterGrace, TestPruneResultsWithNothingToChangeTakesNoWriteLock

#### Scenario: payload の中にだけローカル gid がある
- **WHEN** ある操作の payload の `parent` にだけローカル gid が残っている
- **THEN** その gid の対応は消さない

### Requirement: 送り先の制限は変換のときにかける
送り先ごとのタグの制限は、Markdown から変換するとき (rich-text の仕様) にかける。push は `html_notes` を積んだとおりに送り、コメントと status update の `html_text` も、送り先が受け付けないタグがなければ積んだとおりに送る (MUST)。カスタムフィールドは、編集できる型 (`text`・`number`・`enum`・`multi_enum`・`date`・`people`) で読み取り専用でないものだけを積み、Asana が 400 を返したら `failed` にする。
根拠: push.go:680-695, :787-789, :957-971; sync/projects.go:151-158; store/customfields.go:44-188, :685-797; TestPushHeadingsRulesImagesOnlyInTaskNotes, TestPushNastyLayoutInputsNeverRejected, TestPushCustomFieldErrors

#### Scenario: コメントを送る
- **WHEN** `Comment` で変換したコメントを送る
- **THEN** 偽の Asana (レイアウトのタグで 400 を返す) が受け付ける

### Requirement: 古い形のコメントと status update は送る前に直す
コメントと status update の `html_text` に、送り先が受け付けないタグ (見出し・区切り線・画像・表) があれば、push は送る前に今の変換の決まりで直して積み直す (MUST。#63 より前に積まれたもの、#71)。エディタ用の Markdown に戻してから送り先の変換をかけ、`text` も合わせる。直したものが `failed` なら、利用者に聞かずに `pending` に戻し、試した回数と照合の印を消す (何で失敗したものでも、そのタグがある限り Asana は作らないので、送り直しても二重にならない)。送信中のものは触らない。直したあとはタグが残らないので、ほかの理由で失敗したものは 1 回しか戻らない。
根拠: sync/push.go (pushPass), store/outbox_richtext.go, richtext/target.go (`Fit`); TestPushFitsRichTextQueuedBefore63, TestFitQueuedRichText, TestFitLayoutTags

#### Scenario: #63 より前に積まれたコメントが失敗のまま残っている
- **WHEN** 見出しや `<hr/>` を含む `html_text` のコメントが、400 で `failed` になっている
- **THEN** 次の push で、見出しは太字、区切り線は書いた行の文字に直して送り、偽の Asana (レイアウトのタグで 400 を返す) が受け付ける

### Requirement: タスクの削除
タスクの削除は、一度も送っていないタスクならその場で作成ごと (サブタスクと積んだ変更も) 消し、送ったことのあるタスクなら `delete_task` を積む (MUST)。送るまでは、そのタスクとサブタスクを一覧とサブタスクの一覧から隠し、取り消せば元に戻す。同じタスクの削除を 2 回積まない。
根拠: store/taskops.go (DeleteTaskLocal, deletedTasks); store/read.go (ListTasks, GetTask); TestDeleteTaskLocalHidesUntilSent, TestDeleteLocalTaskIsImmediate

#### Scenario: 削除を積んで取り消す
- **WHEN** 送ったことのあるタスクを削除し、送る前に outbox の `delete_task` を取り消す
- **THEN** 削除を積んでいるあいだはタスクとサブタスクが一覧に出ず、取り消すと、サブタスクに積んであった変更ごと元に戻る

### Requirement: タスクの削除の送信
`delete_task` が送れたら、タスク・サブタスク・それらに積んであった変更をキャッシュから消す (MUST)。`DELETE /tasks/{gid}` が 404 なら、もう消えているとして済んだ扱いにし、捨てた変更として記録しない。
根拠: store/taskops.go (CompleteDelete); sync/push.go (pushDelete); TestCompleteDeleteRemovesTheTree, TestPushDeleteTask, TestPushDeleteTaskAfterA5xx

#### Scenario: 消えていたタスクの削除
- **WHEN** `delete_task` を送ったら Asana が 404 を返す
- **THEN** 操作は済んだ扱いで消え、タスクはキャッシュから消え、捨てた変更の記録は増えない

### Requirement: 親タスクの付け替え
親タスクの付け替えは `set_parent` を積む (MUST)。payload に `parent` (空なら親から外す)・`parent_name`、任意で `insert_before` / `insert_after` (新しい親のほかのサブタスク) を、base に元の親を持つ。同じタスクの続けての付け替えは 1 つにまとめ、元の親に位置なしで戻したら操作を消す。自分自身と自分のサブタスクの下には移さない。送るまでは、取り込みで親が上書きされても積んだ親に戻す。取り消すと元の親に戻す。
根拠: store/taskops.go (SetParentLocal, reapplySetParent); store/outbox.go (discardOpTx); sync/push.go (pushSetParent); TestSetParentLocal, TestSetParentLocalRefuses, TestPushSetParent

#### Scenario: 親を変えて取り込む
- **WHEN** サブタスクを別の親に移し、送る前に古い親のままの内容を取り込む
- **THEN** キャッシュでは新しい親の下のままで、送ると `setParent` に新しい親が入る

### Requirement: オフラインで作った親への付け替え
新しい親がオフラインで作ったタスクなら、`set_parent` はその作成が送れるまで待つ (MUST)。作成が失敗しても待ち続け、捨てない。その作成を取り消したとき、または Asana で消えていたとして捨てたときは、そのタスクを親にする `set_parent` も取り消し、子を元の親に戻す。
根拠: sync/push.go (waitsForCreate); store/taskops.go (undoSetParentsTo); store/outbox.go (discardLocalTask); store/discard.go (DiscardDeletedTask); TestPushSetParentWaitsForTheParentsCreate, TestParentCreatedOfflineGoesAway

#### Scenario: 親の作成が失敗する
- **WHEN** オフラインで作った親の下へ既存のタスクを移し、親の作成が 400 で失敗する
- **THEN** `set_parent` は送られず `pending` のまま残り、捨てた変更の記録も増えない

### Requirement: セクションの中の位置
`move_task` は任意で `insert_before` / `insert_after` (移す先のセクションにあるほかのタスク) を持ち、`addTask` にそのまま渡す (MUST)。位置があれば、今と同じセクションへの移動でも積む。位置付きの移動は、それまでの同じタスクの移動をまとめたうえで列の末尾に積み直し、batch に入れず 1 件ずつ送る。ローカルの gid の目印は送るときに本物の gid に引き、引けなければ位置を付けずに送る。
根拠: store/outbox.go (MoveTaskAtLocal); store/taskops.go (requeue); sync/push.go (pushMove, position); sync/batch.go; TestMoveTaskAtLocal, TestMoveToAPlaceGoesToTheEndOfTheQueue, TestPushMoveToAPlace

#### Scenario: 同じセクションで並べ替える
- **WHEN** タスクを、同じセクションのタスクの後ろに置く
- **THEN** `move_task` が積まれ、送ると `addTask` に `insert_after` が入る

### Requirement: 削除予定のタスクとセクションの削除
セクションの削除で「タスクが残っている」と数えるとき、削除を積んだタスクは数えない (MUST)。そのセクションの `delete_section` は、中のタスクの `delete_task` がすべて outbox から消えるまで送らずに待つ (Asana は空のセクションしか消さないため)。削除を積んだタスクは、マイタスクのセクションへも移さない。
根拠: store/section_ops.go (DeleteSectionLocal); store/taskops.go (TaskDeletesQueuedIn); store/my_sections.go (MoveMySectionLocal); sync/push.go (waitsForCreate); TestDeleteSectionAfterItsTasksDeletes, TestMoveMySectionRefusesATaskBeingDeleted

#### Scenario: タスクを消してからセクションを消す
- **WHEN** セクションの唯一のタスクの削除を積み、続けてそのセクションを削除する
- **THEN** 両方積め、送るときはタスクの削除が先に送られ、それが失敗しているあいだはセクションの削除を送らない

### Requirement: オフラインで作ったセクション
セクションの作成 (`create_section`) はローカル gid を付けて積み、タスクをすぐにそこへ移せる (MUST)。ローカル gid のセクションを名指しする操作 (`move_task`、`insert_section`、`rename_section`、`delete_section`、作成の位置) は、その作成が outbox にある間は送らずに待つ。作成が送れたら、キャッシュのセクションとタスクの所属、outbox の `task_gid` と payload・base の中のローカル gid を本物の gid に書き換え、`sync_state` に `section:<local>` → 本物 を記録する。
根拠: store/section_ops.go; sync/push_sections.go; TestPushCreateSectionThenMoveIntoIt, TestLocalSectionInTaskCreate

#### Scenario: オフラインで作ったセクションへタスクを移す
- **WHEN** オフラインでセクション「レビュー」を作り、タスクをそこへ移してから同期する
- **THEN** 先にセクションが作られ、移動は本物の gid で送られる。キャッシュのタスクも本物の gid のセクションに入る

### Requirement: 作ったセクションの取り消し
送っていないセクションの作成を取り消す (または消す) と、そこへの移動・並べ替えは元に戻し、そこへ作ったタスク (`create_task`・`add_project`) はセクションを外して残す (MUST)。一度も送っていなければ、削除は何も積まない。
根拠: store/section_ops.go (forgetLocalSection); TestDiscardCreateSectionUndoesMoves, TestLocalSectionInTaskCreate

#### Scenario: 作成を取り消す
- **WHEN** オフラインで作ったセクションへタスクを移し、作成を取り消す
- **THEN** タスクは元のセクションに戻り、outbox は空になる

### Requirement: セクションの作成の照合
セクションの作成は二度送らない (MUST)。送る前に、プロジェクトの中で同じ名前のセクションの gid を payload の `existing` に記録する。答えが届かなかったら、同じ名前で `existing` になく、ほかの作成の結果でもないセクションを Asana で探し、1 つなら作成済みとする。
根拠: sync/push_sections.go (pushCreateSection, findCreatedSection); TestPushCreateSectionAnswerLost

#### Scenario: 作成の答えが届かない
- **WHEN** 既に「Done」があるプロジェクトに「Done」を作り、Asana は作ったが 502 を返す
- **THEN** 次の確認で新しい「Done」を作成の結果として記録し、もう一度は作らない

### Requirement: セクションとタグの変更の再適用
送っていないセクションとタグの変更は、同期で Asana の値を入れても保つ (MUST)。作ったセクションは残し、消したセクションは消えたまま、並べ替えは積んだ順にやり直す。タグの新しい名前・色は保ち、消したタグはタスクにも戻さない。
根拠: store/section_ops.go (reapplySectionOps); store/tag_ops.go (reapplyTagEdits); TestPushDeleteAndReorderSections, TestPushUpdateAndDeleteTag

#### Scenario: 並べ替えのあとに同期する
- **WHEN** セクションを並べ替え、送る前に同期で Asana の並びを入れる
- **THEN** キャッシュの並びは並べ替えたあとのまま

### Requirement: 消えたものの削除
Asana で既に消えているセクションやタグの削除は、失敗にせず完了とする (MUST)。空でないセクションの削除を Asana が拒んだら失敗にし、セクションは次の同期で戻る。
根拠: sync/push_sections.go (pushDeleteSection, pushDeleteTag); TestPushDeleteAndReorderSections

#### Scenario: Asana で先に消されたセクション
- **WHEN** 削除を積んだセクションが、送る前に Asana で消される
- **THEN** 削除は完了として outbox から消える

### Requirement: プランにない機能
操作を送って Asana が 402 を返したら、その機能 (セクションの作成・削除・並べ替え、タグの変更・削除) を、操作のワークスペース (セクションはプロジェクトの、タグは付いているタスクの、分からなければ既定のもの) について `plan_unavailable:<機能>:<ワークスペースの gid>` に時刻で記録する (MUST)。24 時間は、そのワークスペースでの操作をプランで使えない旨のエラーにする。すべてのワークスペースに記録があるあいだは、TUI のメニューと MCP のツールにも出さない。
根拠: sync/push.go (send); app/plan.go; TestPushSectionsAndTags402PerWorkspace, TestSectionsOldGlobalPlanRecordIgnored, TestTUIHidesUnavailableFeatures

#### Scenario: 並べ替えに 402 が返る
- **WHEN** セクションの並べ替えを送ると Asana が 402 を返す
- **THEN** その操作は失敗になり、そのワークスペースでは 24 時間セクションの作成・削除・並べ替えを受け付けない。タグの変更は出したまま

#### Scenario: 24 時間たったら試し直す
- **WHEN** 402 の記録から 24 時間たって、同じワークスペースでタグを変え、Asana が受け付ける
- **THEN** 変更は積まれて送られ、記録は消える

#### Scenario: #50 より前の記録
- **WHEN** ワークスペースのない古い記録 `plan_unavailable:sections` だけがある
- **THEN** 読まずに、セクションの作成・削除・並べ替えを出す

#### Scenario: 無料のワークスペースで 402 が返る
- **WHEN** 無料のワークスペースでセクションの並べ替えに 402 が返り、有料の組織のプロジェクトでセクションを作る
- **THEN** 有料の組織では作れる。メニューにも出したまま

### Requirement: マイタスクのセクションへの移動
自分が担当のタスクは、マイタスクのセクションの間を `move_my_section` で移せる (MUST)。payload は `section`・`section_name`・`workspace`、base は元のセクション。続けて移すと 1 つにまとめ、元に戻すと消す。送るときは `POST /sections/{gid}/addTask` を使う (繰り返しても害がない)。同期で Asana の `assignee_section` を入れても、未送信の移動を保つ。取り消すと元のセクションに戻す。ほかの人が担当のタスクは移せない。
根拠: store/my_sections.go; sync/my_sections.go; TestMoveMySectionLocal, TestSyncAndMoveMySections

#### Scenario: 今日やるへ移す
- **WHEN** あとでにある自分のタスクを今日やるへ移し、送る前に同期する
- **THEN** キャッシュでは今日やるのままで、次の送信で今日やるのセクションへ addTask が送られる

### Requirement: プロジェクトの編集は update_project に積む
プロジェクトの説明 (`notes`・`html_notes`)・オーナー (`owner`、`""` で外す)・開始日 (`start_on`)・期日 (`due_on`)・色 (`color`) の変更は、ローカルに当てて `update_project` に積む (MUST)。`task_gid` はプロジェクトの gid、`base` は変える前の値。同じプロジェクトの編集は 1 つにまとめ、全部を元に戻したら消す。捨てたら `base` に戻す。
根拠: internal/store/project_fields.go; TestUpdateProjectLocal, TestDiscardProjectEdit

#### Scenario: 元に戻す
- **WHEN** 色を変えたあと、元の色に戻す
- **THEN** `update_project` は残らない

### Requirement: プロジェクトの編集の検査
色は Asana の色の名前か `none`、日付は `YYYY-MM-DD` でなければ拒む (MUST)。開始日には期日が要り、期日より後にできない。拒んだときは何も当てず、何も積まない。
根拠: internal/domain/project; TestUpdateProjectLocalRejects, TestValidateFields, TestCheckDates

#### Scenario: 期日のないプロジェクトに開始日
- **WHEN** 期日のないプロジェクトに開始日だけを付ける
- **THEN** 「開始日を付けるには期日も必要です」で拒み、outbox は変わらない

### Requirement: 取り込みで未送信のプロジェクトの編集を残す
プロジェクトを取り込むときは、Asana の値で書いたあと、同じトランザクションで、`failed` でない `update_project` を当て直す (MUST)。
根拠: internal/store/write.go (UpsertProjects); TestUnsentProjectEditSurvivesSync, TestFetchProjectOverviewKeepsUnsentEdit

#### Scenario: 期日を変えて未送信のまま取り込む
- **WHEN** プロジェクトの期日をローカルで変えて未送信のまま、同期でプロジェクトを取り込む
- **THEN** 期日はローカルの値のまま、ほかの欄は Asana の値になる

### Requirement: update_project の送り方
`update_project` は、Asana のプロジェクトを取り、変えた欄 (`html_notes` を除く) のどれかが `base` ともこちらの値とも違えば競合にする (MUST)。そうでなければ `PUT /projects/{gid}` を送る。`html_notes` があれば `notes` は送らない。`""` のオーナーと日付は `null` で送る。開始日を送るときに期日を送らないなら、Asana の今の期日を添える。プロジェクトが消えていれば (404)、変更を捨てて記録する。
根拠: internal/sync/projects.go (pushUpdateProject); TestPushUpdateProject, TestPushUpdateProjectConflict, TestPushUpdateProjectGone

#### Scenario: 両方で期日を変えた
- **WHEN** ローカルで期日を変えたあと、Asana でも別の期日に変わっている
- **THEN** 送らずに競合にし、`conflict` に Asana の期日が入る

### Requirement: 作業時間の記録の送信
作業時間の記録の作成 (`add_time_entry`) は `POST /tasks/{gid}/time_tracking_entries` に時間 (`duration_minutes`) と日 (`entered_on`) だけを送る (MUST)。直す (`update_time_entry`) は `PUT /time_tracking_entries/{gid}`、消す (`delete_time_entry`) は `DELETE` で送り、消すときの 404 は完了とする。作成が送れたら、ローカルの記録を Asana の gid に置き換え、`sync_state` に `time:<local>` → 本物 を記録する。
根拠: sync/timetracking.go; store/time_entries.go; TestPushTimeEntries, TestPushTimeEntryDeleteGone

#### Scenario: 記録を作って直す
- **WHEN** 90 分の記録を作り、Asana から読んだ別の記録を 20 分に直して同期する
- **THEN** POST は `{"duration_minutes":90,"entered_on":...}` で 1 回、PUT はその別の記録の gid に送られ、outbox は空になる

### Requirement: 作業時間の記録の作成の照合
記録の作成は二度送らない (MUST)。答えが届かなかったら、タスクの記録の一覧から、同じ時間と日で自分が作り、ほかの作成の結果でないものを選び、1 件ずつ読んで送ったあとに作られたものを探す。1 件なら作成済みとし、2 件以上なら利用者に任せる (失敗にする)。
根拠: sync/timetracking.go (findTimeEntry); domain/outbox/resend.go; TestPushTimeEntryLostAnswerIsFound, TestPushTimeEntryLostAnswerAmbiguous

#### Scenario: 作成の答えが届かない
- **WHEN** 同じ時間と日の記録が 1 時間前に自分のもの、いま他人のものとしてあり、作成は Asana に届いたが 500 が返る
- **THEN** 次の確認でいま作られた自分の記録を結果として記録し、POST は 1 回だけ

### Requirement: 作られていない記録の変更
ローカル gid の記録を直す・消すとき、作成を一度も送っていなければ作成を書き換える・取り消す (MUST)。送っている途中や確認中なら、変更はローカル gid のまま積み、作成が済んでから本物の gid に送る。
根拠: store/time_entries.go; sync/push.go (storyTarget, waitsForCreate); TestTimeEntryNotSentYet, TestTimeEntryChangedWhileSending, TestPushTimeEntryEditsWaitForAdd

#### Scenario: 送る前に直して消す
- **WHEN** 記録を作り、送る前に時間を直し、そのあと消す
- **THEN** 作成の payload が新しい時間に変わり、消すと outbox は空になる

### Requirement: タイムトラッキングがプランにないとき
記録の送信や一覧の取得に Asana が 402 を返したら、タスクのワークスペースごとに `plan_unavailable:time_tracking:<ワークスペース>` に時刻を記録する (MUST)。24 時間はそのワークスペースの一覧を問い合わせず、書き込みはエラーにする。全ワークスペースに記録があれば TUI のメニューと MCP のツールに出さない。24 時間たてば試し直し、読めたら記録を消す。
根拠: sync/plan.go; readmodel/plan.go; app/timetracking.go; TestTimeTrackingPlanPerWorkspaceAndRechecked, TestTimeTrackingPlanUnavailable, TestTimeEntriesPaymentRequiredInTUI

#### Scenario: 記録の作成に 402 が返る
- **WHEN** 記録を作って送ると Asana が 402 を返す
- **THEN** その操作は失敗になり、24 時間はそのワークスペースの一覧も Asana に問い合わせず、プランで使えないと出す

#### Scenario: 無料と有料のワークスペース
- **WHEN** 無料のワークスペースのタスクで 402 が返ったあと、有料のワークスペースのタスクの記録を開く
- **THEN** 有料のほうは Asana から読み、メニューにも出したまま

### Requirement: オフラインで足した選択肢
選択肢の作成 (`create_enum_option`) はローカル gid を付けて積み、タスクの値にすぐ使える (MUST)。ローカル gid の選択肢を使う値の変更 (`set_custom_field`、値付きの `create_task`) は、その作成が outbox にある間は (失敗していても) 送らずに待ち、`POST /batch` にもまとめない。
根拠: store/field_ops.go (AddEnumOptionLocal); sync/push_fields.go (waitsForField); sync/batch.go; TestPushNewOptionForbidden

#### Scenario: 作成を断られた選択肢を使う値
- **WHEN** 足した選択肢を値にしたタスクと、既存の選択肢を値にしたタスクがあり、選択肢の作成が断られる
- **THEN** 既存の選択肢の値だけが送られ、ローカル gid は Asana に送られない

### Requirement: 足した選択肢の gid の置き換え
選択肢の作成が送れたら、フィールドの選択肢、タスクの値、outbox の payload・base・conflict の中のローカル gid を本物に書き換え (multi_enum は並べ直す)、`sync_state` に `enum_option:<local>` → 本物 を記録する (MUST)。同期でフィールドの選択肢を入れ直しても、作成が outbox にある選択肢は残す。
根拠: store/field_ops.go (replaceLocalOption, localOptions); TestPushNewOptionThenValue, TestPushNewOptionMultiEnum, TestCompleteCreateEnumOption, TestPullKeepsLocalOption

#### Scenario: 足した選択肢を値にして同期する
- **WHEN** オフラインで「優先度」に「低」を足してタスクの値にし、同期する
- **THEN** 先に選択肢が作られ、値は本物の gid で送られる。キャッシュの値と選択肢も本物の gid になる

### Requirement: 選択肢の照合
選択肢は二度作らない (MUST)。送る前と、答えが届かなかったときの確認で、Asana のフィールドを読み、同じ名前の有効な選択肢 (ほかの作成の結果として記録されていないもの) があれば、それを作成の結果として記録して作らない。
根拠: sync/push_fields.go (pushCreateEnumOption, findEnumOption); TestPushNewOptionTakesExisting, TestPushNewOptionLostAnswer

#### Scenario: 作成の答えが届かない
- **WHEN** 選択肢の作成に Asana が 500 を返したが、選択肢は作られていた
- **THEN** 次の送信でその選択肢を結果として記録し、もう一度は作らない

### Requirement: 足した選択肢の取り消し
送っていない選択肢の作成を取り消すと、フィールドの選択肢から消す (MUST)。その選択肢だけを設定した変更は元の値に戻して消し、multi_enum のほかの選択肢はそのまま送る。オフラインで作ったタスクは、その値なしで作る。
根拠: store/field_ops.go (undoCreateEnumOption); TestDiscardCreateEnumOption, TestPushNewOptionForbidden

#### Scenario: 作成を取り消す
- **WHEN** 足した選択肢をタスクに設定し、作成を取り消す
- **THEN** タスクの値は元に戻り、outbox は空になる

### Requirement: フィールドの付け外し
既存のフィールドをプロジェクトに付ける (`add_custom_field_setting`) と、すぐにプロジェクトのタスクで編集でき、その値の変更は付ける操作が outbox にある間は待つ (MUST)。送れていない逆の操作があるときは、積まずにそれを消す。外す (`remove_custom_field_setting`) のを取り消すと、元の位置と「重要」に戻す。同期でプロジェクトのフィールドを入れ直しても、送っていない付け外しは保つ。
根拠: store/field_ops.go (SetProjectFieldLocal, reapplyFieldSettingOps, undoFieldSettingOp); TestSetProjectFieldLocal, TestValueWaitsForFailedFieldSetting, TestPushRemoveFieldSetting

#### Scenario: 付けたフィールドに値を入れて同期する
- **WHEN** オフラインでフィールド「メモ」をプロジェクトに付け、タスクの「メモ」を設定して同期する
- **THEN** 先にフィールドが付けられ、そのあとで値が送られる

### Requirement: フィールドの付け外しの送信
フィールドの付け外しや選択肢の作成に Asana が 4xx (401・402・403・404・429 を除く。例: "Custom field already exists in parent object."、"An enum option already exists with the name") を返しても、Asana を読んで既にその状態 (選択肢は同じ名前の有効なもの) なら完了とする (MUST)。送れたらプロジェクトのフィールドを取り直す。
根拠: sync/push_fields.go (pushFieldSetting, pushCreateEnumOption, isRefusedAsIs); TestPushAddFieldSettingThenValue, TestPushAddFieldSettingAlreadyThere, TestPushNewOptionDuplicateMeanwhile

#### Scenario: ほかの人が先に付けていた
- **WHEN** 付けようとしたフィールドを、送る前にほかの人がプロジェクトに付けていた
- **THEN** 400 が返っても完了になり、失敗にしない

### Requirement: カスタムフィールドの変更をプランで断られたとき
選択肢の作成やフィールドの付け外しに Asana が 402 を返したら、そのワークスペースについて `plan_unavailable:custom_fields:<workspace>` に時刻を記録し、24 時間はどちらの機能もそのワークスペースで TUI・CLI・MCP に出さない (MUST)。24 時間たてば試し直し、書き込みが通れば印を消す。ほかのワークスペースでは隠さない。操作は理由付きで失敗にする。
根拠: sync/plan.go (recordPlanRefusal, clearPlanRefusal); sync/push.go (send); readmodel/plan.go (PlanKey, PlanOffFor); app/plan.go (planOffered); TestPushNewOptionPaymentRequired, TestNewOptionNotOfferedWhenRefused

#### Scenario: 無料のワークスペースで 402
- **WHEN** あるワークスペースで選択肢の作成に Asana が 402 を返す
- **THEN** そのワークスペースでは 24 時間「選択肢を足して設定する」を出さず、ほかのワークスペースでは出す

### Requirement: カスタムフィールドの変更を権限で断られたとき
選択肢の作成に Asana が 403 を返したら、そのフィールドについて `field_options_locked:<field>` を記録し、そのフィールドでは選択肢の追加を出さない (MUST)。フィールドの付け外しの 403 なら、そのプロジェクトについて `project_fields_locked:<project>` を記録し、そこでは出さない。操作は理由付きで失敗にする。
根拠: sync/push_fields.go (fieldForbidden); app/fields.go; TestPushNewOptionForbidden, TestValueWaitsForFailedFieldSetting, TestProjectFieldsLocked

#### Scenario: 選択肢の追加を 403 で断られる
- **WHEN** 選択肢の作成に Asana が 403 を返す
- **THEN** 操作は失敗になり、そのフィールドでは「選択肢を足して設定する」を出さない。ほかのフィールドでは出す

### Requirement: カスタムフィールドの値の書き込み形式
`set_custom_field` の値は、Asana が受け付ける形で送る (MUST): date は `{"date": "YYYY-MM-DD"}` (文字列は "date_value: DayAndDateTime is not a JSON object" で断られる)、people と multi_enum は gid の配列で、空にするときは `[]` (null は "people_value: Not an array"・"multi_enum_values: Not an array" で断られる)、number は数値、そのほかを空にするときは null。本物の Asana で確かめた (Issue #1)。
根拠: readmodel/fieldvalues.go (CustomFieldAPIValue); TestMovedValuesMatchOldStore, TestPushClearMultiEnumSendsEmptyList

#### Scenario: 担当者のフィールドを空にする
- **WHEN** people のフィールドを空にして同期する
- **THEN** `{"custom_fields": {"<field>": []}}` が送られる

### Requirement: カスタムタイプの変更
種類 (`custom_type`) だけを変えたときは、その種類の最初の選べるステータス (完了でないものを先に) も積む (MUST)。種類はタスクのプロジェクトが出すものだけ、ステータスは選べるものだけ (今のステータスと Asana 側のステータスは通す) を受け付ける。
根拠: store/customtypes_write.go (customTypeChanges, defaultCustomStatus, offersCustomType); TestUpdateCustomTypeLocal, TestAttackDisabledStatusIsRefused, TestAttackUnknownTypeIsRefused

#### Scenario: 種類だけを変える
- **WHEN** 普通のタスクを種類「バグ」(ステータス: 完了・対応中) にする
- **THEN** payload に `resource_subtype: custom`・`custom_type`・`custom_type_status_option` (対応中) が入る

### Requirement: ステータスと完了
完了のステータス (`completion_state` が Complete) にするとローカルでも完了にし、未完了のステータスなら未完了にする (MUST)。
根拠: store/outbox.go (applyFields); TestUpdateCustomTypeLocal

#### Scenario: 完了のステータス
- **WHEN** 種類「バグ」のタスクのステータスを「完了」にする
- **THEN** タスクはローカルで完了になる

### Requirement: カスタムタイプをやめる
ほかの種類 (`resource_subtype`) にすると、ローカルの種類とステータスを外し、payload から `custom_type`・`custom_type_status_option` を除く (MUST)。base には種類・カスタムタイプ・ステータス・完了を残し、取り消すと元に戻る。
根拠: store/outbox.go (applyFields); domain/outbox/fold.go; TestDiscardRestoresCustomType, TestFoldCustomType

#### Scenario: マイルストーンにして取り消す
- **WHEN** 種類「バグ」のタスクをマイルストーンにし、その変更を取り消す
- **THEN** 種類「バグ」と元のステータス・完了に戻る

### Requirement: オフラインで作ったタスクの種類
オフラインで作ったタスクの種類は作成に混ぜず、その後のタスクの変更もまとめて `update_task` として作成のあとに送る (MUST)。
根拠: store/outbox.go (updateTaskLocal); TestCustomTypeOfOfflineTaskIsAnUpdate

#### Scenario: 作ってから種類を付ける
- **WHEN** オフラインで作ったタスクに種類を付け、名前を変える
- **THEN** `create_task` には種類が無く、種類と名前は後の `update_task` に入る

### Requirement: カスタムタイプを送る
`custom_type` か `custom_type_status_option` のある `update_task` は、`resource_subtype: custom` と (ステータスだけなら base の) `custom_type` も付けて `PUT /tasks/{gid}` で送り、`POST /batch` にはまとめない (MUST)。Asana のステータスが base と違えば競合にする。
根拠: sync/push_customtypes.go (taskUpdateFields); sync/batch.go (batchEntryFor); TestPushCustomType, TestPushCustomTypeConflict

#### Scenario: ステータスだけを変える
- **WHEN** 種類「バグ」のタスクのステータスを変えて送る
- **THEN** 本文に `resource_subtype: custom`・`custom_type`・`custom_type_status_option` が入る

### Requirement: Asana が受け付けないカスタムタイプ
Asana が断ったとき (402・400・403・422)、または 200 でも答えに送った種類・ステータスが無いときは、種類の部分だけを操作から外し、理由付きで捨てた変更に残して、タスクの種類・ステータス・完了をローカルで base に戻す (MUST)。残りは、断られたときは送り直し、200 のときは完了にする。答えの種類かステータスに gid が無いときは比べられないので、断られたとはみなさない。
根拠: sync/push_customtypes.go (customTypeRefused, customTypeApplied); store/customtypes_write.go (WithdrawCustomType); TestPushCustomTypeNotSupported, TestCustomTypeAppliedWithoutGid

#### Scenario: Asana が種類を受け付けない
- **WHEN** 種類と名前を変えた変更に、Asana が 400 を返す (または種類を変えずに 200 を返す)
- **THEN** 種類は理由付きで捨てた変更になってタスクの種類が元に戻り、名前は Asana に届く

### Requirement: カスタムタイプの 402
種類を送って 402 が返ったら、そのタスクのワークスペースについて `plan_unavailable:custom_types:<workspace>` を記録する (MUST)。同じ操作に開始日があっても、開始日の印は付けず、種類を外した残りと一緒に送り直す。
根拠: sync/push_customtypes.go (customTypeRefused); sync/push.go (send); TestPushCustomTypePaymentRequired, TestPushCustomTypeAndStartOnPaymentRequired

#### Scenario: 開始日と種類に 402
- **WHEN** 開始日と種類を変えた変更に、種類があるときだけ 402 が返る
- **THEN** 種類だけが捨てた変更になり、開始日は送られ、開始日の印は付かない

### Requirement: 並べ替えはキャッシュにもすぐ出す
`move_task` と `set_parent` を積んだら、Asana が置く場所にキャッシュでも置く (MUST)。`insert_before` / `insert_after` があればその隣、位置なしで別のセクションへ移したらその先頭、位置なしで親を変えたら末尾。送るまでは、並びの取り直しやタスクの取り込みのあとも、積んだ場所に戻す。
根拠: store/outbox.go (MoveTaskAtLocal, reapplyUpdates); store/taskops.go (SetParentLocal, reapplySetParent); store/order.go (placeInSection, placeSubtask, reapplyMoves, reapplyParentMoves); TestOrderOfUnsentMove, TestOrderOfMoveToOtherSection, TestOrderOfUnsentSetParent

#### Scenario: 前に置いて同期する
- **WHEN** `task move C --before A` を積み、送る前に Asana の古い並びを取り込む
- **THEN** キャッシュの一覧では C が A の前のまま

### Requirement: 並べ替えをやめたら元の場所に戻す
元の場所に位置なしで戻したときと、操作を取り消したときは、そのとき前にあったタスクのあとに戻す (MUST)。そのタスクがもうなければ、元の番号に戻す。
根拠: store/order.go (recordPlace, restorePlace); store/outbox.go (discardOpTx); TestOrderDiscardAfterRenumbering, TestOrderOfMoveToOtherSection

#### Scenario: 並べ替えを取り消す
- **WHEN** C を A の後ろに移して、送る前に outbox から取り消す
- **THEN** C は元の場所 (前にあったタスクのあと) に戻る

## 未確認

- `safeToResend` を直接確かめる単体テストはない (push のテストを通して確かめている)。
- #54 (DB のエラーを ErrNotFound にしない) を確かめるテストの関数名。
- 本物の Asana での照合 (検索の反映の遅れが 1 分・5 分で足りるか)。
- 1 回の push で 1 つの操作に 4 手順まで、照合は 1 回まで、を直接確かめるテスト (コードは push.go:185-225, :327-328)。
