---
name: issue
description: >-
  asagao の GitHub Issue を決まった形式で作る。作業中に見つけた「今回は扱わない」問題や、ユーザーに頼まれた残タスクを登録するときに使う。重複を確認してから作成する。
---

# issue

1. 重複を確認する:
   ```sh
   gh issue list --state open --search "<キーワード>" --limit 5
   ```
   同じ Issue が既にあれば `gh issue comment` で追記して終える（末尾に `🤖 Generated with Gemini (Antigravity)` を付ける）。
2. タイトルは日本語で症状が分かる一文にし、本文は以下の構成で作成する:
   ```markdown
   ## 何が起きるか
   <どういう入力・操作で、何が起きるか。main でも起きるかどうか>

   ## 影響
   <データが消えるか / 見た目だけか / 安全性に関わるか>

   ## 直し方の案
   <分かっていれば。場所は `path/file.go` の関数名で書く>

   ## 見つけた経緯
   <どの作業・どのテストで見つけたか (1 行)>

   🤖 Generated with Gemini (Antigravity)
   ```
3. 複数ある場合は関係の近いものを 1 つにまとめる。ユーザーには Issue 番号とタイトルだけを簡潔に返す。
4. ユーザーの名前・メールアドレス・トークンなどの個人・機密情報は本文に書かない。
