## ADDED Requirements

### Requirement: ワークスペースのカスタムフィールドの取得
定期の取り直し (poll) のとき、またはまだ取っていないとき、ワークスペースごとにカスタムフィールド (`GET /workspaces/{gid}/custom_fields`、選択肢つき) を取り、キャッシュに入れる (MUST)。プロジェクトに付けられるフィールドの一覧に使う。402 なら一覧を空にして `plan_unavailable:custom_fields:<workspace>` を記録し、403 なら一覧を空にする。ほかの失敗では同期を失敗にせず、今のキャッシュを保って次の同期でまた試す。
根拠: sync/push_fields.go (syncWorkspaceCustomFields); store/field_ops.go (ReplaceWorkspaceCustomFields); TestSyncWorkspaceCustomFields

#### Scenario: プランにない
- **WHEN** ワークスペースのカスタムフィールドの取得に 402 が返る
- **THEN** 同期は成功し、付けられるフィールドの一覧は空になり、フィールドの付け外しと選択肢の追加は出さない
