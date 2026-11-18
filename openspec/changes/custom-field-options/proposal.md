# カスタムフィールドの選択肢を足す・プロジェクトに付ける (Issue #37)

振る舞いを変える変更。

## なぜ

カスタムフィールドは、プロジェクトに付いているものの値を読み書きできるだけ。選択式 (enum / multi_enum) に無い値を入れたいとき、Web に戻って選択肢を足すしかない。ワークスペースにある既存のフィールドをプロジェクトに付ける (外す) こともできない。

## 何を変えるか

- outbox の種類を 3 つ足す: `create_enum_option` (`POST /custom_fields/{gid}/enum_options`)、`add_custom_field_setting` / `remove_custom_field_setting` (`POST /projects/{gid}/addCustomFieldSetting` / `removeCustomFieldSetting`)。どれもオフラインで積める。
- 選択肢の作成はローカル gid を付け、すぐにタスクの値に使える。値の変更 (`set_custom_field`、値付きの `create_task`) は、作成が outbox にある間は送らずに待つ (`POST /batch` にもまとめない)。作成が送れたら、フィールドの選択肢・タスクの値・outbox の中のローカル gid を本物に書き換え (multi_enum は並べ直す)、`sync_state` に `enum_option:<local>` → 本物 を記録する。
- 選択肢は名前で照合する: 送る前に Asana のフィールドを読み、同じ名前の有効な選択肢 (ほかの作成の結果でないもの) があればそれを使い、作らない。答えが届かなかったときの確認も同じ探し方をする。
- 選択肢の作成を取り消すと、フィールドから消し、それだけを設定した変更は元の値に戻して消す。multi_enum のほかの選択肢は残して送る。オフラインで作ったタスクは、その値なしで作る。
- フィールドの付け外し: 付けたフィールドはすぐにプロジェクトのタスクで編集でき、その値は付ける操作が outbox にある間は待つ。送れていない逆の操作があれば、積まずにそれを消す。外すのを取り消すと、元の位置と「重要」に戻す。Asana が 400 を返しても、プロジェクトが既にその状態なら完了とする。送れたらプロジェクトのフィールドを取り直す。
- 値の書き込み形式 (#1、本物で確かめた): people と multi_enum を空にするときは null でなく `[]` を送る。date は今の `{"date": …}` のまま。
- 選択肢の作成やフィールドの付け外しが 4xx で断られても、Asana が既にその状態なら完了とする (同じ名前の選択肢がある、既に付いている・付いていない)。
- ワークスペースのカスタムフィールド (`GET /workspaces/{gid}/custom_fields`) を、定期の取り直しか、まだ取っていないときに取ってキャッシュに入れる (付けられるフィールドの一覧)。マイグレーション 1 つ: `workspace_custom_fields`。
- 402 なら、そのワークスペースについて `plan_unavailable:custom_fields:<workspace>` (時刻) を記録し、24 時間はどちらの機能も出さない (そのあと試し直す。書き込みが通れば印を消す)。ほかのワークスペースでは隠さない。403 なら、選択肢はそのフィールド (`field_options_locked:<field>`)、付け外しはそのプロジェクト (`project_fields_locked:<project>`) について記録し、出さない。操作は理由付きで失敗にする。
- TUI: 選択式の値の選択肢に「＋ 選択肢を足して設定する…」を出し、名前を打つと足して設定する。スペース G でプロジェクトのフィールドを付け外す (外すのは確認あり)。
- CLI: `task field … --add-option` (名前が完全に一致しなければ足す)。`project fields <プロジェクト> [--all] [--add …] [--remove … --yes]`。
- MCP: `update_task` の `add_missing_options`、`get_project` の `custom_fields` と `include_available_fields`、`update_project_custom_fields`。`get_task` のフィールドに `can_add_option`。

## やらないこと

- カスタムフィールドの新規作成・名前や種類の変更 (`POST /custom_fields`・`PUT /custom_fields/{gid}`) はしない (要望があれば)。
- 選択肢の名前・色の変更、無効化、並べ替えはしない。足す選択肢の色は Asana に任せる。
- フィールドを外したとき、タスクの値をローカルで消すことはしない (次の取り込みで Asana の状態になる)。
- 「重要」(is_important) の切り替えはしない。
