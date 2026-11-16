# asana-tui の設計 (Issue #69)

DDD の考え方で、ドメイン・アプリケーション層・リポジトリ・外部依存の境界を段階的に整えるための設計の文書。ここに書くのは**構成** (どこに置くか、何に依存するか)。**振る舞い** (何をするか) は OpenSpec の仕様 `openspec/specs/` にある。

## 読む順番

1. [用語集](glossary.md) — コード・仕様・Issue で使う言葉
2. [コンテキストマップ](context-map.md) — まとまりの境界と、翻訳する場所
3. [書き込みの流れ](write-flow.md) — 入力 → ローカルに反映 → outbox → push → 照合 → 確定
4. [集約と不変条件](aggregates.md) — 1 つのトランザクションで守る決まり
5. [目標の構成と依存の向き](architecture.md) — 層、パッケージの置き場所、今との違い
6. [依存の向きを守らせる方法](dependency-rules.md) — go list を使うテストの提案
7. [移行の順番](migration-plan.md) — 段階 a〜d の PR の範囲
8. [ADR](adr/) — 判断ごとの記録

| ADR | 判断 |
|---|---|
| [0001](adr/0001-record-design-decisions.md) | 設計の判断は docs/design/adr に 1 件 1 ファイルで残す |
| [0002](adr/0002-openspec-tooling.md) | 仕様は OpenSpec 1.15.0 で書き、package.json で版を固定し、CI で --strict で確かめる |
| [0003](adr/0003-layers-and-dependency-direction.md) | 入り口・アプリ・ドメイン・adapter の 4 層にし、パッケージの名前は今のものを残す |
| [0004](adr/0004-external-libraries-in-adapters.md) | 外部ライブラリは adapter と入り口にだけ置き、ドメインとアプリは標準ライブラリだけにする |
| [0005](adr/0005-conform-to-asana-model.md) | キャッシュは Asana の型に順応し、型だけを asana/model に、表示用の型を readmodel に分ける |
| [0006](adr/0006-application-use-cases.md) | ユースケースはローカルへの反映と outbox までを行い、送り方は入り口から Sender で受ける |
| [0007](adr/0007-migration-order-and-dependency-checks.md) | アプリ層 → ドメイン → リポジトリ → ポートの順に移し、依存の向きは go list のテストで守る |

## 仕様 (OpenSpec)

| 仕様 | コンテキスト |
|---|---|
| [sync-pull](../../openspec/specs/sync-pull/spec.md) | 同期: 取り込み |
| [outbox-push](../../openspec/specs/outbox-push/spec.md) | 送信待ちと同期: 積む・送る・照合 |
| [rich-text](../../openspec/specs/rich-text/spec.md) | 説明文 |
| [auth-token](../../openspec/specs/auth-token/spec.md) | 認証 |
| [store-lifecycle](../../openspec/specs/store-lifecycle/spec.md) | ローカルキャッシュ |
| [write-flow](../../openspec/specs/write-flow/spec.md) | タスクの操作と入り口 |

### 使い方

```sh
npm ci --ignore-scripts      # 初めてのとき (Node.js 20.19 以上)
npm run spec:validate        # openspec validate --all --strict
npm run spec:list            # 仕様の一覧
```

- 仕様は日本語で書く。要件の本文には `(MUST)` か `(SHALL)` を入れる (`--strict` が英語のキーワードを探すため)。
- 要件ごとに `根拠:` の行で、コードとテストの場所を書く。確かめられないことは、要件にせず「未確認」の節に書く。
- 振る舞いを変える PR は、`openspec/changes/<名前>/` に変更の提案 (`proposal.md` と、仕様の差分) を置く。構成を移すだけの PR (段階 a〜d) は、`openspec/specs/` を変えない。

## レビューの決まり (Issue #69 の追記から)

- レビュー役とテスト担当には、Issue と一緒に、関係する仕様を渡す。
- 振る舞いを変える PR は、仕様も直す。構成を移す PR は、`git diff main -- openspec/specs` が空であること。
- 構成を移す PR は、関係する ADR と、`migration-plan.md` のどの PR かを説明に書く。
