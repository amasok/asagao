# ADR 0005: キャッシュは Asana の型に順応し、型だけを asana/model に、表示用の型を readmodel に分ける

- 状態: 提案 (Issue #69 の設計の段階)
- 日付: 2026-11-16

## 背景

- `internal/asana` の型 (`asana.Task`、`asana.Ref`、`asana.Project`、`asana.Story` ほか) は、Asana の JSON をそのまま写したもので、キャッシュ (store) と同期 (sync) と認証 (auth) で使われている。store は 9 つのファイルで `internal/asana` を import している。入り口で `internal/asana` を import するのは cli だけ (`asana.NewClient` の組み立て)。
- そのため、キャッシュ (adapter) が、HTTP のクライアントを含むパッケージ (別の adapter) に依存している。
- DDD を厳密に当てはめると、ドメインの自前の `Task` 型を作り、Asana の型との写し替えを腐敗防止層に置く形になる。

## 決めたこと

1. **キャッシュと同期が扱う型は、Asana の型に順応 (Conformist) する。** 自前の `Task` 型は作らない。
   - asana-tui は Asana のクライアントで、持ち主は Asana。表示する欄は Asana の欄そのもので、写し替えても情報は増えない。
   - 写し替えると、欄を足すたびに 3 か所 (Asana の型、自前の型、写し替え) を直すことになる。#28〜#36 の機能は欄を足すものが多い。
2. **型だけを `internal/asana/model` に分ける (段階 d1)。** 標準ライブラリだけを使う、JSON のタグ付きの型のパッケージにする。HTTP のクライアント (`asana.Client`) は `internal/asana` に残す。store・sync・auth は `asana/model` を import し、`internal/asana` は import しない (組み立てを除く)。入り口は今も `internal/asana` を import していない (cli の `asana.NewClient` を除く。これは段階 d4 で組み立てへ移す)。
   - 401 などの判定に使う `asana.Error` も、Asana の応答の型として `asana/model` に入れる。
3. **書き込みの側 (outbox の操作、畳み込み、再送してよいか) は、自前の型と関数にする (段階 b)。** ここが asana-tui の決まりで、Asana の型には現れないため。
4. **入り口が読む表示用の型は、Asana の型ではなく、今も store が定義している型** (`store.TaskSummary`・`store.TaskDetail` など、tui だけで約 50 種類) である。これは順応ではなく、store が作る読み取りのモデル。段階 c2 で、標準ライブラリだけの `internal/readmodel` に移す (architecture.md の「読み取りの扱い」)。
5. 移すときは、`internal/asana` に型の別名 (`type Task = model.Task`) をしばらく残し、使う側を何回かの PR で移す。最後の PR で別名を消す。

## 結果

- キャッシュの表の形と、Asana の JSON の形が近いままになる。Asana が欄の意味を変えたら、キャッシュも表示も影響を受ける。これは受け入れる (今も同じ)。
- 依存の向きのテストで、「store → asana」は禁止、「store → asana/model」は許可にできる。

## 選ばなかった案

- **自前のドメインの型を作る**: 上の 1 の理由で、今は見合わない。Asana の型に振り回される問題が実際に起きたら (例: Asana が欄の形を変え、キャッシュの移行が要る)、そのときにキャッシュの型だけを新しい ADR で見直す。
- **今のまま (store が internal/asana を import する)**: adapter 同士の依存が残り、依存の向きのテストに例外を残し続けることになる。
