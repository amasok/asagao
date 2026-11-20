# プロジェクトの一覧の分け方と、概要・編集 (Issue #38・#39)

振る舞いを変える変更 (足すだけ)。

## なぜ

プロジェクトの一覧はワークスペースの全部が平らに並ぶだけで、チームごとに分けたり、Web で「お気に入り」にしたものを上に出したりできなかった。新しいプロジェクトのチームも選べなかった (組織ではチームが要る)。また、プロジェクトの説明・オーナー・開始日と期日・色を変えられず、概要 (進み具合・ブリーフ) も見えなかった。

## 何を変えるか

- 同期で、参加しているチーム (`GET /users/me/teams`) と自分のお気に入りのプロジェクト (`GET /users/me/favorites?resource_type=project`) を取ってキャッシュする。ポーリングのとき (と、まだ取っていないとき) だけ。402・403 は「無い」、ほかの失敗はキャッシュを残して次の同期で取り直す。
- プロジェクトの一覧は「お気に入り (自分の並び順) → チーム名の順 → チームなし」。TUI のサイドバーと CLI `project list` は、区分が 2 つ以上あるときだけ見出しを出す。CLI `project list --team`、MCP `list_projects` の `team` で 1 チームに絞る。
- 新しいプロジェクトのチーム: 指定があればそれ、参加しているチームが 1 つならそれ、複数なら選ばせる (TUI はピッカー、CLI `project create --team`、MCP `create_project` の `team`)。候補は CLI `project teams`。まだ取っていなければキャッシュのプロジェクトのチーム。
- プロジェクトの説明 (Markdown ⇔ html_notes)・オーナー・開始日・期日・色を変える。新しい outbox の種類 `update_project` に積み (オフラインでも)、同期で `PUT /projects/{gid}` を送る。タスクの更新と同じく、同じプロジェクトの編集は 1 つにまとめ、元に戻せば消し、Asana 側で同じ欄が変わっていれば競合にする。開始日には期日が要る (Asana の決まり) ので、開始日を送るときは期日も添える。
- 概要: 説明・オーナー・期間・色・進み具合 (`GET /projects/{gid}/task_counts`)・最新のステータス・ブリーフ (`GET /project_briefs/{gid}`、preview)。開いたときにオンラインなら取り直してキャッシュし、表示はキャッシュから。ブリーフが 402・403 なら出さない。TUI はキー `O` (メニュー) で開き、`e` でエディタで編集。CLI `project show` / `project edit` (`--dry-run` 対応)。MCP `get_project` に概要、`update_project` に `notes`・`owner`・`start_on`・`due_on`・`color`。
- マイグレーション 1 件: projects に html_notes・owner_gid・start_on・due_on、表 teams・project_favorites・project_overviews。

## やらないこと

- ブリーフの編集 (preview の API なので読むだけ)。
- チームの作成やメンバーの変更、お気に入りの付け外し (Asana の API にない)。
- プロジェクト名の変更 (今回の Issue の範囲外)。
