# テンプレートからタスク・プロジェクトを作る、プロジェクトの複製 (Issue #2)

振る舞いを変える変更 (足すだけ)。

## なぜ

Asana のタスクテンプレート・プロジェクトテンプレートを asagao から使えず、決まった形のタスクやプロジェクトを作るたびに Web を開く必要があった。テンプレートが使えないプランでは、既存のプロジェクトを複製する手段もなかった。

## 何を変えるか

- asana: `GET /task_templates`・`GET /project_templates`・`POST /task_templates/{gid}/instantiateTask`・`POST /project_templates/{gid}/instantiateProject`・`POST /projects/{gid}/duplicate`・`GET /jobs/{gid}`。
- sync: 一覧 (`TaskTemplates`・`ProjectTemplates`)、作成 (`InstantiateTask`・`InstantiateProject`・`DuplicateProject`)。ジョブは最大 1 分待ち、作られたものをキャッシュに取り込む。終わらなければ作られた分を取り込み、`pending` として返す (残りは次の同期)。
- 作成はオンラインだけ (outbox に積まない)。理由: Asana が非同期のジョブでサブタスク・セクション・タスク・日付を作るので、ローカルの仮のものでは代わりにならない。作ってすぐその中で作業したい。プロジェクトの作成と同じ扱い。
- 送ったか分からない失敗 (5xx・接続切れ) は、プロジェクトの作成と同じく Asana の一覧で照合する。タスクは、テンプレートのプロジェクトで同じ名前の、送った後に作られたタスク。名前を省いたときもテンプレートの名前を送り、照合できるようにする。見つからなければ作り直さない (二重を避ける)。
- 402: そのワークスペースのその機能 (`plan_unavailable:task_templates:<ワークスペースの gid>` / `project_templates:…`) に印を付け、TUI のメニューから消す。ほかの機能と違い、1 日たったら印を無視してまた出す (お試し・プランの変更で変わるため)。一覧が取れたら印を消す。
- TUI: メニューの `G` (テンプレートからタスク)・`B` (テンプレートからプロジェクト)・`S` (プロジェクトを複製)。テンプレートの一覧はキャッシュせず、メニューを開いたときと同期のあとに Asana から取る (10 分に 1 回まで)。一覧が空なら `G`・`B` を出さない。
- CLI: `template tasks`・`template projects`・`template new-task`・`template new-project`・`project duplicate` (`--dry-run` 対応)。
- MCP: `list_task_templates`・`list_project_templates` (読み取り。`--read-only` でも使える)、`create_task_from_template`・`create_project_from_template`・`duplicate_project` (`--read-only` では無効)。
- マイグレーションなし。

## やらないこと

- テンプレートのキャッシュ (オフラインでの一覧)。
- テンプレートの作成・編集・削除 (`POST /projects/{gid}/saveAsTemplate` など)。
- プロジェクトの複製で、日付をずらす (`schedule_dates`) 指定。
- TUI での役割 (requested_roles) と日付の指定 (日付は今日。指定は CLI・MCP)。
