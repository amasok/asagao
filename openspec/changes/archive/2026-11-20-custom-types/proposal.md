# カスタムタイプの種類とステータス (Issue #59)

振る舞いを変える変更。

## なぜ

Asana のカスタムタイプ (resource_subtype が `custom` のタスク) は、種類 (`custom_type`) と種類ごとのステータス (`custom_type_status_option`) を持つ。asagao は種類を `default_task`・`milestone`・`approval` の 3 つだと考えていたため、カスタムタイプのタスクは種類が `custom` と出るだけで、どの種類か、どのステータスかが分からず、変えられなかった。

## 何を変えるか

- タスクを読むとき `custom_type`・`custom_type_status_option` の gid と名前を取る (取れないワークスペースでは、ほかの省ける欄と同じく省く)。Asana は `.name` だけを頼むと gid を返さないので、`.gid` も名指しする (本物で確かめた)。キャッシュには gid を持ち、名前は `custom_types`・`custom_type_status_options` に入れる。
- プロジェクトで使える種類とステータス (`GET /custom_types?project=`) を、プロジェクトのカスタムフィールドを取るときに取る。402 ならワークスペースについて `plan_unavailable:custom_types:<workspace>` を記録し、24 時間は問い合わせず選択肢にも出さない。
- `update_task` に `custom_type`・`custom_type_status_option` (gid) を足す。種類は `resource_subtype: custom` と一緒にだけ設定でき、ステータスはそのタスクの種類のものだけ。種類だけを変えたら、その種類の最初の (完了でない) ステータスを付ける。完了のステータスにするとローカルでも完了にする。ほかの種類にすると、カスタムタイプとステータスはローカルで外し、送らない。
- 送るとき、種類かステータスがあれば `resource_subtype: custom` と種類も送る。`POST /batch` にはまとめない (402 を記録するため)。
- API が種類の設定に対応しているかは、告知と報告が食い違っていて本物で確かめていない。Asana が断ったとき (400・403・422・402) や、200 でも反映しなかったときは、種類の部分だけを理由付きで捨て (#57 の開始日と同じ形)、同じ変更のほかの欄は送る。書き込みは読み取りと別のコミットにしてあり、使えないと分かればそこだけ外せる。
- オフラインで作ったタスクの種類は、作成に混ぜず、そのあとの `update_task` で送る。
- マイグレーション 2 つ。22 番: `tasks` に 2 列、`custom_types`・`custom_type_status_options`・`project_custom_types`。23 番: `.name` だけで読んでいた版のキャッシュに種類の無いカスタムタイプのタスクがあれば、次の同期を全件にする。
- TUI: 詳細の「種類」にカスタムタイプを足し、「ステータス」の行で選ぶ。一覧に `[バグ: 対応中]`。CLI: `task type` で種類の名前、`task status`、`task show`。MCP: `update_task` の `custom_type`・`custom_type_status`、`get_task` の `custom_type`・`custom_type_status`・`custom_types`。

## やらないこと

- 種類やステータスの選択肢そのものの作成・変更 (API が対応していない)。
- 作るとき (`POST /tasks`) に種類を指定すること。本物の Asana は `POST /tasks` の `resource_subtype: custom` と `custom_type` も受け付けて種類付きで作るが、作ってから PUT する形で動くので変えない。
