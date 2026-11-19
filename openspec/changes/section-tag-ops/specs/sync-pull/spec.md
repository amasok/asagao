## ADDED Requirements

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
