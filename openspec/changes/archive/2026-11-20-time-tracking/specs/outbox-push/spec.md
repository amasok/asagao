## ADDED Requirements

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
