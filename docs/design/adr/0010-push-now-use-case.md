# ADR 0010: 書き込みのあとの即時送信の判定は app.PushNow にまとめ、送る手段は入り口が渡す

- 状態: 採用 (Issue #75)
- 日付: 2026-11-20
- ADR 0008 の決めたことの 1 (送り方のポートは作らない) のうち、CLI と MCP の「すぐ送って結果を判定する」部分を置き換える。TUI の送り方 (同期の依頼) は ADR 0008 のまま

## 背景

- CLI (`reportWrite`) と MCP (`s.push`) は、どちらも書き込みのあと 20 秒の期限で `Syncer.Push` を呼び、結果 (別のアカウント、送信中、ログイン、`InboxReadOnly`、破棄) を分けて、ローカルの gid を Asana の gid に直していた。同じ手順を 2 か所で書いていたため、細かい判定がずれていた。
  - MCP は `PushResult.Remaining` を見ず、作成が 500 で返ってキューに残った (次の push で Asana を確かめる) ときも `sent: true` を返していた。
  - CLI は作ったタスクを送ったあと、`task_gid` を空にしていた (Asana の gid を読まずに、分からないものとして扱っていた)。MCP は Asana の gid を返していた。
  - CLI はログインしていないときも「オフラインのため未送信」と出していた。MCP はログインの問題として分けていた。
- ADR 0008 で送り方のポートを作らなかった理由は「app に使う側がない」ことだった。判定を app に移すと、使う側ができる。

## 決めたこと

1. **`app.PushNow.Run(ctx, connect, PushTarget)` が、接続、push、結果の分類、gid の解決を行い、`PushReport` を返す。** 入り口は、`PushReport` を自分の形 (CLI のテキストと `--json`、MCP の `WriteOutput`) に直すだけにする。
2. **送る手段は、入り口が `connect` で渡す。** `connect` は `app.Pusher` (`Push(ctx) (readmodel.PushResult, error)`) を返す。`*sync.Syncer` がそのまま満たす。`internal/app` は今までどおり `internal/sync` を import しない。
   - 期限 (`DefaultPushTimeout`、20 秒) は接続と push の両方にかかる (CLI の今の形)。MCP は syncer をあとの呼び出しでも使うので、`connect` に渡る期限付きの context ではなく、呼び出しの context で作る。
   - ログインの問題かどうかは `LoginHelp` (入り口が `auth.LoginHelp` を渡す) で分ける。
3. **共有の型を readmodel に移す。** `PushResult` と `ErrAccountChanged` を `internal/readmodel` に置き、`internal/sync` には同じ名前の別名を残す (`ErrPushBusy` と同じ形)。
4. **判定は正しい方にそろえる。**
   - 送れたか (`Sent`): エラーがなく、衝突・失敗・残り (`Remaining`) がどれも 0 で、`InboxReadOnly` でなく、この変更が破棄されていないとき (CLI の判定)。
   - 分類の順番: `InboxReadOnly` (通知の既読の変更を元に戻したことは確か) → 別のアカウント → ログインを拒まれた → 送信中 → そのほかのエラー → 衝突・失敗・残り → 送れた。
   - gid: 作ったタスク・セクション・作業時間は、Asana が作ったあとは Asana の gid を返す (MCP の形)。作業時間は、作成がまだキューにあるあいだはローカルの gid のまま。
   - 破棄: 書き込みが渡した gid の変更が破棄されたら、送れていない。オフラインで作ったタスクのあとの変更 (削除されたセクションへの移動など) が Asana の gid で破棄されても、作成は届いているので送れたとし、破棄は別に知らせる。タスクを持たない書き込み (セクション、タグなど) は、タスクを持たない変更の破棄を自分のものとみなす (今までと同じ)。
   - CLI は、まだ見せていない破棄 (前の push や、送信中だったほかのプロセスのもの) も `PushReport.Drop` に渡して判定する。MCP は、この push の破棄だけを返す (TUI と CLI があとで見せるため、見せたことにはしない)。

## 理由

- 判定を 1 か所にすると、入り口ごとのずれがなくなり、分類と gid の解決を偽の pusher と偽の store で試せる。
- 送る手段は入り口に残るので、ADR 0008 の理由 (TUI の同期の状態を画面の外に出さない) は変わらない。

## 結果

- CLI の `--json` と MCP の出力の欄は変わらない。値は 4 の判定に従って変わる: MCP は残りがあると `sent: false` とメモ (`still queued`) を返す。CLI は送ったタスクの `task_gid` に Asana の gid を入れ、ログインしていないときは「ログインが必要なため未送信」と出す。

## 選ばなかった案

- **新しいパッケージ (例: `internal/pushnow`) に置く**: 層が増える。app に置けば、ほかのユースケースと同じく、ポートと偽の実装で試せる。
- **app で `sync.PushResult` を受ける**: app と sync は互いに import しない (規則 2)。
