# 集約と不変条件

集約は「1 つのトランザクションで守る決まりのまとまり」。asana-tui では、本当の持ち主は Asana なので、ここで書くのは**ローカルの側で守る決まり** (キャッシュと outbox が食い違わないこと、送ったものを二重に作らないこと) になる。根拠は仕様 (`openspec/specs/`) とコードの場所。

## 一覧

| 集約 | 根 (root) | 中に含むもの | 1 つのトランザクションの例 |
|---|---|---|---|
| タスク | タスク (gid) | メンバーシップ、タグ、フォロワー、カスタムフィールドの値、依存、詳細、そのタスクへの未送信の操作 | `UpdateTaskLocal`、`upsertTaskTx` (取り込みと当て直し) |
| 送信待ちの操作 | outbox の 1 行 (id) | payload、base、conflict、状態、verify、試した回数 | `BeginSend`、`Complete*`、`Mark*`、`DiscardOp` |
| コメント | story (gid) | 本文 (text と html_text の組) | `AddRichCommentLocal` |
| status update | status (gid) | 種類、題、本文の組 | `CreateStatusLocal` |
| 同期の記録 | `sync_state` のキー | `last_sync`・`last_full_sync`・`last_poll`・`events:<project>`・`me_gid`・送った記録 | `SetSyncState`、`PruneResults` |
| ログイン | 保存先の 1 項目 | refresh token / PAT、client secret、ユーザー | `tokenstore.Store.Save` |

タスクと送信待ちの操作は、別の集約だが**同じトランザクションで一緒に変える**ことがある (積む、完了する、当て直す)。SQLite の 1 つのファイルにあるので、今はそれで足りている。分けたときもこの単位を崩さない (コンテキストマップの「共有の核」)。

## タスク

不変条件:

1. 名前は空でない。根拠: store/outbox.go:261-262, `validateFields` (:714-743)。
2. 承認の状態は、種類が `approval` のタスクにだけある。承認の状態を変えたら、完了の状態もそれに合わせる。根拠: outbox.go:93-101, :814-817。
3. `html_notes` は `notes` と組で、`notes` だけを書いたら `html_notes` は空になる。根拠: outbox.go:776-826。
4. 未送信の操作があるタスクは、取り込みで値が消えない (`failed` 以外を当て直す)。片付けでも消えない。根拠: store/write.go:149-166, :330-333。
5. ローカル gid のタスクは、作成の操作 (`create_task` か `duplicate_task`) が outbox にある間だけある。作成を捨てたら、サブタスクと操作ごと消える。根拠: outbox.go:560-595, :912-922。
6. ローカル gid のタスクには、複製・依存・プロジェクトの変更をできない。根拠: relations.go:64-66, :182; dependencies.go:147。

## 送信待ちの操作

不変条件:

1. **畳み込み**: 1 つのタスクに、`pending` か `conflict` の `update_task` は高々 1 つ (新しい編集はそこに合わせる)。`sending` の操作には合わせない。根拠: outbox.go:925-936。
2. **base**: 欄ごとの base は、最初に積んだときの値で、あとから変えない。新しい値が base と同じなら、その欄は payload にない。payload が空の操作はない。根拠: outbox.go:139-162。
3. **未送信の作成**: 一度も送っていない作成があるタスクへの変更は、別の操作にならず作成の payload に入る。根拠: outbox.go:102-114, :888-897。
4. **状態の移り方**:
   ```
   pending ──BeginSend──▶ sending ──成功──▶ (削除)
      ▲                      │
      │ ReleaseOp/            ├──分からない──▶ pending + verify ──照合──▶ (削除) / pending / failed
      │ ReleaseNotSent        ├──断られた────▶ failed ──retry──▶ pending
      └──────────────────────┤
                             └──競合─────────▶ conflict ──keep_local──▶ pending
                                                       └──keep_remote / discard──▶ (削除)
   ```
   `sending` のまま残った操作は、次の push の初めに `pending` に戻す (再送してよくないものは verify を付ける)。根拠: outbox_send.go:41-162; sync/resolve.go:72-106。
5. **再送してよくない操作** (作成 5 種と、記録のないローカルのタグへの `add_tag`) は、届いたか分からないまま再送しない。根拠: outbox_send.go:97-115。
6. **試した回数**: 届いていないと分かったときは数えない。合わせて 5 回で `failed`。根拠: push.go:291-434; TestPushOfflineDoesNotCountAttempts。
7. **完了は 1 つのトランザクション**: 送った記録・gid の書き換え・キャッシュの反映・操作の削除は同時に行う。根拠: outbox_send.go:13-18, :165-173。

## コメント・status update

1. 本文は、送り先 (`Comment` / `StatusUpdate`) で変換した rich text と plain の組。見出し・区切り線・画像・表のタグを持たない。根拠: rich-text の仕様。
2. ローカル gid のコメントは、取り込みで消えない (`ReplaceStories` が `local:` を残す)。根拠: store/write.go:286。
3. 作成は再送してよくない (照合する)。根拠: outbox_send.go:97-115。

## 同期の記録

1. Events の token は、そのプロジェクトの出来事をすべてキャッシュにしまったあとでだけ進む。根拠: sync/sync.go:375-381。
2. `last_full_sync` は、片付けが終わったあとでだけ進む。`last_sync` は最後に進む。根拠: sync.go:383-403。
3. 記録する時刻は Asana の時計 (カスタムフィールドの記録を除く。未確認の扱い)。根拠: sync.go:129-131, :438。
4. 送った記録 (ローカル gid → 本物の gid) は、どこかで使われている間と、そのあと 30 日は消さない。根拠: store/results.go。
5. `me_gid` と Asana の答えが違うとき、同期は何も書かない。根拠: sync.go:118-128。

## ログイン

1. 保存するのは、refresh token が変わったとき (アクセストークンを保存する保存先なら、アクセストークンが変わったときも)。根拠: auth/tokensource.go:169-190。
2. 保存の前に読み直し、別のユーザーのログインを上書きしない。根拠: tokensource.go:139-147, :200-221。

## 段階 b で外に出す判断

今は SQL と同じ関数の中にあり、段階 b でドメインの関数 (標準ライブラリだけ、時刻は引数) にする候補:

| 判断 | 今の場所 | 出す先 (案) |
|---|---|---|
| 欄の検証、承認の状態の確認 | `validateFields`、`UpdateTaskLocal` | `domain/task` |
| 畳み込み (base、元に戻したら外す、conflict を pending に) | `UpdateTaskLocal` の後半 | `domain/outbox` の `Fold` |
| 未送信の作成かどうか | `findUnsentCreate` の WHERE | `domain/outbox` の `Op.NeverSent()` |
| 再送してよいか | `safeToResend` | `domain/outbox` の `SafeToResend(op, tagRecorded bool)` |
| 片付けてよいか | `PruneTasks` の WHERE | `domain/task` の規則を文書とテストにし、SQL は残す (migration-plan.md の c3) |
| ローカル gid | `store.LocalPrefix`、`IsLocalGID` (sync 17 か所、tui 3、cli 1、store 17) と SQL の `LIKE 'local:%'` 5 か所 | `domain` の `LocalGID` 型と `IsLocal` |
| 送り先と許すタグ | `richtext.Target`、`layout()`、`isBlockElement` | `domain/richtext` (名前は段階 b で決める) |
| 照合で同じものとみなす条件 | `sameTaskFields`、`sameComment` | `domain/outbox` |

注: Issue #69 の本文は「ローカル gid の判定が sync に 15 か所以上に散らばっている」としていたが、調べると sync は判定を自分で書かず、すべて `store.IsLocalGID` を呼んでいる (17 か所)。散らばっているのは**判定の持ち主** (store) と、SQL の中の `LIKE 'local:%'` (store に 5 か所: write.go:286、attachments.go:50、projects.go:47、outbox.go:561、results.go:56)。
