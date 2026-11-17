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

### Requirement: タスクの説明の変換は固定の入力集の結果と同じ
`TaskNotes` の変換結果 (html と plain の両方) は、testdata/tasknotes_golden.json と同じである (MUST)。この結果は #63 の前の main (45ff98d) で作り、変換を直す Issue でだけ作り直した (#72、#64、#66、#59、#80、#12、#13。#65 と #61 では変わらなかった)。作り直すときは `RICHTEXT_UPDATE_GOLDEN=1` でテストを回し、変わった入力を 1 件ずつ確かめる。
根拠: tasknotes_golden_test.go TestTaskNotesMatchMain (1500 件、seed 630、testdata/tasknotes_golden.json)

#### Scenario: 固定の入力集で比べる
- **WHEN** 生成した 1500 件の Markdown を `TaskNotes` で変換する
- **THEN** html と plain が testdata/tasknotes_golden.json と一致する

### Requirement: 入れ子の決まりを守る (#60)
引用の中のリスト、リスト項目の中の引用・見出し・コード片・区切り線は、タグにせず Markdown の文字として書く (MUST。区切り線は #80: 文書に項目の中の `<hr/>` の記載がなく、エディタはリストの外に書き戻すため)。引用の中の見出しとコード片は許す。リスト項目の中の空の見出しは、印だけ (`#`) を書き、あとに空白を付けない (#72)。
根拠: frommd.go:115-135, :282-328; nesting_spec_test.go TestFromMarkdownFollowsNestingRules, TestUserMarkdownFollowsNestingRules; spec_attack_test.go TestSpecNoListInQuoteOrHeading

#### Scenario: 引用の中のリスト
- **WHEN** `> - a` を変換する
- **THEN** `<blockquote>- a</blockquote>` になり、`<blockquote><ul>` は出ない

#### Scenario: リスト項目の中の引用
- **WHEN** `- > q` を変換する
- **THEN** `<li>&gt; q</li>` になる

### Requirement: 文字にした入れ子は打ったとおりに残す
入れ子の決まりで文字にした Markdown は、打ったとおりの文字にする (MUST): 番号付きリストの番号 (`1.` を続けたら `1.` のまま)、見出しの `#` と閉じの `#`、setext の見出しの線 (リスト項目の `a` の下の `---` は `a` と `---` のまま)、区切り線の書き方 (`***` など)。参照リンクの定義 (`[ref]: url`) は何も書かず、その前に改行も足さない (#66)。
根拠: frommd.go headingText, markdownText, ruleText; nesting_spec_test.go TestFromMarkdownFollowsNestingRules

#### Scenario: 引用の中で同じ番号を続ける
- **WHEN** `> 1. a\n> 1. b` を変換する
- **THEN** `<blockquote>1. a\n1. b</blockquote>` になる

#### Scenario: リスト項目の中の setext の線
- **WHEN** `- a\n  ---` を変換する
- **THEN** `<li>a\n---</li>` になり、`## a` にはならない

### Requirement: 文字にした入れ子は、もう一度変換しない
文字にしたブロックの文字は、エディタで読み直したときの形 (リンクなど) に変えずに送る (MUST NOT 変える)。文字の中の生の HTML にある URL は、リンクにしない。利用者はリンクを打っていないため (#66)。
根拠: frommd.go fromMarkdown; nesting_spec_test.go TestFromMarkdownDemotedTextAsTyped

#### Scenario: 項目の中の引用の生の HTML の URL
- **WHEN** `- > <div>see https://x.jp/a_b</div>` を変換する
- **THEN** `<li>&gt; &lt;div&gt;see https://x.jp/a_b&lt;/div&gt;</li>` になり、`<a>` は出ない

### Requirement: 生の HTML は文字として扱う
Markdown の中の生の HTML (インラインもブロックも) は、エスケープした文字として書く (MUST)。タグとして送らない。ブロックの HTML は、最後の行の改行を付けない (MUST、段落と同じく次のブロックとの区切りだけにする。引用やリスト項目の中で往復すると消えるため、#72)。
根拠: frommd.go block の HTMLBlock、inline の RawHTML; richtext_test.go TestFromMarkdown; blocks_test.go TestFromMarkdownHTMLBlockEnd

#### Scenario: タグに見える文字
- **WHEN** `<tag>` を含む Markdown を変換する
- **THEN** `&lt;tag&gt;` になる

#### Scenario: 引用の中の HTML のブロック
- **WHEN** `> <div>x</div>` を変換する
- **THEN** `<blockquote>&lt;div&gt;x&lt;/div&gt;</blockquote>` になり、`</blockquote>` の前に改行はない

### Requirement: コードのブロックは行をそのまま残す
コードのブロック (`<pre>`) の行は、エディタの往復でまとめたり落としたりしない (MUST): 続く空行、空白だけの行、先頭の改行も残す。Asana の rich text は XML なので `<pre>\nx` の中身は `\nx` であり、HTML として読むと落ちる先頭の改行を補って読む (#65)。
根拠: richtext.go preStartBreak, codeBlock, tidy; frommd.go codeText; blocks_test.go TestCodeBlockKeepsLines

#### Scenario: コードの空行と先頭の改行
- **WHEN** `<pre>\na\n\n\nb</pre>` をエディタで往復する
- **THEN** `<pre>\na\n\n\nb</pre>` のまま変わらない

### Requirement: コードのブロックの言語名を送る
フェンスの情報文字列の最初の語 (```` ```go ```` の `go`) を `<pre data-language="go">` で送り、エディタで開くとフェンスに戻す (MUST)。Asana の rich text の文書に "You can specify a language with data-language" とある。言語名は英数字と `+#.-_` だけのときに使い、それ以外は捨てる (フェンスに書き戻せないため)。リスト項目の中のコード (#60 で文字にする) には付けない (#65)。
根拠: richtext.go codeLanguage; frommd.go block の FencedCodeBlock; blocks_test.go TestCodeBlockLanguage

#### Scenario: 言語名
- **WHEN** ```` ```go\nx\n``` ```` を変換する
- **THEN** `<pre data-language="go">x</pre>` になり、エディタで開くと ```` ```go ```` に戻る

### Requirement: 改行をまたぐインラインのコード
改行をまたぐインラインのコードは、行ごとのコードと、そのあいだの改行の文字にする (MUST)。コードの中の改行は Markdown では空白になり、空行は段落を終えるため。空行と空白だけの行はコードにせず文字にし、Markdown のコードの最初と最後の空行は落とす (改行だけのコードは空白 1 つのコード) (#65)。
根拠: richtext.go splitCodeLines; frommd.go inline の CodeSpan; blocks_test.go TestInlineCodeOverLineBreak, TestInlineCodeBlankLines

#### Scenario: 改行をまたぐインラインのコード
- **WHEN** `` `x\ny` `` を変換する
- **THEN** `<code>x</code>\n<code>y</code>` になり、往復しても変わらない

### Requirement: 改行は LF にそろえる
Markdown の CRLF と CR は、変換の前に LF にそろえる (MUST)。コードのブロックや生の HTML にも `\r` を残さない (#72)。
根拠: frommd.go normalizeNewlines; blocks_test.go TestFromMarkdownCRLF

#### Scenario: CRLF のコード
- **WHEN** ```` ```\r\na\r\nb\r\n``` ```` を変換する
- **THEN** `<pre>a\nb</pre>` になる

### Requirement: 書式なしの文字でブロックは行を分ける
`PlainFromMarkdown` では、引用・コードのブロック・見出し・リスト項目のあとで行を分ける (MUST)。前後のブロックの文字をつなげない (#72)。
根拠: frommd.go block (w.plain のとき); blocks_test.go TestPlainFromMarkdownQuoteEndsLine

#### Scenario: 引用のあとのコード
- **WHEN** `> q\n\n```\ncode\n```` の plain を作る
- **THEN** `q\ncode` になる

### Requirement: メンションと画像の書き方
エディタ用の Markdown では、メンションを `[名前](asana:user:GID)`、画像を `![画像](asana:GID)` またはキャッシュ済みのローカルのパスで表す (MUST)。Markdown → html では、メンションを `<a data-asana-gid="GID"/>` に戻す。画像を `<img data-asana-gid="GID"/>` に戻すのは `TaskNotes` だけで、ほかの送り先では文字として残す (前の要件)。asana の gid でない画像の宛先はリンクにする。
根拠: richtext.go:584-611, :820-841; frommd.go:456-538, :506-528; richtext_test.go TestEditableImagesAsLocalPaths

#### Scenario: Asana の文字がメンションにならない
- **WHEN** Asana から来た文字に `[x](asana:user:1)` のような文字列がある
- **THEN** エディタを往復しても、メンションのタグにならない (TestEditableRoundTripCannotInjectMention)

### Requirement: エディタの往復は 1 回で落ち着く
Asana の rich text `h0` をエディタで開いて何も変えずに戻した `h1` を、もう一度開いて戻しても `h1` のまま変わらない (MUST)。`h1` が `h0` と同じであることは求めない (次の要件の、変換で落ちるものがあるため)。次の要件の、既知の例外は除く。
根拠: stability_attack_test.go TestEditableRoundTripIsStable (30000 件、seed 45); nesting_attack_test.go TestNestedStructuresStable; nesting_spec_test.go TestUserMarkdownFollowsNestingRules (除く形は unstableOnMain、除いた件数をログに出す); target_attack_test.go TestTargetRandomAttack

#### Scenario: 2 回目の往復
- **WHEN** html を ToEditableMarkdown → FromMarkdown と 2 回続けて往復する
- **THEN** 1 回目の結果と 2 回目の結果が同じになる

### Requirement: 隣り合うリストは分けたまま
同じ種類のリストが隣り合っていても、1 つにまとめない (MUST)。Asana の rich text の文書は隣り合うリストについて何も書いておらず、まとめると書かれたものが変わるため。エディタ用の Markdown では、直前が同じ種類のリストなら印を替える (`-` のあとは `*`、`1.` のあとは `1)`)。Markdown は印の違うリストを別のリストとして読む。あいだの空白・コメント・`<br>` は落とす (#61)。
根拠: richtext.go altMarker, listItem; lists_test.go TestAdjacentListsStayApart

#### Scenario: 隣り合う箇条書き
- **WHEN** `<ul><li>a</li></ul><ul><li>b</li></ul>` をエディタで開く
- **THEN** `- a\n\n* b` になり、戻すと 2 つのリストのまま

### Requirement: リンクの文字の端の空白はリンクの外に出す
リンクの文字の端の空白と改行は、リンクの外の文字にする (MUST)。リンクの文字は端を切り詰めて書くので、そのままだと前後の語がくっつくため (#61)。表示用 (ToMarkdown) でも同じで、ブロックの末尾に来た空白は文字として残る。
根拠: richtext.go moveEdgeSpaceOutOfMarks; lists_test.go TestLinkEdgeSpaces

#### Scenario: リンクの端の空白
- **WHEN** `a<a href="http://x.jp"> b c </a>d` をエディタで往復する
- **THEN** `a <a href="http://x.jp">b c</a> d` になる

### Requirement: 字下げした setext の見出しは、字下げを除いて送る
下に setext の見出しの線 (`---`・`===`) が続く段落の行は、字下げを文字にせず除いて送る (MUST)。Markdown は見出しの文字の字下げを落とし、エディタも見出しを字下げなしで書くため、残すと往復の 2 回目で変わる (#12)。4 つ以上の字下げもコードにせず、見出しの行にする。線が続かない段落の字下げは、これまでどおり文字にする。段落のあとでリストを始められない項目 (`2.`、中身のない `-`) は段落の続き、印の少ない行は引用の段落の続き (lazy) として見る。見出しの文字の端にある空白 (文字参照で打ったもの、`## &#32;a`) も、送るときに除く (#24)。
根拠: frommd.go keepIndentation, setextUnderline, interruptsParagraph; setext_indent_test.go TestIndentedSetextHeading

#### Scenario: 字下げした setext の見出し
- **WHEN** `   a\n  ---` を変換して、エディタで往復する
- **THEN** 1 回目から `<h2>a</h2>` になり、2 回目も変わらない

#### Scenario: 線の続かない字下げした段落
- **WHEN** `  a\nb` を変換する
- **THEN** `<body>  a\nb</body>` になる (字下げは文字のまま)

### Requirement: 入れ子のリストのあとの文は、そのリストの最後の項目に入れる
リスト項目の中で入れ子のリストのあとに続く文は、そのリストの最後の項目の新しい行にする (MUST)。Markdown はその行を最後の項目の続きと読むので、エディタはそこにしか書き戻せないため。`FromMarkdown` も送るときにそうし、html にあるときも読むときにそうする (間の改行は落とし、語はくっつけない)。そのあとに続くリストは、隣り合うリストになる (#59)。空行と字下げで打った外側の項目の続き (`- a\n  - b\n\n  c`) も、Markdown がそう読むので、この文にする (#13、次の要件)。
根拠: frommd.go itemBlocks; richtext.go moveTextAfterNestedLists; lists_test.go TestTextAfterNestedList

#### Scenario: 入れ子のリストのあとの文
- **WHEN** `<ul><li>a<ul><li>b</li></ul>c</li></ul>` をエディタで往復する
- **THEN** `<ul><li>a<ul><li>b\nc</li></ul></li></ul>` になる

### Requirement: 空行のあとでも、項目の内容の桁まで字下げした行は項目の続き
空行のあとの行も、開いているリスト項目の内容の桁 (印と、そのあとの空白のあと) まで字下げしてあれば、その項目の続きとして読む (MUST、CommonMark と同じ)。いちばん深い、届いている項目の続きになり、届かない項目は終わる。どの項目にも届かない行の字下げは、これまでどおり文字にする (#13)。エディタは、リストのすぐあとの行の字下げを文字参照 (`&#32;`・`&#9;`) で書き、項目の続きと読まれないようにする (MUST)。
根拠: frommd.go keepIndentation, itemContentColumn; richtext.go lineAfterList; list_continuation_test.go TestIndentedLineAfterBlankLineInItem

#### Scenario: 空行のあとの外側の項目の続き
- **WHEN** `- a\n  - b\n\n  c` を変換する
- **THEN** `c` はリストの中に入る (`<ul><li>a<ul><li>b\nc</li></ul></li></ul>`)

#### Scenario: リストのあとの字下げした文
- **WHEN** `<ul><li>x</li></ul>  y` をエディタで開く
- **THEN** `- x\n\n&#32; y` になり、戻すと同じ html になる

### Requirement: リスト項目の中のブロックは、読むときに文字にする
Asana が受け付けないリスト項目の中の引用・コード片・見出し・区切り線が html にあるとき (Asana は送ってこないが、キャッシュやほかの道具が持つことがある) は、読むときに、`FromMarkdown` が送るのと同じ文字にする (MUST): `> ` の行 (中のリストは `- ` の行)、行ごとのコード、`# h`、`---`。ブロックとして書くとリストが終わり、あとの項目が文字になるため。引用を文字にできるか (段落などを含まないか) は書き換える前に確かめ、できないものは何も変えずにリストのあとに出す (#59)。
根拠: richtext.go demoteBlocksInItems, demotedBlock, asText, listText; lists_test.go TestTextAfterNestedList, TestRuleInItemIsText, TestQuoteInItemLeftAsIs

#### Scenario: 項目の中の区切り線
- **WHEN** `<ol><li>a<hr/></li><li>b</li></ol>` をエディタで往復する
- **THEN** `<ol><li>a\n---</li><li>b</li></ol>` になる

### Requirement: 強調は隣の文字によらず残す (#58)
html の太字・斜体・取り消し線は、句読点・リンク・画像・コード・ほかの強調・メールアドレス・URL と隣り合っても、エディタを往復したあと同じ文字に同じ装飾が付いている (MUST)。Markdown の区切り (`**`・`*`・`~~`、コードの `` ` ``) がそのままでは開かない・閉じない所、隣の区切りとつながる所、Linkify にメールアドレスや URL の一部として読まれる所では、区切りの外側に `\ ` (エスケープした空白) を書く。Markdown → rich text では、区切りのすぐ隣の `\ ` を何も書かないものとして読む (goldmark の CJK 拡張と同じ)。ほかの所の `\ ` は文字にする。
根拠: marks.go separateTokens; frommd.go escapedSpaceParser; marks_test.go; stability_attack_test.go genInlineRT

#### Scenario: 利用者が打った `\ `
- **WHEN** `cd My\ Documents` を変換する
- **THEN** `\ ` は打ったとおりに残り、`cd My\ Documents` になる (TestEscapedSpaceNextToDelimitersOnly)

#### Scenario: 表示用の Markdown
- **WHEN** `これは<strong>「重要」</strong>です` を表示用に変換する
- **THEN** `\ ` の代わりに空白を書き、`これは **「重要」** です` になる

#### Scenario: かぎかっこを太字にする
- **WHEN** `これは<strong>「重要」</strong>です` をエディタで開く
- **THEN** `これは\ **「重要」**\ です` になり、戻すと `これは<strong>「重要」</strong>です` になる

#### Scenario: 太字のあとのメールアドレス
- **WHEN** `z<strong>a</strong>b@x.jp` をエディタで往復する
- **THEN** `**` はリンクに入らず、`z<strong>a</strong><a href="mailto:b@x.jp">b@x.jp</a>` になる (`**` のすぐあとのアドレスと同じ)

### Requirement: 強調を Markdown で書ける形にする (#58)
html → Markdown の前に、強調を Markdown で書ける形にする (MUST)。見た目は変えない。同じ強調の中の同じ強調は外の 1 つにする (`<em><em>a</em></em>` が太字にならない)。空行をまたぐ強調は、段落ごとの強調に分ける (空行で段落が終わり、強調も終わるため)。`~` の直後の `~~` は開かない・閉じないので、その `~` を `&#126;` と書く。表示用にメンションを太字にするとき、太字の中では重ねない。
根拠: marks.go normalizeMarks, separateTokens; richtext.go link; marks_test.go TestEmphasisBoundaries, TestEmphasisBoundariesForDisplay

#### Scenario: 空行をまたぐ太字
- **WHEN** `<strong>a\n\nb</strong>` をエディタで往復する
- **THEN** `<strong>a</strong>\n\n<strong>b</strong>` になる

### Requirement: 空白のあとと同じ所で Linkify がリンクにする (#77)
字下げや行末の空白を書いた文字参照 (`&#32;`・`&#9;`) のすぐあとのメールアドレスや URL は、空白のあとと同じくリンクにする (MUST)。インラインのコードの直前が語の文字でも、コードの中のメールアドレスはリンクにしない。URL のすぐあとの文字参照・取り消し線・コード・リンクは、URL に含めない (`\ ` で区切る)。
根拠: frommd.go spaceRefParser; richtext.go keepTrailingSpace; marks.go separateTokens; marks_test.go TestLinkifyAsWithoutEscapes, TestEmphasisNextToEmailNotLinked; nesting_spec_test.go TestFromMarkdownDemotedTextAsTyped

#### Scenario: 字下げのあとのメールアドレス
- **WHEN** `> - a\n> - &#32;b@x.jp` を変換する
- **THEN** `b@x.jp` は `mailto:` のリンクになる

#### Scenario: 文字のすぐあとのコードのメールアドレス
- **WHEN** `z<code>a@x.jp</code>` をエディタで往復する
- **THEN** `z<code>a@x.jp</code>` のままになる

### Requirement: エスケープを含む URL は文字のまま残す (#77)
Linkify が読む URL (エスケープしない文字で読んだときの範囲) がエスケープする文字を含むときは、リンクにせず文字のまま残す (MUST)。スキームの `:` か `www` の `.` もエスケープする (`\` の手前で切れたリンクになっていた)。含まないときは、html の URL の文字は、エスケープなしの Markdown として読んだときのリンクになる (末尾の句読点は外れる)。
根拠: richtext.go escapeInline, linkifyURL; marks_test.go TestLinkifyAsWithoutEscapes, TestLinkifyURLAsGoldmark (goldmark と同じ範囲か)

#### Scenario: エスケープを含む URL
- **WHEN** `see https://x.jp/a_b` をエディタで往復する
- **THEN** `see https\://x.jp/a\_b` になり、戻すと `see https://x.jp/a_b` の文字のままになる

### Requirement: 文の文字は往復で落とさない
入れ子の決まりに合う html の、文として見える文字 (空白を除く) は、エディタを 1 回往復しても落とさない (MUST)。plain の文字列は、同じ Markdown から作った html の文字と、同じ空白以外の文字を持つ。空白を埋めるための `\` や `&#32;` は、文字として出さない。
根拠: nesting_attack_test.go TestNonSpaceCharsPreserved, TestNoLeakedFillers; spec_attack_test.go TestSpecPlainMatchesHTML

#### Scenario: 空白を埋める文字が漏れない
- **WHEN** 任意の html をエディタで往復する
- **THEN** 表示に `\` や `&#32;` の文字が残らない

### Requirement: 見出しの中の装飾とメンションを残す
見出しの中の太字・斜体・取り消し線・コード・リンク・メンション・画像は、エディタの往復で残す (MUST)。見出しは 1 行なので、中の改行 (setext の見出しの行の切れ目も) は空白にする。空の見出しは `#` だけの行にし、空の見出しのまま戻す (MUST、`# ` と書くと文字の `#` になっていた)。見出しの末尾の `#` は文字として残す (閉じの `#` と読まれないようにエスケープする) (#64)。
根拠: richtext.go headingLine; frommd.go lineBreak; blocks_test.go TestHeadingKeepsInlines

#### Scenario: 見出しの中の太字とメンション
- **WHEN** `<h2><strong>b</strong> <a data-asana-gid="9" data-asana-type="user">@A</a></h2>` をエディタで往復する
- **THEN** `<h2><strong>b</strong> <a data-asana-gid="9"/></h2>` になる

#### Scenario: 空の見出し
- **WHEN** `<h1></h1>` をエディタで開く
- **THEN** `#` になり、戻すと `<h1></h1>` になる

### Requirement: 変換で落ちるもの
次のものは変換で落ちる (MUST、今の振る舞いとして): html → Markdown で `<u>` の下線 (中の文字は残る)、`h4`〜`h6` の見出し (文として残る)、見出しの中の改行 (空白になる)、`<pre>` の `data-language` のうち英数字と `+#.-_` 以外を含む値や 65 文字以上の値、gid も alt もない `<img>`、`h3` は `###` になり、戻すと `<h2>` になる。Markdown → `TaskNotes` で、画像の alt (html) と画像そのもの (plain)、区切りのすぐ隣で利用者が打った `\ ` (#58)。
根拠: richtext.go walk (atom.U、知らない要素), headingLine, image; frommd.go block の Heading (h3 以下は h2), inline の Image

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
- 本物の Asana が返す `<pre>\nx` の中身が `\nx` か (rich text は XML なのでそう読んでいる、#65)。Asana が `<pre>` の直後に整形の改行を入れて返すなら、開くたびにコードの先頭に空行が 1 つ増える。

## テストで確かめていない振る舞い (コードから読める)

- `ToMarkdown` に `<table>` や `<script>` などの知らない要素を渡すと、要素は捨てて子だけを書く (richtext.go:296-308)。`<script>` の中身も文字として残る。
