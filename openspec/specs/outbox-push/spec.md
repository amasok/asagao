# outbox-push (送信待ちの操作・送信・照合)

## Purpose

ローカルの変更を outbox (送信待ちの操作) に積み、Asana に送り (push)、送れたか分からないときに Asana を見て照合する振る舞いの基準。操作の種類、ローカル gid、畳み込みの規則、送る順番、結果ごとの扱い、再送してよいかの判断、照合、利用者による解決を書く。コンテキストは「送信待ち」と「同期」(docs/design/context-map.md)。今の実装は `internal/store` (outbox.go、outbox_send.go、tags.go、relations.go、customfields.go、dependencies.go、sections.go、projects.go、notifications.go、results.go、discard*.go) と `internal/sync` (push.go、reconcile.go、resolve.go)。

## Requirements

### Requirement: 操作の種類
outbox の操作の種類は 21 個 (MUST): `update_task`、`create_task`、`add_comment`、`move_task`、`add_follower`、`remove_follower`、`add_project`、`remove_project`、`duplicate_task`、`upload_attachment`、`set_custom_field`、`add_tag`、`remove_tag`、`add_dependency`、`remove_dependency`、`rename_section`、`create_status`、`mark_notification`、`update_story`、`delete_story`、`delete_attachment`。
根拠: internal/domain/outbox/outbox.go (種類の定数。store の Op* はその別名)

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
根拠: internal/sync/push.go:89-210, :327-349; store/outbox.go:363-383; sync/inbox.go:170-225; TestPushSkipsWhileAnotherProcessPushes, TestPushDoesNotDropWhenAccountChanged, TestPushChildWaitsForFailedParentCreate, TestPushSendsChangesQueuedMeanwhile

#### Scenario: 親の作成が失敗している
- **WHEN** 親タスクの作成が `failed` で、子の作成が積まれている
- **THEN** 子は送らず `pending` のまま残る

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
自動で捨てた変更は、要約・理由・payload ごと `discarded_changes` に記録し、利用者に 1 回だけ知らせる (MUST)。一覧では、知らせたかどうかを問わず新しい順に出す。理由は保存する値で、初期化で消したものはコード `reset` (`readmodel.DiscardReasonReset`)、それ以外は Asana の応答を述べる文にする。表示の文言は保存しない (#82。それより前に保存した日本語の文は、マイグレーションで `reset` にする)。CLI (`outbox discarded --json`、書き込みの `--json` の `discarded`) と MCP の `discarded` は、`reason` を保存したまま返す。要約は、組み立てが `Store.SetDescribeChange` で渡す表示の説明で記録し、渡されていなければ空にして、表示するときに種類と payload から作る。
根拠: store/discard.go (recordDiscard); store/discard_list.go; store/schema.go (最後のマイグレーション); readmodel/discard.go (DiscardReasonReset、IsReset); TestDroppedChangeKeepsPayload, TestRecentDiscards, TestMigrateResetReasonToCode, TestResetDiscardReasonAndSummary, TestOpenStoreDescribesDroppedChanges

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

## 未確認

- `safeToResend` を直接確かめる単体テストはない (push のテストを通して確かめている)。
- #54 (DB のエラーを ErrNotFound にしない) を確かめるテストの関数名。
- 本物の Asana での照合 (検索の反映の遅れが 1 分・5 分で足りるか)。
- 1 回の push で 1 つの操作に 4 手順まで、照合は 1 回まで、を直接確かめるテスト (コードは push.go:185-225, :327-328)。
