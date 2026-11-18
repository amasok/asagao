package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// briefBody is a brief with what Asana's rich text can hold: headings, emphasis, a link,
// a rule, lists, an image and a table (asagao keeps a table as an asana-html block).
const briefBody = "# Heading one\n\n## Heading two\n\nPlain text with **bold**, *italic* and a [link](https://example.com/realcheck).\n\n" +
	"---\n\n- first\n- second\n  - nested\n\n1. one\n2. two\n\n![picture](https://example.com/realcheck.png)\n\n" +
	"| a | b |\n|---|---|\n| 1 | 2 |\n"

// brief checks project briefs (#60, a preview API) on the real Asana through the CLI and
// MCP (-brief): create, update, title only, a change in Asana meanwhile (a conflict),
// delete, and that the body comes back the same way. Only the brief of a project of the
// run is written; the raw answers are saved in the run's shapes folder.
func (c *Check) brief() {
	pre := c.Made.Prefix
	c.group("#60 プロジェクトブリーフ (project brief、MCP の get/update_project_brief)")
	s := &shapes{c: c, dir: filepath.Join(c.R.Env.Run, "shapes")}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		c.must("shapes", err)
	}
	out := c.tool("create_project", "create_project", M{"name": pre, "team": c.team})
	c.p = str(out, "project_gid")
	if c.p == "" {
		c.must("create_project の gid", fmt.Errorf("%v", out))
	}
	c.Made.add(&c.Made.Projects, c.p)
	s.get("project-brief-none", "/projects/"+c.p, "opt_fields=project_brief")
	s.get("brief-404", "/project_briefs/1")

	file := filepath.Join(c.R.Env.Work, "brief.md")
	if err := os.WriteFile(file, []byte(briefBody), 0o600); err != nil {
		c.must("brief.md", err)
	}
	c.cli("project brief --title --file (作る)", "project", "brief", c.p, "--title", pre+" brief", "--file", file)
	c.syncClean("ブリーフを作る")
	bg := c.briefGID()
	if bg == "" {
		c.must("ブリーフの gid", fmt.Errorf("Asana にブリーフがない"))
	}
	c.Made.add(&c.Made.Briefs, bg)
	first := s.get("brief-created", "/project_briefs/"+bg, "opt_fields=title,text,html_text,project")
	html1 := str(first, "data", "html_text")
	c.Rep.check("Asana のタイトル", str(first, "data", "title") == pre+" brief", str(first, "data", "title"))
	for _, want := range []string{"<h1>", "<h2>", "<strong>bold</strong>", "<em>italic</em>", `href="https://example.com/realcheck"`, "<hr", "<ul>", "<ol>", "<table"} {
		c.Rep.check("Asana の html_text に "+want, strings.Contains(html1, want), oneLine(html1))
	}
	c.Rep.note("画像 (外部 URL) の Asana での形", imgOf(html1))

	// Round trip: what asagao reads back, written again, is the same in Asana.
	md1 := c.briefMarkdown("読み戻し 1")
	c.Rep.note("asagao が読み戻したマークダウン", oneLine(md1))
	if err := os.WriteFile(file, []byte(md1), 0o600); err != nil {
		c.must("brief.md", err)
	}
	c.cli("project brief --file (読み戻したものを書き戻す)", "project", "brief", c.p, "--file", file)
	c.syncClean("書き戻す")
	second := s.get("brief-written-back", "/project_briefs/"+bg, "opt_fields=title,html_text")
	md2 := c.briefMarkdown("読み戻し 2")
	c.Rep.check("書き戻しても Asana の html_text が変わらない", str(second, "data", "html_text") == html1,
		"1: "+oneLine(html1)+"\n2: "+oneLine(str(second, "data", "html_text")))
	c.Rep.check("書き戻しても asagao のマークダウンが変わらない", md2 == md1, "1: "+oneLine(md1)+"\n2: "+oneLine(md2))

	// Title only.
	c.cli("project brief --title (タイトルだけ)", "project", "brief", c.p, "--title", pre+" brief 2")
	c.syncClean("タイトルだけ")
	third := s.get("brief-title-only", "/project_briefs/"+bg, "opt_fields=title,html_text")
	c.Rep.check("タイトルだけ変わり、本文はそのまま", str(third, "data", "title") == pre+" brief 2" && str(third, "data", "html_text") == html1,
		str(third, "data", "title")+" / "+oneLine(str(third, "data", "html_text")))

	// Changed in Asana meanwhile: asagao's next change is a conflict, not an overwrite.
	s.write("brief-changed-in-asana", "PUT", "/project_briefs/"+bg, M{"data": M{"html_text": "<body><p>changed in Asana</p></body>"}})
	before := c.outbox()
	var cr M
	err := c.R.JSON(c.ctx, &cr, "project", "brief", c.p, "--title", pre+" brief 3")
	c.Rep.note("Asana で変わったあとに asagao で変える", fmt.Sprintf("%v %s", err, show(cr)))
	ob := c.outbox()
	conflict := false
	for _, e := range toDiscardAny(before, ob) {
		conflict = conflict || str(e, "state") == "conflict"
	}
	fourth := s.get("brief-after-conflict", "/project_briefs/"+bg, "opt_fields=title,html_text")
	c.Rep.check("競合になり、Asana の本文は上書きされない", conflict && strings.Contains(str(fourth, "data", "html_text"), "changed in Asana"),
		show(ob)+" / "+oneLine(str(fourth, "data", "html_text")))
	for _, e := range toDiscardAny(before, ob) {
		if _, err := c.R.CLI(c.ctx, "outbox", "discard", outboxID(e)); err != nil {
			c.Rep.fail("outbox discard", err.Error())
		}
	}
	c.syncClean("競合を捨てたあと")

	// Delete with the CLI, then the same with MCP.
	c.cli("project brief rm", "project", "brief", "rm", c.p)
	c.syncClean("brief rm")
	c.Rep.check("CLI で消えた", c.briefGID() == "", "")
	c.Made.add(&c.Made.Deleted, "project_briefs/"+bg)

	var mo M
	err = c.R.Tool(c.ctx, "update_project_brief", M{"project": c.p, "title": pre + " mcp", "markdown": "## From MCP\n\nbody"}, &mo)
	c.Rep.check("MCP update_project_brief で作る", err == nil, errText(err)+show(mo))
	c.syncClean("MCP で作る")
	mg := c.briefGID()
	c.Made.add(&c.Made.Briefs, mg)
	var gb M
	err = c.R.Tool(c.ctx, "get_project_brief", M{"project": c.p}, &gb)
	c.Rep.check("MCP get_project_brief", err == nil && strings.Contains(str(gb, "markdown"), "From MCP") && str(gb, "title") == pre+" mcp",
		errText(err)+show(gb))
	var cli M
	_ = c.R.JSON(c.ctx, &cli, "project", "brief", c.p, "--offline")
	c.Rep.check("CLI と MCP で同じ本文", str(cli, "markdown") == str(gb, "markdown") && str(cli, "title") == str(gb, "title"),
		show(cli)+" / "+show(gb))
	err = c.R.Tool(c.ctx, "update_project_brief", M{"project": c.p, "delete": true}, &mo)
	c.syncClean("MCP で消す")
	c.Rep.check("MCP で消えた", err == nil && c.briefGID() == "", errText(err))
	c.Made.add(&c.Made.Deleted, "project_briefs/"+mg)

	// A second brief on one project: what Asana answers.
	s.write("brief-direct-create", "POST", "/projects/"+c.p+"/project_briefs", M{"data": M{"title": pre + " direct", "html_text": "<body>x</body>"}})
	if g := c.briefGID(); g != "" {
		c.Made.add(&c.Made.Briefs, g)
	}
	s.write("brief-second-create", "POST", "/projects/"+c.p+"/project_briefs", M{"data": M{"title": pre + " second", "html_text": "<body>y</body>"}})
	s.write("brief-text-and-html", "PUT", "/project_briefs/"+c.briefGID(), M{"data": M{"text": "t", "html_text": "<body>h</body>"}})
	c.syncClean("最後")
}

// briefGID is the gid of P's brief in Asana ("" for none).
func (c *Check) briefGID() string {
	p, _ := c.asana("/projects/"+c.p, "opt_fields=project_brief")
	return str(p, "project_brief", "gid")
}

// briefMarkdown is the brief as asagao reads it from Asana.
func (c *Check) briefMarkdown(step string) string {
	var b M
	if err := c.R.JSON(c.ctx, &b, "project", "brief", c.p); err != nil {
		c.Rep.fail(step, err.Error())
	}
	return str(b, "markdown")
}

func imgOf(html string) string {
	i := strings.Index(html, "<img")
	if i < 0 {
		return "img がない: " + oneLine(html)
	}
	j := strings.Index(html[i:], ">")
	if j < 0 {
		return html[i:]
	}
	return html[i : i+j+1]
}

// toDiscardAny is the entries of after that are new since before.
func toDiscardAny(before, after []M) []M {
	old := map[string]bool{}
	for _, e := range before {
		old[outboxID(e)] = true
	}
	var out []M
	for _, e := range after {
		if !old[outboxID(e)] {
			out = append(out, e)
		}
	}
	return out
}
