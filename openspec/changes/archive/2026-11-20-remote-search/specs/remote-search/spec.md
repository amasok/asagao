## ADDED Requirements

### Requirement: Asana でも検索する
CLI の `search --remote`、TUI の `space g`、MCP の `search_tasks` の `remote: true` は、キャッシュに加えて Asana でも検索する (MUST)。キャッシュの一致のあとに、Asana で見つかったタスクのうちキャッシュの一致にないものを足す。ほかの絞り込みと件数の上限は両方に効く。見つかったタスクは、全部の欄を Asana から読んでからキャッシュに入れる。
根拠: app/remotesearch.go; sync/search.go; TestSearchWithAsanaAddsWhatAsanaFound, TestSearchWithAsanaKeepsTheLimit, TestSearchTasksCachesWhatTheSearchAPIFinds, TestSearchRemoteFindsAndCaches, TestSearchAsanaFromTheTUI, TestSearchTasksRemote

#### Scenario: 同期していないプロジェクトのタスクを見つける
- **WHEN** オンラインで `asagao search 請求書 --remote` を実行し、Asana の検索 API が、キャッシュにない完了タスク t5 を返す
- **THEN** 結果に t5 が出て、t5 がキャッシュに入り、標準エラーに「Asana で N 件見つかりました」が出る

### Requirement: Asana に届かないときの検索
Asana でも検索するとき、Asana に届かなければエラーにせず、キャッシュの一致と、探せなかった理由を出す (MUST)。理由は CLI は標準エラー、MCP は `note`、TUI はステータス行に出す。CLI の `--json` の形は変えない。
根拠: app/remotesearch.go (RemoteErr); cli/task.go (searchRemote); TestSearchWithAsanaOffline, TestSearchRemoteOffline, TestSearchTasksRemoteOffline, TestSearchAsanaNeedsAQueryAndAConnection

#### Scenario: オフライン
- **WHEN** ログインしていない状態で `asagao search ソファ --remote` を実行する
- **THEN** キャッシュの一致を出し、標準エラーに「Asana では探せませんでした」と理由が出る。終了コードは 0

### Requirement: 検索 API がないプランでは typeahead を使う
Asana の検索 API が 402 を返したら、探したワークスペースについて `plan_unavailable:search:<ワークスペースの gid>` に覚え、24 時間は検索 API を呼ばずに typeahead (タスク名の先頭) で探し、そう探したことを結果に添える (MUST)。typeahead も 402 なら `plan_unavailable:typeahead:<ワークスペースの gid>` に覚え、24 時間は Asana での検索をプランで使えないエラーにし、名前は Asana に聞かない。402 以外のエラーは覚えず、切り替えない。
根拠: sync/search.go (searchRefs, findNames); TestSearchTasks402SwitchesToTypeaheadAndRemembers, TestSearchTasksWithoutTypeahead, TestSearchPlanPerWorkspaceAndRecheck

#### Scenario: 無料のワークスペース
- **WHEN** 検索 API が 402 を返すワークスペースで、Asana でも検索を 2 回する
- **THEN** 検索 API は 1 回だけ呼ばれ、2 回とも typeahead で探し、「タスク名の先頭で探しました」と出る

#### Scenario: 24 時間たったら試し直す
- **WHEN** 検索 API の 402 から 24 時間たって検索し、検索 API が答える
- **THEN** 検索 API で探し、記録は消える。ほかのワークスペースの記録と、#50 より前のワークスペースのない記録 (`plan_unavailable:search`) は読まない

### Requirement: キャッシュにない名前は Asana に聞く
クイック追加の `@担当`・`#タグ` と、CLI・MCP のタスクの作成と更新の担当は、キャッシュに一致するものがないときだけ Asana に typeahead で聞き、見つかったものをキャッシュに入れて読み直す (MUST)。1 つのトークンについて聞くのは 1 回。複数に一致するときや 2 つ目の担当は聞かない。Asana に届かないとき・見つからないときは、キャッシュだけで読んだ結果のまま。
根拠: app/quickadd.go (ReadQuickAddFinding); app/tasks.go (resolveUser); app/finding_test.go; cli/search_remote_test.go; tui/remotesearch_test.go; mcpserver/search_remote_test.go

#### Scenario: キャッシュにない人を担当にする
- **WHEN** オンラインで `asagao task assign t1 hanako` を実行し、hanako はキャッシュにないが Asana の typeahead が Hanako Yamada を返す
- **THEN** Hanako Yamada がキャッシュに入り、t1 の担当になる。キャッシュにいる人 (Partner) を指定したときは Asana に聞かない

### Requirement: 見つけた人とタグはキャッシュにないものだけ入れる
名前で見つけた人・タグは、キャッシュにないものだけを入れる (MUST)。キャッシュにある人・タグは変えない (見つけた記録にメールや色がなくても消さない)。消して未送信のタグは戻さない。
根拠: store/write.go (AddUsers); store/tags.go (AddTags); TestFindUsersAddsOnlyNewUsers, TestFindTagsKeepsADeletedTagDeleted

#### Scenario: 消したタグをもう一度見つける
- **WHEN** タグ g7 をこのコンピューターで消し (未送信)、そのあと Asana の typeahead が g7 を返す
- **THEN** g7 はキャッシュに戻らず、足した数は 0

### Requirement: タスクの ID (custom ID) の参照
`git scan` と post-commit hook は、コミットメッセージの `asana#<custom ID>` (英字で始まる英数字、`-`、数字。例 `asana#ENG-123`) をタスクの参照として読む (MUST)。`asana#` のない `ENG-123` は読まない。閉じるキーワードと amend の扱いは gid の参照と同じ。
根拠: domain/commitref/commitref.go; TestParseCustomID, TestAmendedCustomID

#### Scenario: 接頭辞のない ID は読まない
- **WHEN** メッセージが `UTF-8 and SHA-256 and #ENG-123 and ENG-123` のコミットを読む
- **THEN** タスクの参照はない

### Requirement: タスクの ID を gid に解決する
`asana#<custom ID>` は Asana で引いてタスクをキャッシュに入れ、gid を `custom_id:<ID>` に覚える (MUST)。Asana に届かないとき (`--offline` を含む) は前に覚えた gid を使い、届かなかったことを警告に出す。hook は Asana を 3 秒だけ待つ。Asana がその ID を知らなければ「見つかりません」にし、覚えた gid を忘れる。
根拠: app/gitlink.go (ResolveCustomIDs, GitLink.CustomIDs); sync/search.go (TaskByCustomID); cli/git.go (resolveCustomIDs); TestGitLinkByCustomID, TestResolveCustomIDs, TestResolveCustomIDsStopsAskingAfterAFailure, TestTaskByCustomID, TestGitScanByCustomID, TestGitScanHookCustomIDSlowAsana

#### Scenario: Asana が遅い
- **WHEN** hook が `asana#ENG-1` と `asana#ENG-2` (どちらも前に見つけた) のコミットを読み、Asana が 3 秒のうちに答えない
- **THEN** 前に覚えた gid で両方のタスクにコメントを積み、Asana で引けなかったことを警告に出す

#### Scenario: タスクの ID で完了にする
- **WHEN** オンラインで、メッセージが `fixes asana#ENG-1 and see asana#ENG-404` のコミットを `git scan` する (ENG-1 は gitA、ENG-404 はない)
- **THEN** gitA を完了にしてコメントし、ENG-404 は「見つかりません」と出る。あとでオフラインのときに `asana#ENG-1` を読んでも、覚えた gid で gitA にコメントを積む

### Requirement: custom ID の API が 500 を返したら検索 API に切り替える
ID で引く API が 500 を返したときは、検索 API (`text=<ID>`) で候補を探して全欄取得し、ID カスタムフィールドの値が完全一致するタスクだけを採用してキャッシュする (MUST)。検索 API が 402 のときは `plan_unavailable:search` だけを記録して元の 500 を返し、失敗時は前に覚えた gid を消さない。
根拠: sync/search.go (TaskByCustomID, taskByCustomIDFromSearch); asana/model (HasCustomID, RawCustomIDValue); TestTaskByCustomID500FallsBackToSearch, TestTaskByCustomID500FallbackRealAsanaCustomFieldDef

#### Scenario: 500 のときに検索 API から ID 一致のタスクだけを採用する
- **WHEN** `GET /workspaces/{gid}/tasks/custom_id/RC-1` が 500 を返し、検索 API (`text=RC-1`) が本文一致のタスク t1 と ID カスタムフィールド `RC-1` を持つタスク t4 を返す
- **THEN** t4 だけを完全な欄でキャッシュに入れて `custom_id:RC-1` に t4 を覚え、t1 はキャッシュに入れない

#### Scenario: タスクが型を省くときはカスタムフィールド定義で確かめる
- **WHEN** 候補タスクの `custom_fields` が `representation_type` と `id_prefix` を省き、値 `RC-1` のテキスト欄を持つ
- **THEN** `GET /custom_fields/{gid}` で ID フィールドと確認できた場合だけ採用し、通常のテキスト欄なら採用しない

#### Scenario: 検索 API が 402 のときは元の 500 を返す
- **WHEN** `GET /workspaces/{gid}/tasks/custom_id/RC-1` が 500 を返し、切り替え先の検索 API が 402 を返す
- **THEN** `plan_unavailable:search:<ワークスペースの gid>` を記録し、`plan_unavailable:custom_id:<ワークスペースの gid>` は記録せず、元の 500 エラーを返して既存の `custom_id:RC-1` は残す

#### Scenario: 検索結果が上限 100 件で切り捨てられて一致しないときは不在と断定しない
- **WHEN** `GET /workspaces/{gid}/tasks/custom_id/RC-1` が 500 を返し、切り替え先の検索 API が上限 100 件を返してその中に `RC-1` の一致がない
- **THEN** 「見つかりません」にせず元の 500 エラーを返し、既存の `custom_id:RC-1` は残す

### Requirement: タスクの ID がないプラン
custom ID でのタスクの取得に Asana が 402 を返したら、そのワークスペースについて `plan_unavailable:custom_id:<ワークスペースの gid>` に覚え、それから 24 時間は Asana に聞かずに前に覚えた gid だけを使う (MUST)。そのあと試し直し、答え (見つからない 404 も含む) が来たら記録を消す。
根拠: sync/search.go (TaskByCustomID); TestTaskByCustomID, TestTaskByCustomID402Remembered, TestSearchPlanPerWorkspaceAndRecheck

#### Scenario: 402 のあと
- **WHEN** custom ID の取得が 402 になったあと、もう一度 `asana#ENG-1` を読む
- **THEN** Asana には聞かない
