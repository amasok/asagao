# ADR 0002: 仕様は OpenSpec 1.15.0 で書き、package.json で版を固定し、CI で --strict で確かめる

- 状態: 提案 (Issue #69 の設計の段階)
- 日付: 2026-11-16

## 背景

Issue #69 で、今の振る舞いを OpenSpec の仕様 (要件と場面、WHEN / THEN) に書き起こし、構成を移す PR の基準にすると決めた。OpenSpec は Node.js の CLI (`@fission-ai/openspec`) で、開発でだけ使う。アプリ (Go のバイナリ) には入れない。

2026-11-16 に npm で確かめたこと:

| 版 | 公開日 | node の要件 | 備考 |
|---|---|---|---|
| 2.0.0 | 2026-11-11 | >=22.12.0 | 最新。メジャーの更新から 5 日 |
| 1.17.0 | 2026-11-09 | — | |
| 1.16.1 | 2026-11-07 | — | |
| 1.15.0 | 2026-10-24 | >=20.19.0 | 公開から 2 週間以上たっている最新の版 |

1.15.0 と 2.0.0 の両方を一時ディレクトリで試し、次のことが同じだと確かめた。

- `openspec init --tools none` が作るのは `openspec/config.yaml`、`openspec/specs/`、`openspec/changes/archive/` だけ。
- 仕様の形: `## Purpose` (50 文字以上。短いと警告)、`## Requirements`、`### Requirement: <名前>`、`#### Scenario: <名前>` と `- **WHEN**` / `- **THEN**` / `- **AND**`。
- 要件の本文に英語の `SHALL` か `MUST` (`/\b(SHALL|MUST)\b/`) がないと警告になる。`--strict` では警告も失敗になる。日本語の本文でも `(MUST)` と書けば通る。
- 要件の本文は 500 文字を超えると警告になる。
- 使用状況の送信 (telemetry) は既定で有効。`OPENSPEC_TELEMETRY=0` か `DO_NOT_TRACK=1` で止まり、CI では自動で止まる。

## 決めたこと

1. **版は 1.15.0 に固定する。** 2.0.0 はメジャーの更新から日が浅い。npm の公開直後の版は、乗っ取りなどの問題が見つかる前に入ってしまうおそれがある。公開から 2 週間以上たった最新の版を選ぶ。使う機能 (init / validate / list / show / change) は 1.15.0 で足りる。上げるときは、別の PR で `npm install -D -E @fission-ai/openspec@<版>` を実行し、`npm run spec:validate` が通ることを確かめる。
2. **入れ方は、リポジトリ直下の `package.json` の devDependency と `package-lock.json` にする。** `npx @fission-ai/openspec@1.15.0` だけだと、本体の版は固定できても、依存 (`ora` `^9` など) の版が実行のたびに変わる。lock ファイルなら、依存も含めて版と integrity (ハッシュ) が固定される。
   - 実行は `npm run spec:validate` を使う。`npm run` は `node_modules/.bin` にあるものしか使わない。`npx openspec` は、入っていないと npm にある別のパッケージ `openspec` を取りに行くので使わない。
   - `npm ci --ignore-scripts` で入れる。依存のインストール時スクリプトは動かさない。
   - `package.json` の scripts で `OPENSPEC_TELEMETRY=0` を付ける (macOS / Linux のシェルを前提にする)。
   - `node_modules/` は `.gitignore` に入れる。
   - `go.mod` には何も足さない。
3. **AI 向けの設定は作らない (`--tools none`)。** `--tools claude` は `.claude/commands/opsx/*` と `.claude/skills/openspec-*` を 12 ファイル作る。このリポジトリには役割ごとのエージェントとスキル (`/land` `/issue` `/mutation`) がすでにあり、手順が二重になる。必要になったら別の PR で `openspec update` を使って足す。
4. **CI で `openspec validate --all --strict` を回す。** `.github/workflows/ci.yml` に `spec` ジョブを足す (ubuntu だけ。Node 22)。
   - 理由: 仕様は「レビューの決まり」(振る舞いを変える PR は仕様も直す) の基準になる。形が崩れた仕様 (場面のない要件、キーワードの抜け) が main に入ると、基準として使えなくなる。ジョブは数秒で終わる。
   - macOS では回さない。道具の動きは OS によらないため。
5. **仕様の書き方の決まり** (`openspec/config.yaml` の `context` にも書く)
   - 日本語で書く。要件の本文の最初の文の終わりに `(MUST)` か `(SHALL)` を付ける。
   - `openspec/specs/` は今の振る舞いの基準 (baseline)。要件ごとに `根拠:` の行で、コードとテストの場所を書く。
   - 確かめられないことは「未確認」と書き、要件にはしない。
   - 仕様は DDD のコンテキストごとに分ける (`docs/design/context-map.md`)。

## 結果

- 開発者は Node.js 20.19 以上が要る。Go だけでビルドとテストはできる (仕様を直すときだけ Node が要る)。
- 版を上げるのは手で行う。Dependabot などで自動にはしない (今の Go の依存も手で上げている)。
- `openspec` が作る `openspec/changes/archive/` は、変更の提案を取り込んだあとの置き場として使う。

## 選ばなかった案

- **2.0.0 を使う**: 上に書いた理由で、今は使わない。2 週間たち、問題が報告されていなければ上げてよい。
- **`tools/openspec/` の下に package.json を置く**: ルートが汚れない代わりに、コマンドが長くなる (`npm --prefix tools/openspec ...`)。openspec はカレントディレクトリの `openspec/` を探すので、ルートに置くほうが素直。
- **CI で回さない**: 上に書いた理由で、回す。
- **単体の実行ファイル (`openspec install`)**: Node が要らなくなる代わりに、ダウンロードしたバイナリの版の固定とハッシュの検証を、別に用意する必要がある。lock ファイルなら npm がそれを行う。
