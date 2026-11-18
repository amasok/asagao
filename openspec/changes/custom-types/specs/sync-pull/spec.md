## ADDED Requirements

### Requirement: カスタムタイプの取得
プロジェクトのカスタムフィールドを取るとき (全件の同期、変更のイベント、まだ取っていないとき) に、そのプロジェクトで使える種類とステータス (`GET /custom_types?project=`) を取ってキャッシュに入れる (MUST)。403 なら一覧を空にする。ほかの失敗では同期を失敗にせず、次の同期でまた試す。
根拠: sync/sync.go (syncCustomTypes); store/customtypes.go (ReplaceProjectCustomTypes); TestSyncCustomTypesOfProject

#### Scenario: 全件の同期で取り直す
- **WHEN** 種類を取ったプロジェクトを、全件でない同期のあとに全件で同期する
- **THEN** 全件でない同期では問い合わせず、全件の同期で取り直す

### Requirement: カスタムタイプの取得の 402
種類の取得に 402 が返ったら、一覧を空にし、ワークスペースについて `plan_unavailable:custom_types:<workspace>` を記録して、24 時間はそのワークスペースのどのプロジェクトでも問い合わせない (MUST)。そのあと試し直し、答えが来たら印を消す。ほかのワークスペースは隠さない。
根拠: sync/sync.go (syncCustomTypes); sync/plan.go; TestSyncCustomTypesPaymentRequired

#### Scenario: プランにない
- **WHEN** 種類の取得に 402 が返る
- **THEN** 同期は成功し、そのワークスペースでは種類の選択肢を出さず、24 時間は問い合わせない

### Requirement: タスクの種類を読む
タスクの `custom_type`・`custom_type_status_option` は省ける欄として読む (MUST)。断られたら、まずこの 2 つだけを外して読み直し、それでも断られたら省ける欄を全部外す。種類を読まなかったタスクでは、キャッシュの種類を保ち、競合の比べにも使わない。
根拠: asana/customfields.go (readTasks, fieldTier); store/write.go; readmodel/fieldvalues.go; TestCustomTypeFieldsRefusedAlone, TestCustomTypeNotFetchedIsKept, TestCustomTypePartialAndCleared

#### Scenario: 種類の欄だけを断るプラン
- **WHEN** タスクの読み取りが、opt_fields に `custom_type` があるときだけ 402 になる
- **THEN** カスタムフィールドと依存関係は読まれ、キャッシュの種類は消えない

### Requirement: 種類の gid を名指しして読む
タスクの種類とステータスは、gid と名前を名指しして (`custom_type.gid`・`custom_type.name`・`custom_type_status_option.gid`・`custom_type_status_option.name`) 読む (MUST)。Asana は `.name` だけを頼むと gid を返さない。種類かステータスが gid 無しで来たタスクでは、キャッシュの種類を保つ。
根拠: asana/resources.go (TaskFields); store/customtypes.go (upsertTaskCustomType); TestTaskFieldsHaveCustomType, TestCustomTypeReadWithGid, TestCustomTypeWithoutGidIsKept

#### Scenario: gid を名指しして読む
- **WHEN** 種類「バグ」・ステータス「対応中」のタスクを読む (Asana は名指しした欄だけを返す)
- **THEN** キャッシュのタスクに種類とステータスの gid と名前が入り、そのステータスを変えられる

### Requirement: 種類を読めていなかったキャッシュ
種類を `custom_type.name` だけで読んでいた版のキャッシュに、種類の無いカスタムタイプのタスクがあれば、マイグレーション (23 番) で最後の全件の同期の記録を消し、次の同期を全件にして種類を読み直す (MUST)。そのタスクが無いキャッシュは、全件の同期の記録を保つ。
根拠: store/schema.go (migrations); TestMigrateStoreWithCustomTasksWithoutType

#### Scenario: 種類の無いカスタムタイプのタスク
- **WHEN** 種類の無いカスタムタイプのタスクがあるキャッシュを、新しい版で開く
- **THEN** 次の同期は全件になる
