# ゴールとポートフォリオを表示する (Issue #7)

振る舞いを変える変更 (足すだけ。読むだけの機能)。

## なぜ

Asana の Advanced プランにあるゴールとポートフォリオが、asagao では見えなかった。ゴールの進み具合や、ポートフォリオに入っているプロジェクトの状態を、ターミナルや AI エージェントから確かめたい。

## 何を変える

- ゴール (`GET /goals?workspace=`・`GET /goals/{gid}`・`GET /goal_relationships?supported_goal=`)、期間 (`GET /time_periods?workspace=`)、自分のポートフォリオ (`GET /portfolios?workspace=&owner=me`・`GET /portfolios/{gid}`・`GET /portfolios/{gid}/items`) を読む。
- 同期では取らない。表示するとき (TUI で開いたとき、CLI・MCP で呼んだとき) にオンラインなら取り直してキャッシュに置き、表示はキャッシュから行う。オフラインではキャッシュを出す。
- Asana が 402 を返したら (プランにない)、ワークスペースごとに `plan_unavailable:goals:<gid>` / `plan_unavailable:portfolios:<gid>` に時刻を記録して、そのワークスペースでその機能を 1 日隠す (TUI のメニュー、MCP のツール)。1 日たったら再び出し、開いたときにもう一度試す。答えが返れば記録を消す。プランを上げた (お試しを始めた) ワークスペースが、だれも何も消さずに使えるようになるため。
- TUI: メニューの `g` (ゴール) と `G` (ポートフォリオ)。一覧 → `enter` で詳細 (ゴールはサブゴール・支えているプロジェクトやタスク、ポートフォリオは中身)。そこからサブゴール・ポートフォリオを開く、プロジェクトに移る、タスクを開く。`c` で今日を含む期間に絞る、`tab` でゴールとポートフォリオを切り替える。
- CLI: `goal list [--period current|期間]`・`goal show <ゴール>`・`goal periods`・`portfolio list`・`portfolio show <ポートフォリオ>` (どれも `--offline` と `--json`)。期間で絞ると、その中の期間 (年の四半期など) のゴールも含む。
- MCP: `list_goals` (`period`)・`get_goal`・`list_portfolios`・`get_portfolio` (読み取り専用)。
- マイグレーション 1 件: 表 goals・goal_supports・time_periods・portfolios・portfolio_items。ワークスペースが消えたとき・初期化のときに一緒に消す。

## やらないこと

- ゴールやポートフォリオの作成・編集 (読むだけ)。
- ワークロード (`GET /allocations`)。後回し。
- 他人がオーナーのポートフォリオの一覧 (Asana の API は自分のものしか返さない)。ただし、自分のポートフォリオに入っているものは開ける。
