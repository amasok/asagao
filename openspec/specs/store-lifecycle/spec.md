# store-lifecycle (キャッシュの開き方・ロック・移行・初期化)

## Purpose

ローカルのキャッシュ (SQLite の `store.db`) を開く・移行する・排他する・初期化する振る舞いの基準。TUI・CLI・MCP サーバーの複数のプロセスが同じファイルを同時に使っても壊れないこと、未送信の変更を失わないことを書く。コンテキストは「ローカルキャッシュ」(docs/design/context-map.md)。今の実装は `internal/store` (store.go、schema.go、lock*.go、reset*.go、termsafe_driver.go)。

## Requirements

### Requirement: 開き方
`store.Open` はフォルダを 0700、ファイルを 0600 で作り、WAL・`busy_timeout` 5000ms・外部キー有効・書き込みのトランザクションは BEGIN でロックを取る (`_txlock=immediate`) 設定で開く (MUST)。既にあるフォルダやファイルの権限は変えない。
根拠: internal/store/store.go:25-59; TestWriteTxReadThenWriteDoesNotFailAsBusy, TestConcurrentWritersFromTwoStoresDoNotFailAsBusy (#50)

#### Scenario: 2 つのプロセスが同時に書く
- **WHEN** 2 つの Store が同じファイルで同時に書き込みのトランザクションを始める
- **THEN** 片方は待ち、どちらも busy で失敗しない

### Requirement: 移行は末尾に足し、1 回だけ当てる
スキーマの移行は `migrations` の順に、1 つずつ別のトランザクションで当て、`PRAGMA user_version` に当てた数を記録する (MUST)。版は書き込みのロックを取ってから読み直すので、同時に開いても 1 回だけ当たる。busy のときは 5 秒の中で 20ms ごとに試し直す。今の移行は 15 個。
根拠: internal/store/schema.go; store.go:67-161; TestConcurrentOpenMigratesOnce, TestRetryBusyStaysWithinBudget, TestMigrateStoreFromBeforeInboxAndFields

#### Scenario: 2 つのプロセスが同時に新しい版で開く
- **WHEN** 古いスキーマのファイルを 2 つのプロセスが同時に開く
- **THEN** 各移行は 1 回だけ当たり、どちらも開ける

### Requirement: 送信と初期化は 1 つのプロセスだけ
outbox の送信 (push)、同期全体 (`Syncer.Run`)、初期化 (`ResetUpTo`) は、`<db>.push.lock` の排他ロック (Unix は flock、Windows は LockFileEx) を待たずに取る (MUST)。取れなければ `ErrPushBusy` (「別のプロセスが未送信の変更を送信中です」) を返す。開くことと移行には、このロックを使わない。
根拠: internal/store/lock.go:10-28; lock_unix.go; lock_windows.go; internal/sync/push.go:130-137; sync.go:168-175; store/reset.go:54-58; TestBusyAttackPushLockWithWritersInOtherProcesses, TestResetWaitsForNoPush

#### Scenario: 同期中に初期化する
- **WHEN** 別のプロセスが同期している間に `reset` を実行する
- **THEN** 初期化は `ErrPushBusy` で止まり、何も消さない

### Requirement: 未送信の変更があれば初期化しない
初期化は、指定した番号より新しい outbox の操作があれば `ErrUnsentChanges` を返し、何も消さない (MUST)。消すときは 1 つのトランザクションで、outbox の操作を「破棄した変更」として記録してから、キャッシュの表を消す。バックアップ付きの初期化は、先に操作と破棄の記録を JSON (0700 のフォルダ、fsync) に書き、書けなければ何も消さない。
根拠: internal/store/reset.go:38-130; reset_backup.go:18-110; TestResetRefusesWithUnsentChanges, TestResetDeletesOnlyConfirmedChanges, TestResetWithBackup

#### Scenario: 確認のあとに新しい操作が積まれた
- **WHEN** 利用者が確認したあとに、ほかのプロセスが新しい操作を積む
- **THEN** 初期化は `ErrUnsentChanges` で止まり、新しい操作は残る

### Requirement: 読んだ文字は端末で安全にする
store から読む文字列の列は、すべて termsafe を通して端末の制御文字を取り除く (MUST)。ドライバーを包む形で入れ、グローバルには登録しない。TEXT 列の JSON は、デコードする場所で `termsafe.Unmarshal` を使う。
根拠: internal/store/termsafe_driver.go:18-124; store.go:44; TestStoreReadsAreTerminalSafe, TestStoreOtherTextsAreTerminalSafe

#### Scenario: タスク名にエスケープシーケンス
- **WHEN** Asana から来たタスク名に ESC の制御文字がある
- **THEN** store から読んだ名前に制御文字はない

### Requirement: 大きな取り込みでもほかの書き手を待たせない
タスクの upsert は、約 500ms ごとのトランザクションに分け、間に 150ms 空ける (MUST)。
根拠: internal/store/write.go:96-145; TestUpsertTasksLetsOtherWritersIn (#55)

#### Scenario: 全件の同期中に TUI で編集する
- **WHEN** 大きな全件の同期の間に、別のプロセスがタスクを書く
- **THEN** その書き込みは busy で失敗しない

## 未確認

- キャッシュより新しいスキーマのファイル (`user_version` が移行の数より大きい) を開いたときは、何も言わずに開く (store.go:131-133)。テストはない。古いバイナリでの読み書きが安全かも未確認。
- `-wal` と `-shm` のファイルの権限。
- DB ファイルの権限 (0600) を確かめるテスト。
