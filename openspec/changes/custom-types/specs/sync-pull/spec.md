## ADDED Requirements

### Requirement: カスタムタイプの取得
プロジェクトのカスタムフィールドを取るとき (全件の同期、変更のイベント、まだ取っていないとき) に、そのプロジェクトで使える種類とステータス (`GET /custom_types?project=`) を取ってキャッシュに入れる (MUST)。402 なら一覧を空にし、ワークスペースについて `plan_unavailable:custom_types:<workspace>` を記録して、24 時間はそのワークスペースのどのプロジェクトでも問い合わせない (そのあと試し直し、答えが来たら印を消す)。403 なら一覧を空にする。ほかの失敗では同期を失敗にせず、次の同期でまた試す。タスクの `custom_type`・`custom_type_status_option` は省ける欄として読み、省いて読んだタスクでは今の値を保つ。
根拠: sync/sync.go (syncCustomTypes); store/customtypes.go (ReplaceProjectCustomTypes, upsertTaskCustomType); asana/customfields.go (isOptionalTaskField); TestSyncCustomTypesOfProject, TestSyncCustomTypesPaymentRequired, TestCustomTypePartialAndCleared

#### Scenario: プランにない
- **WHEN** 種類の取得に 402 が返る
- **THEN** 同期は成功し、そのワークスペースでは種類の選択肢を出さず、24 時間は問い合わせない
