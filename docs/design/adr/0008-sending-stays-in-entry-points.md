# ADR 0008: 送り方はポートにせず入り口に残し、TUI は解決済みの gid を受ける CreateHere で作成する

- 状態: 提案 (Issue #69 の段階 a3)
- 日付: 2026-11-17
- ADR 0006 の決めたことの 2 (`Sender` ポート) と、ADR 0003 の決めたことの 2 のうち「送り方」をポートで受けること、結果の「送り方は `Sender` ポートで受ける」を置き換える

## 背景

- ADR 0006 の 2 は、送り方を `type Sender interface { Send(ctx, gid string) WriteResult }` のようなポートで受け、形は段階 a3 (TUI を移すとき) に決める、としていた。
- 今の送り方 (write-flow の仕様):
  - CLI (`reportWrite`) と MCP (`s.push`) は、書き込みのあと入り口ですぐ `Syncer.Push` を 20 秒の期限で呼び、送れたかを自分の形 (`sent`・`task_gid`) で返す。
  - TUI は、書き込みが終わった知らせ (`writeDoneMsg`) を画面の更新の中で受け、ほかの約 20 の書き込みと同じ `afterWrite` から同期を頼む (`requestSync`)。同期中なら、終わってからもう一度行う (`syncing`・`syncAgain`)。この状態は画面 (`Model`) にあり、画面の更新のゴルーチンだけが触る。
- TUI のタスク作成は、名前を解決しない。今見ているプロジェクトとセクションの gid、解釈済みの期日を、そのまま `CreateTaskLocal` に渡す。My Tasks では既定のワークスペースと自分 (`MeGID`) を使い、エラーはそのまま画面に出す。ADR 0006 の 3 は、このための「名前の解決をしない入力の形を用意する」としていた。

## 決めたこと

1. **送り方のポート (`Sender`) は作らない。送るのは入り口のまま。** CLI・MCP・TUI は、ユースケースから gid を受け取ったあと、今と同じ方法で送る。
2. **TUI の作成は `app.Tasks.CreateHere(ctx, CreateHereInput)` で行う。** 入力は gid (`Parent`・`Project`・`Section`) と解釈済みの期日 (`Due`) で、名前を解決しない。
   - 呼ぶ store の読み書きとその順番は、今の TUI と同じにする: サブタスクは `CreateTaskLocal` だけ、プロジェクトはセクションがなければ `FirstSection`、それ以外は `DefaultWorkspace` → 自分 → `CreateTaskLocal`。
   - 自分は `ResolveUser(ctx, "me")` で取る (store では `MeGID` をそのまま返す)。`TaskStore` に `MeGID` は足さない。
   - エラーは store のものをそのまま返す (`*FieldError` にしない。利用者が打った名前がないため)。タグと説明文は扱わない (TUI の作成の入力にないため)。
   - 時刻は `Clock` で受ける。TUI は、キーを押したときの `Now()` を返す `Clock` を渡す (今と同じ時点)。

## 理由

- ユースケースは送らない (ADR 0006 の 1)。app に `Sender` を使う側がない。Go の習慣ではインターフェースは使う側に置く (ADR 0003 の 2) ので、使う側のないポートは作らない。
- TUI の送り方は、書き込みごとの呼び出しではなく、画面の状態を使う同期の依頼。`Send(ctx, gid)` に包むと、`syncing`・`syncAgain` を画面の外に移すか、別のゴルーチンから触ることになり、振る舞いが変わるおそれがある。
- `Tasks.Create` (CLI・MCP 用) に gid を渡すと、`ResolveProject`・`ResolveSection` を通り、gid と同じ文字列の名前を持つ別のプロジェクトに当たる、キャッシュにないセクションの gid で見つからないエラーになる、自分の情報がないときのエラーが `assignee: ` で包まれる、など、TUI の振る舞いが変わる。

## 結果

- 入り口は「入力を集める → ユースケースを呼ぶ → 自分の方法で送る → 表示する」になる。`internal/app` は `internal/sync` を import しない。sync を呼ぶのは入り口 (CLI の `reportWrite`、MCP の `s.push`、TUI の同期) のまま。
- 作成のユースケースは、名前で受ける `Create` と gid で受ける `CreateHere` の 2 つになる。名前の解決をしない入力が要る書き込み (a4 以降の TUI) は、同じ形を足す。
- 3 つの入り口の送り方を組み立てから差し替えたくなったら (段階 d4 の `internal/wire`)、そのときに使う側 (入り口) に型を置く。

## 選ばなかった案

- **`Sender` ポートを app に置き、TUI には同期を頼む実装を渡す**: 使う側がなく、TUI の同期の状態を移すことになる。
- **`CreateTaskInput` に gid の欄 (`ProjectGID` など) を足す**: 名前と gid の両方が入ったときの決まりが要り、`Create` の分かれ方が増える。担当の既定やエラーの包み方も TUI と CLI・MCP で違うので、1 つの関数に入れると条件が混ざる。
- **`TaskStore` に `MeGID` を足す**: 名前ははっきりするが、a1 の偽の store (既存のテスト) も直すことになる。`ResolveUser("me")` は store で `MeGID` をそのまま返すので、振る舞いは同じ。
