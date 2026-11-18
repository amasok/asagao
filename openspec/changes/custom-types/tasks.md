# タスク

- [x] asana: `CustomTypes`、タスクの `custom_type`・`custom_type_status_option` (省ける欄)
- [x] store: マイグレーション 22、タスクの種類とステータス、プロジェクトの種類、ローカルの変更・取り消し
- [x] sync: 種類の取得と 402、送る本文、batch から外す
- [x] app: `ResolveUpdateFor` (名前の解決)
- [x] TUI / CLI / MCP、README
- [x] asana: 種類とステータスの gid を名指しして読む (`.name` だけでは gid が来ない)、store: gid の無い種類でキャッシュを消さない、マイグレーション 23
- [ ] 本物のテスト用ワークスペースで、API の形を確かめる (取り込みの前にリーダーが手配)。完了のステータスでタスクも完了になるかも確かめる
