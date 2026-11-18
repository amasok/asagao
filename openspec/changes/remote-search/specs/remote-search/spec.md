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
Asana の検索 API が 402 を返したら `plan_unavailable:search` に覚え、それからは検索 API を呼ばずに typeahead (タスク名の先頭) で探し、そう探したことを結果に添える (MUST)。typeahead も 402 なら `plan_unavailable:typeahead` に覚え、Asana での検索はプランで使えないエラーにし、名前は Asana に聞かない。402 以外のエラーは覚えず、切り替えない。
根拠: sync/search.go (searchRefs, findNames); TestSearchTasks402SwitchesToTypeaheadAndRemembers, TestSearchTasksOtherErrorsDoNotSwitch, TestSearchTasksWithoutTypeahead, TestSearchRemoteWithoutTheSearchAPI

#### Scenario: 無料のワークスペース
- **WHEN** 検索 API が 402 を返すワークスペースで、Asana でも検索を 2 回する
- **THEN** 検索 API は 1 回だけ呼ばれ、2 回とも typeahead で探し、「タスク名の先頭で探しました」と出る

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
根拠: app/gitlink.go (ResolveCustomIDs, GitLink.CustomIDs); sync/search.go (TaskByCustomID); cli/git.go (resolveCustomIDs); TestGitLinkByCustomID, TestResolveCustomIDs, TestTaskByCustomID, TestGitScanByCustomID

#### Scenario: タスクの ID で完了にする
- **WHEN** オンラインで、メッセージが `fixes asana#ENG-1 and see asana#ENG-404` のコミットを `git scan` する (ENG-1 は gitA、ENG-404 はない)
- **THEN** gitA を完了にしてコメントし、ENG-404 は「見つかりません」と出る。あとでオフラインのときに `asana#ENG-1` を読んでも、覚えた gid で gitA にコメントを積む
