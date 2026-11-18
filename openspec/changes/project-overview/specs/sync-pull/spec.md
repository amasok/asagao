## ADDED Requirements

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
