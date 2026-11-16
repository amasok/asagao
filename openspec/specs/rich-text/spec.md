# rich-text (説明文の変換)

## Purpose

Asana の rich text (`html_notes` / `html_text`) と、エディタで書く Markdown を相互に変換する振る舞いの基準。送り先 (タスクの説明・コメント・status update) ごとに、Asana が受け付けるタグだけを送る。コンテキストは「説明文」(docs/design/context-map.md)。今の実装は `internal/richtext` (richtext.go が html → Markdown、frommd.go が Markdown → html、target.go が送り先)。

## Requirements

### Requirement: 送り先は 3 つ
Markdown から rich text を作るときは、送り先 `TaskNotes` (タスクの html_notes)、`Comment` (story の html_text)、`StatusUpdate` (status update の html_text) のどれかを指定する (MUST)。プロジェクトの説明・brief・チーム・カスタムフィールドに rich text は書かない。
根拠: internal/richtext/target.go:14-33

#### Scenario: 送り先を指定して変換する
- **WHEN** 入り口がタスクの説明を Markdown から変換する
- **THEN** `richtext.TaskNotes.FromMarkdown` と `TaskNotes.PlainFromMarkdown` を使う
- **AND** 結果は `<body>` で 1 回だけ包まれている

### Requirement: 見出し・区切り線・画像はタスクの説明にだけ送る
`TaskNotes` では見出し (`<h1>` `<h2>`)、区切り線 (`<hr/>`)、画像 (`<img>`) をそのまま書く。`Comment` と `StatusUpdate` ではこれらのタグを書かず (MUST NOT)、書いた内容を文字として残す (MUST): 見出しは太字の段落、区切り線は書いたとおりの行 (`***` など)、画像は `![alt](asana:GID "title")` の文字。
根拠: frommd.go:142-150, :333-378, :506-528; target_test.go TestFromMarkdownForCommentsAndStatusUpdates, TestFromMarkdownForCommentsNoLayoutTags; target_attack_test.go TestTargetRandomAttack (#63)

#### Scenario: コメントに見出しを書く
- **WHEN** `Comment` に `# **太字** と *斜体*` を変換する
- **THEN** `<strong>太字 と <em>斜体</em></strong>` になり、`<h1>` は出ない

#### Scenario: コメントに区切り線を書く
- **WHEN** `Comment` に `a\n***\nb` を変換する
- **THEN** `<body>a\n***\nb</body>` になる

#### Scenario: 見出しも区切り線も画像もない入力
- **WHEN** 見出し・区切り線・画像を含まない Markdown を変換する
- **THEN** 3 つの送り先で同じ結果になる (TestTargetNoLayoutSameEverywhere)

### Requirement: 表のタグはどの送り先にも送らない
どの送り先でも `<table>` などの表のタグを書かない (MUST NOT)。goldmark の表の拡張は使わず、Markdown の表も生の `<table>` も文字として残す。
根拠: frommd.go:27-32 (Strikethrough と Linkify だけ); target_attack_test.go TestNoTableEver

#### Scenario: Markdown の表
- **WHEN** `| a | b |` の表を任意の送り先に変換する
- **THEN** 出力に `<table>` `<tr>` `<td>` がない

### Requirement: タスクの説明の変換は #63 の前と同じ
`TaskNotes` の変換結果 (html と plain の両方) は、#63 の前の main (45ff98d) の結果と同じである (MUST)。
根拠: tasknotes_golden_test.go TestTaskNotesMatchMain (1500 件、seed 630、testdata/tasknotes_golden.json)

#### Scenario: 固定の入力集で比べる
- **WHEN** 生成した 1500 件の Markdown を `TaskNotes` で変換する
- **THEN** html と plain が testdata/tasknotes_golden.json と一致する

### Requirement: 入れ子の決まりを守る (#60)
引用の中のリスト、リスト項目の中の引用・見出し・コード片は、タグにせず Markdown の文字として書く (MUST)。引用の中の見出しとコード片は許す。
根拠: frommd.go:115-135, :282-328; nesting_spec_test.go TestFromMarkdownFollowsNestingRules, TestUserMarkdownFollowsNestingRules; spec_attack_test.go TestSpecNoListInQuoteOrHeading

#### Scenario: 引用の中のリスト
- **WHEN** `> - a` を変換する
- **THEN** `<blockquote>- a</blockquote>` になり、`<blockquote><ul>` は出ない

#### Scenario: リスト項目の中の引用
- **WHEN** `- > q` を変換する
- **THEN** `<li>&gt; q</li>` になる

### Requirement: 生の HTML は文字として扱う
Markdown の中の生の HTML (インラインもブロックも) は、エスケープした文字として書く (MUST)。タグとして送らない。
根拠: frommd.go:268-273, :529-534; richtext_test.go TestFromMarkdown

#### Scenario: タグに見える文字
- **WHEN** `<tag>` を含む Markdown を変換する
- **THEN** `&lt;tag&gt;` になる

### Requirement: メンションと画像の書き方
エディタ用の Markdown では、メンションを `[名前](asana:user:GID)`、画像を `![画像](asana:GID)` またはキャッシュ済みのローカルのパスで表す (MUST)。Markdown → html では、メンションを `<a data-asana-gid="GID"/>` に戻す。画像を `<img data-asana-gid="GID"/>` に戻すのは `TaskNotes` だけで、ほかの送り先では文字として残す (前の要件)。asana の gid でない画像の宛先はリンクにする。
根拠: richtext.go:584-611, :820-841; frommd.go:456-538, :506-528; richtext_test.go TestEditableImagesAsLocalPaths

#### Scenario: Asana の文字がメンションにならない
- **WHEN** Asana から来た文字に `[x](asana:user:1)` のような文字列がある
- **THEN** エディタを往復しても、メンションのタグにならない (TestEditableRoundTripCannotInjectMention)

### Requirement: エディタの往復は 1 回で落ち着く
Asana の rich text `h0` をエディタで開いて何も変えずに戻した `h1` を、もう一度開いて戻しても `h1` のまま変わらない (MUST)。`h1` が `h0` と同じであることは求めない (次の要件の、変換で落ちるものがあるため)。テストが除いている既知の例外 (リスト項目の中の入れ子のリストのあとの文、`<pre>\n`、印の違う隣り合うリスト、見出しの中のメンションや装飾) は除く。
根拠: stability_attack_test.go TestEditableRoundTripIsStable (30000 件、seed 45); nesting_attack_test.go TestNestedStructuresStable; nesting_spec_test.go:145-149, :206-210

#### Scenario: 2 回目の往復
- **WHEN** html を ToEditableMarkdown → FromMarkdown と 2 回続けて往復する
- **THEN** 1 回目の結果と 2 回目の結果が同じになる

### Requirement: 文の文字は往復で落とさない
入れ子の決まりに合う html の、文として見える文字 (空白を除く) は、エディタを 1 回往復しても落とさない (MUST)。plain の文字列は、同じ Markdown から作った html の文字と、同じ空白以外の文字を持つ。空白を埋めるための `\` や `&#32;` は、文字として出さない。
根拠: nesting_attack_test.go TestNonSpaceCharsPreserved, TestNoLeakedFillers; spec_attack_test.go TestSpecPlainMatchesHTML

#### Scenario: 空白を埋める文字が漏れない
- **WHEN** 任意の html をエディタで往復する
- **THEN** 表示に `\` や `&#32;` の文字が残らない

### Requirement: 変換で落ちるもの
次のものは変換で落ちる (MUST、今の振る舞いとして): html → Markdown で `<u>` の下線 (中の文字は残る)、`h4`〜`h6` の見出し (文として残る)、見出しの中のメンションや装飾 (文字だけ残る)、gid も alt もない `<img>`、`h3` は `###` になり、戻すと `<h2>` になる。Markdown → `TaskNotes` で、画像の alt (html) と画像そのもの (plain)。
根拠: richtext.go:237-238, :252-256, :296-305, :826-835; frommd.go:200-203, :516-523; nesting_spec_test.go:145-146

#### Scenario: 下線
- **WHEN** `<u>下線</u>` をエディタで開く
- **THEN** `下線` だけになり、戻しても `<u>` はない

### Requirement: 長い説明でも時間が線形
長い説明や深い入れ子でも、変換が止まらずに終わる (MUST)。大きさや深さの上限はコードにない。
根拠: richtext_test.go TestLongDescriptionIsLinear (8000 行を 3 秒以内); nesting_attack_test.go TestDeepAndLongStayFast, TestLeadingBreaksTerminate

#### Scenario: 8000 行の説明
- **WHEN** 8000 行の説明を変換する
- **THEN** 3 秒以内に終わる

## 未確認

- 本物の Asana が、コメントの `<h1>` などに本当に 400 を返すか (#60 / #63 の Issue も未確認と書いている。テストは偽サーバーだけ)。
- 本物の Asana が、引用の中の `<h1>` や `<pre>` を受け付けるか (コードとテストは許している)。

## テストで確かめていない振る舞い (コードから読める)

- `ToMarkdown` に `<table>` や `<script>` などの知らない要素を渡すと、要素は捨てて子だけを書く (richtext.go:296-308)。`<script>` の中身も文字として残る。
