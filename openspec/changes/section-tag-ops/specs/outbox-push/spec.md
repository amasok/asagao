## MODIFIED Requirements

### Requirement: 操作の種類
outbox の操作の種類は 25 個 (MUST): `update_task`、`create_task`、`add_comment`、`move_task`、`add_follower`、`remove_follower`、`add_project`、`remove_project`、`duplicate_task`、`upload_attachment`、`set_custom_field`、`add_tag`、`remove_tag`、`add_dependency`、`remove_dependency`、`rename_section`、`create_section`、`delete_section`、`insert_section`、`update_tag`、`delete_tag`、`move_my_section`、`create_status`、`mark_notification`、`update_project`。
根拠: internal/domain/outbox/outbox.go; internal/store/outbox.go:28-33; section_ops.go; tag_ops.go; relations.go:18-23; customfields.go:38-40; tags.go:15-16; dependencies.go:20-21; sections.go:16; projects.go:16; notifications.go:21

#### Scenario: タスクにタグを付ける
- **WHEN** 既にあるタグをタスクに付ける
- **THEN** `add_tag` の操作が積まれ、payload に `tag`・`tag_name`・`workspace` が入る

## ADDED Requirements

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
