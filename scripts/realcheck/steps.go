package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const taskFields = "name,notes,html_notes,due_on,completed,assignee.gid,parent.gid," +
	"memberships.project.gid,memberships.section.gid,tags.gid,tags.name,followers.gid,liked," +
	"resource_subtype,approval_status,assignee_section.gid,workspace.gid," +
	"custom_fields.gid,custom_fields.number_value,custom_fields.date_value,custom_fields.people_value.gid"

// gate stops the write steps after Ctrl-C, or when Asana rate-limited us twice.
func (c *Check) gate() {
	if c.ctx.Err() != nil {
		c.Rep.fail("中断", c.ctx.Err().Error())
		panic(errStop)
	}
	if c.R.RateLimited >= 2 {
		c.Rep.fail("レート制限 (429) が 2 回出たので、書き込みをやめて片付けに進んだ", "")
		panic(errStop)
	}
}

// tool calls an MCP write tool and checks it reached Asana right away.
func (c *Check) tool(step, name string, args M) M {
	c.gate()
	var out M
	err := c.R.Tool(c.ctx, name, args, &out)
	if err != nil {
		c.Rep.fail(step, err.Error())
		return nil
	}
	if s, ok := out["sent"].(bool); ok && !s {
		c.Rep.fail(step+": すぐ送れた", show(out))
	}
	return out
}

func (c *Check) cli(step string, args ...string) M {
	c.gate()
	var out M
	if err := c.R.JSON(c.ctx, &out, args...); err != nil {
		c.Rep.fail(step, err.Error())
		return nil
	}
	if s, ok := out["sent"].(bool); ok && !s {
		c.Rep.fail(step+": すぐ送れた", show(out))
	}
	return out
}

func (c *Check) asana(path string, fields ...string) (any, error) {
	data, err := c.R.API(c.ctx, "GET", path, fields...)
	if err != nil {
		return nil, err
	}
	return decode(data), nil
}

func (c *Check) asanaTask(gid string) any {
	v, err := c.asana("/tasks/"+gid, "opt_fields="+taskFields)
	if err != nil {
		c.Rep.fail("Asana のタスク "+gid, err.Error())
	}
	return v
}

func (c *Check) cacheTask(gid string) any {
	var v M
	if err := c.R.Tool(c.ctx, "get_task", M{"gid": gid, "offline": true}, &v); err != nil {
		c.Rep.fail("キャッシュのタスク "+gid, err.Error())
		return nil
	}
	return v["task"]
}

// both fetches a task from the cache and from Asana and checks them with f.
func (c *Check) both(name, gid string, f func(cache, asana any) (bool, string)) {
	ca, as := c.cacheTask(gid), c.asanaTask(gid)
	ok, d := f(ca, as)
	c.Rep.check(name, ok, d)
}

func (c *Check) eq(name, gid string, cachePath, asanaPath []string, want any) {
	c.both(name, gid, func(ca, as any) (bool, string) {
		av := normalize(want) // asanaPath nil: only the cache is checked
		if asanaPath != nil {
			av = normalize(get(as, asanaPath...))
		}
		return same(name, normalize(get(ca, cachePath...)), av, normalize(want))
	})
}

// normalize makes JSON values from the two sides comparable ("" ≡ null, []any of strings).
func normalize(v any) any {
	switch x := v.(type) {
	case string:
		return nilIfEmpty(x)
	case []any:
		if len(x) == 0 {
			return nil
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	}
	return v
}

// gids is the gids of an array (nil when there is none).
func gids(v any) []any {
	a, _ := normalize(get(v, "#gid")).([]any)
	return a
}

func (c *Check) writes() {
	pre := c.Made.Prefix

	c.group("プロジェクト (create_project・update_project・create_status)")
	for i, name := range []string{pre, pre + "-b"} {
		out := c.tool("create_project "+name, "create_project", M{"name": name, "team": c.team})
		gid := str(out, "project_gid")
		if gid == "" {
			c.must("create_project の gid", fmt.Errorf("no project_gid: %v", out))
		}
		c.Made.add(&c.Made.Projects, gid)
		if i == 0 {
			c.p = gid
		} else {
			c.p2 = gid
		}
	}
	c.cli("project edit", "project", "edit", c.p, "--notes", "Dummy **notes** for realcheck",
		"--start", "2030-01-10", "--due", "2030-01-20", "--color", "dark-green", "--owner", "me")
	c.syncClean("project edit")
	c.project()
	c.tool("create_status_update", "create_status_update", M{"project": c.p, "status": "on_track", "title": pre + " status", "text": "Dummy **status**"})
	c.syncClean("create_status_update")
	c.status()

	c.group("セクション (create・insert・rename・delete_section)")
	c.s1 = str(c.tool("create_section S1", "create_section", M{"project": c.p, "name": "S1"}), "section_gid")
	c.s2 = str(c.tool("create_section S2", "create_section", M{"project": c.p, "name": "S2"}), "section_gid")
	s3 := str(c.tool("create_section S3", "create_section", M{"project": c.p, "name": "S3"}), "section_gid")
	if c.s1 == "" || c.s2 == "" || s3 == "" {
		c.must("create_section の gid", fmt.Errorf("%q %q %q", c.s1, c.s2, s3))
	}
	c.cli("section move", "section", "move", s3, "--project", c.p, "--before", c.s1)
	c.tool("rename_section", "rename_section", M{"project": c.p, "section": c.s2, "name": "S2 renamed"})
	c.syncClean("セクション")
	c.sections("S3 が S1 の前、S2 の名前が変わった", []string{"S3", "S1", "S2 renamed"})
	c.tool("delete_section", "delete_section", M{"project": c.p, "section": s3})
	c.syncClean("delete_section")
	c.sections("S3 が消えた", []string{"S1", "S2 renamed"})

	c.group("タスクの作成 (create_task)")
	c.t1 = c.createTask(M{"name": pre + "-T1", "project": c.p, "section": c.s1, "notes": "Dummy **notes**", "due_on": "2030-02-01"})
	c.t2 = c.createTask(M{"name": pre + "-T2", "project": c.p, "section": c.s1})
	c.t3 = c.createTask(M{"name": pre + "-T3", "project": c.p, "section": c.s1})
	c.cli("task create --parent (CLI)", "task", "create", pre+"-C1", "--parent", c.t1)
	c.c2 = c.createTask(M{"name": pre + "-C2", "parent": c.t1})
	c.syncClean("create_task")
	c.c1 = c.subtaskByName(c.t1, pre+"-C1")
	c.both("T1 の名前・説明・期日・セクション", c.t1, func(ca, as any) (bool, string) {
		for _, p := range [][]string{{"name"}, {"due_on"}} {
			if ok, d := same(p[0], get(ca, p...), get(as, p...), get(as, p...)); !ok {
				return false, d
			}
		}
		if !strings.Contains(str(as, "html_notes"), "<strong>notes</strong>") || str(ca, "notes") != str(as, "notes") {
			return false, "説明: Asana " + str(as, "html_notes") + " / キャッシュ " + str(ca, "notes")
		}
		return true, ""
	})
	c.eq("T1 のセクションが S1", c.t1, []string{"memberships", "#section_gid"}, []string{"memberships", "#section", "#gid"}, []any{c.s1})
	c.subtasks("C1・C2 が T1 のサブタスク", c.t1, []any{c.c1, c.c2}, false)

	c.group("タスクの更新 (update_task)")
	c.tool("update_task 名前・説明・期日・担当", "update_task", M{"gid": c.t1, "name": pre + "-T1-renamed", "notes": "Updated _notes_", "due_on": "2030-02-05", "assignee": "me"})
	c.cli("task complete", "task", "complete", c.t2)
	c.syncClean("update_task")
	c.eq("T1 の名前", c.t1, []string{"name"}, []string{"name"}, pre+"-T1-renamed")
	c.eq("T1 の期日", c.t1, []string{"due_on"}, []string{"due_on"}, "2030-02-05")
	c.eq("T1 の担当", c.t1, []string{"assignee", "gid"}, []string{"assignee", "gid"}, c.Me)
	c.both("T1 の説明", c.t1, func(ca, as any) (bool, string) {
		ok := strings.Contains(str(as, "html_notes"), "<em>notes</em>") && strings.Contains(str(ca, "notes"), "notes")
		return ok, "Asana " + str(as, "html_notes") + " / キャッシュ " + str(ca, "notes")
	})
	c.eq("T2 が完了", c.t2, []string{"completed"}, []string{"completed"}, true)
	c.cli("task reopen", "task", "reopen", c.t2)
	c.syncClean("task reopen")
	c.eq("T2 が未完了", c.t2, []string{"completed"}, []string{"completed"}, false)

	c.group("移動・並べ替え・親の付け替え (move_task・set_parent)")
	c.tool("move_task セクション", "move_task", M{"gid": c.t1, "section": c.s2})
	c.cli("task move --before (CLI)", "task", "move", c.t3, "--before", c.t2)
	c.syncClean("move_task")
	c.eq("T1 のセクションが S2", c.t1, []string{"memberships", "#section_gid"}, []string{"memberships", "#section", "#gid"}, []any{c.s2})
	c.sectionOrder("S1 で T3 が T2 の前", c.s1, c.t3, c.t2)
	c.tool("set_parent C2→T2", "set_parent", M{"gid": c.c2, "parent": c.t2})
	c.syncClean("set_parent")
	c.eq("C2 の親が T2", c.c2, []string{"parent_gid"}, []string{"parent", "gid"}, c.t2)
	c.tool("set_parent C2→T1 (C1 の前)", "set_parent", M{"gid": c.c2, "parent": c.t1, "insert_before": c.c1})
	c.syncClean("set_parent insert_before")
	c.subtasks("T1 のサブタスクが C2, C1 の順", c.t1, []any{c.c2, c.c1}, true)
	c.cli("task parent --none (CLI)", "task", "parent", c.c2, "--none")
	c.syncClean("set_parent none")
	c.eq("C2 の親がない", c.c2, []string{"parent_gid"}, []string{"parent"}, nil)

	c.comments()
	c.attachments()
	c.tags()
	c.myTasks()
	c.relations()
	c.premium()
	if c.Shapes {
		c.shapes(c.Templates)
	}

	c.group("削除・アーカイブ (delete_task・archive)")
	c.tool("delete_task T3", "delete_task", M{"gid": c.t3})
	c.syncClean("delete_task")
	_, aerr := c.asana("/tasks/"+c.t3, "opt_fields=name")
	var ca M
	cerr := c.R.Tool(c.ctx, "get_task", M{"gid": c.t3, "offline": true}, &ca)
	c.Rep.check("T3 が Asana にもキャッシュにもない", aerr != nil && cerr != nil, fmt.Sprintf("Asana: %v / キャッシュ: %v", aerr, cerr))
	for _, undo := range []bool{false, true} {
		args := []string{"project", "archive", c.p2}
		if undo {
			args = append(args, "--undo")
		}
		_, err := c.R.CLI(c.ctx, args...)
		c.Rep.check(strings.Join(args[:2], " ")+fmt.Sprintf(" (undo=%v)", undo), err == nil, errText(err))
		c.syncClean("archive")
		var pc M
		_ = c.R.JSON(c.ctx, &pc, "project", "show", c.p2, "--offline")
		pa, _ := c.asana("/projects/"+c.p2, "opt_fields=archived")
		ok, d := same("archived", get(pc, "archived"), get(pa, "archived"), !undo)
		c.Rep.check(fmt.Sprintf("P2 の archived が %v", !undo), ok, d)
	}

	c.markNotification()

	c.group("書き込みのあと")
	c.syncClean("最後")
}

func (c *Check) createTask(args M) string {
	out := c.tool("create_task "+fmt.Sprint(args["name"]), "create_task", args)
	gid := str(out, "task_gid")
	if !gidRE.MatchString(gid) {
		c.must("create_task の本当の gid", fmt.Errorf("task_gid %q", gid))
	}
	c.Made.add(&c.Made.Tasks, gid)
	return gid
}

func (c *Check) subtaskByName(parent, name string) string {
	ca := c.cacheTask(parent)
	if sub, ok := get(ca, "subtasks").([]any); ok {
		for _, s := range sub {
			if str(s, "name") == name {
				g := str(s, "gid")
				c.Made.add(&c.Made.Tasks, g)
				return g
			}
		}
	}
	c.must("CLI で作ったサブタスク "+name, fmt.Errorf("not in the cache: %v", show(get(ca, "subtasks"))))
	return ""
}

// subtasks compares the subtasks of parent (in order when ordered).
func (c *Check) subtasks(name, parent string, want []any, ordered bool) {
	ca := c.cacheTask(parent)
	as, err := c.asana("/tasks/"+parent+"/subtasks", "opt_fields=gid")
	if err != nil {
		c.Rep.fail(name, err.Error())
		return
	}
	cg, ag := gids(get(ca, "subtasks")), gids(as)
	if ordered {
		// The cache keeps no order of subtasks (created order): Asana is checked for the
		// order, the cache for the same set.
		okA, dA := same(name+" (Asana)", ag, ag, want)
		c.Rep.check(name+" (Asana の並び)", okA, dA)
		c.Rep.note(name+" (キャッシュの並び。並び順は持たない作り)", show(cg))
		ordered = false
	}
	if !ordered {
		slices.SortFunc(cg, cmpAny)
		slices.SortFunc(ag, cmpAny)
		want = slices.Clone(want)
		slices.SortFunc(want, cmpAny)
	}
	ok, d := same(name, cg, ag, want)
	c.Rep.check(name, ok, d)
}

func cmpAny(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) }

// sectionOrder checks first comes before second in section s, in Asana and in the cache.
func (c *Check) sectionOrder(name, s, first, second string) {
	as, err := c.asana("/sections/"+s+"/tasks", "opt_fields=gid")
	if err != nil {
		c.Rep.fail(name, err.Error())
		return
	}
	var ca []any
	var lt M
	if err := c.R.Tool(c.ctx, "list_tasks", M{"project": c.p, "section": s, "completion": "all"}, &lt); err == nil {
		ca, _ = get(lt, "tasks").([]any)
	}
	before := func(list []any) bool {
		i, j := slices.Index(list, any(first)), slices.Index(list, any(second))
		return i >= 0 && j >= 0 && i < j
	}
	ag, cg := gids(as), gids(ca)
	c.Rep.check(name+" (Asana)", before(ag), show(ag))
	// The cache keeps no order of a section's tasks (README: "Asana にだけ反映").
	c.Rep.note(name+" (キャッシュの並び。並び順は持たない作り)", fmt.Sprintf("%v → %v", show(cg), before(cg)))
}

func (c *Check) project() {
	var ca M
	if err := c.R.JSON(c.ctx, &ca, "project", "show", c.p, "--offline"); err != nil {
		c.Rep.fail("project show", err.Error())
		return
	}
	as, err := c.asana("/projects/"+c.p, "opt_fields=notes,html_notes,start_on,due_on,color,owner.gid")
	if err != nil {
		c.Rep.fail("Asana のプロジェクト", err.Error())
		return
	}
	for _, f := range []struct {
		k    []string
		want any
	}{{[]string{"start_on"}, "2030-01-10"}, {[]string{"due_on"}, "2030-01-20"}, {[]string{"owner", "gid"}, c.Me}} {
		ok, d := same(f.k[0], get(ca, f.k...), get(as, f.k...), f.want)
		c.Rep.check("プロジェクトの "+strings.Join(f.k, "."), ok, d)
	}
	// Asana renames the old color names (dark-green → green): the cache must follow it.
	ok, d := same("color", get(ca, "color"), get(as, "color"), get(as, "color"))
	c.Rep.check("プロジェクトの色がキャッシュと Asana で同じ", ok && str(as, "color") != "none", d)
	c.Rep.note("project edit --color dark-green のあとの Asana の色", str(as, "color"))
	ok = strings.Contains(str(as, "html_notes"), "<strong>notes</strong>") && str(ca, "notes") == str(as, "notes")
	c.Rep.check("プロジェクトの説明 (マークダウン → html_notes、キャッシュは Asana の notes と同じ)", ok, "Asana "+str(as, "html_notes")+" "+str(as, "notes")+" / キャッシュ "+str(ca, "notes"))
}

func (c *Check) status() {
	as, err := c.asana("/status_updates", "parent="+c.p, "opt_fields=title,status_type,html_text")
	if err != nil {
		c.Rep.fail("Asana のステータス更新", err.Error())
		return
	}
	for _, g := range gids(as) {
		c.Made.add(&c.Made.Statuses, fmt.Sprint(g))
	}
	var ca M
	_ = c.R.JSON(c.ctx, &ca, "project", "show", c.p, "--offline")
	ok, d := same("title", get(ca, "latest_status", "title"), get(as, "#title"), c.Made.Prefix+" status")
	if a, isArr := get(as, "#title").([]any); isArr && len(a) == 1 {
		ok, d = same("title", get(ca, "latest_status", "title"), a[0], c.Made.Prefix+" status")
	}
	c.Rep.check("ステータス更新が 1 件、キャッシュの最新と同じ", ok, d)
	html := strings.Join(strs(get(as, "#html_text")), " ")
	c.Rep.check("ステータス更新の本文がマークダウン → html", strings.Contains(html, "<strong>status</strong>"), html)
}

// sections compares the section names of P (ignoring Asana's default "Untitled section").
func (c *Check) sections(name string, want []string) {
	as, err := c.asana("/projects/"+c.p+"/sections", "opt_fields=name")
	if err != nil {
		c.Rep.fail(name, err.Error())
		return
	}
	var ps []M
	_ = c.R.JSON(c.ctx, &ps, "project", "list")
	var cs any
	for _, p := range ps {
		if str(p, "gid") == c.p {
			cs = p["sections"]
		}
	}
	names := func(v any) []string {
		var out []string
		for _, n := range strs(get(v, "#name")) {
			if n != "Untitled section" && n != "無題のセクション" {
				out = append(out, n)
			}
		}
		return out
	}
	an, cn := names(as), names(cs)
	ok := slices.Equal(an, want) && slices.Equal(cn, want)
	c.Rep.check(name, ok, fmt.Sprintf("期待 %v / キャッシュ %v / Asana %v (全部: %v)", want, cn, an, strs(get(as, "#name"))))
}

func (c *Check) comments() {
	c.group("コメント (add_comment・update_story・delete_story)")
	c.tool("add_comment", "add_comment", M{"gid": c.t1, "text": "Dummy **comment**"})
	c.syncClean("add_comment")
	var gid string
	if st, ok := get(c.cacheTask(c.t1), "stories").([]any); ok {
		for _, s := range st {
			if strings.Contains(str(s, "text"), "Dummy") && !strings.HasPrefix(str(s, "gid"), "local") {
				gid = str(s, "gid")
			}
		}
	}
	if gid == "" {
		c.Rep.fail("コメントの本当の gid がキャッシュにある", "")
		return
	}
	story := func() (any, error) { return c.asana("/stories/"+gid, "opt_fields=text,html_text,resource_subtype") }
	as, err := story()
	c.Rep.check("コメントが Asana にあり、太字が html になった", err == nil && strings.Contains(str(as, "html_text"), "<strong>comment</strong>"), errText(err)+" "+str(as, "html_text"))
	c.tool("update_comment", "update_comment", M{"gid": gid, "text": "Edited _comment_"})
	c.syncClean("update_comment")
	as, err = story()
	ca := c.cacheStory(c.t1, gid)
	c.Rep.check("コメントの編集が Asana とキャッシュに届いた", err == nil && strings.Contains(str(as, "html_text"), "<em>comment</em>") && strings.Contains(str(ca, "text"), "Edited"),
		errText(err)+" Asana "+str(as, "html_text")+" / キャッシュ "+str(ca, "text"))
	c.cli("comment delete (CLI)", "comment", "delete", gid, "--yes")
	c.syncClean("delete_story")
	_, err = story()
	c.Rep.check("コメントが Asana からもキャッシュからも消えた", err != nil && c.cacheStory(c.t1, gid) == nil, errText(err))
}

func (c *Check) cacheStory(task, gid string) any {
	if st, ok := get(c.cacheTask(task), "stories").([]any); ok {
		for _, s := range st {
			if str(s, "gid") == gid {
				return s
			}
		}
	}
	return nil
}

func (c *Check) attachments() {
	c.group("添付 (upload_attachment・delete_attachment)")
	dir, err := os.MkdirTemp(c.R.Env.Work, "attach-")
	if err != nil {
		c.Rep.fail("一時ファイル", err.Error())
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "realcheck.txt")
	if err := os.WriteFile(path, []byte("dummy attachment for asagao realcheck\n"), 0o600); err != nil {
		c.Rep.fail("一時ファイル", err.Error())
		return
	}
	c.cli("task attach (CLI)", "task", "attach", c.t1, path)
	c.syncClean("upload_attachment")
	list := func() []any {
		as, err := c.asana("/attachments", "parent="+c.t1, "opt_fields=name")
		if err != nil {
			c.Rep.fail("Asana の添付", err.Error())
		}
		return gids(as)
	}
	ag := list()
	cg := gids(get(c.cacheTask(c.t1), "attachments"))
	ok, d := same("attachments", cg, ag, ag)
	c.Rep.check("添付が 1 件で、Asana とキャッシュの gid が同じ", ok && len(ag) == 1, d)
	if len(ag) != 1 {
		return
	}
	c.tool("delete_attachment", "delete_attachment", M{"gid": fmt.Sprint(ag[0])})
	c.syncClean("delete_attachment")
	ok, d = same("attachments", gids(get(c.cacheTask(c.t1), "attachments")), list(), nil)
	c.Rep.check("添付が Asana からもキャッシュからも消えた", ok, d)
}

func (c *Check) tagByName(name string) M {
	var tags []M
	_ = c.R.JSON(c.ctx, &tags, "tag", "list")
	for _, t := range tags {
		if str(t, "name") == name {
			return t
		}
	}
	return nil
}

func (c *Check) tags() {
	c.group("タグ (add_tag・update_tag・remove_tag・delete_tag)")
	name := c.Made.Prefix + "-tag"
	c.cli("task tag --add (新しいタグ)", "task", "tag", c.t1, "--add", name)
	c.syncClean("add_tag")
	t := c.tagByName(name)
	c.tag = str(t, "gid")
	if !gidRE.MatchString(c.tag) {
		c.Rep.fail("新しいタグの本当の gid がキャッシュにある", show(t))
		return
	}
	c.Made.add(&c.Made.Tags, c.tag)
	c.eq("T1 にタグが付いた", c.t1, []string{"tags"}, []string{"tags", "#name"}, []any{name})
	name2 := name + "2"
	c.cli("tag rename", "tag", "rename", name, name2)
	c.cli("tag color", "tag", "color", name2, "dark-red")
	c.syncClean("update_tag")
	c.tagIs("タグの名前と色", name2, "dark-red")
	c.cli("tag color none", "tag", "color", name2, "none")
	c.syncClean("update_tag none")
	c.tagIs("タグの色を外した", name2, nil)
	c.tool("update_task remove_tags", "update_task", M{"gid": c.t1, "remove_tags": []string{c.tag}})
	c.syncClean("remove_tag")
	c.eq("T1 からタグが外れた", c.t1, []string{"tags"}, []string{"tags"}, nil)
	c.cli("tag rm", "tag", "rm", name2, "--yes")
	c.syncClean("delete_tag")
	_, err := c.asana("/tags/"+c.tag, "opt_fields=name")
	c.Rep.check("タグが Asana からもキャッシュからも消えた", err != nil && c.tagByName(name2) == nil, errText(err))
}

func (c *Check) tagIs(name, tagName string, color any) {
	as, err := c.asana("/tags/"+c.tag, "opt_fields=name,color")
	if err != nil {
		c.Rep.fail(name, err.Error())
		return
	}
	ca := c.tagByName(tagName)
	ok1, d1 := same("name", str(ca, "name"), str(as, "name"), tagName)
	// Asana may rename old color names (as dark-green → green for projects): the cache must
	// follow Asana; only "no color" is checked against what was asked.
	want := normalize(get(as, "color"))
	if color == nil {
		want = nil
	}
	ok2, d2 := same("color", normalize(get(ca, "color")), normalize(get(as, "color")), want)
	c.Rep.check(name, ok1 && ok2, d1+" "+d2)
	c.Rep.note(name+": Asana の色", fmt.Sprintf("%v (頼んだのは %v)", get(as, "color"), color))
}

func (c *Check) myTasks() {
	c.group("マイタスクのセクション (move_my_section)")
	var out struct {
		Sections []M `json:"sections"`
	}
	if err := c.R.Tool(c.ctx, "list_my_sections", M{}, &out); err != nil || len(out.Sections) < 2 {
		c.Rep.fail("マイタスクのセクションが 2 つ以上ある", fmt.Sprint(err, out.Sections))
		return
	}
	cur := str(c.cacheTask(c.t1), "my_section", "gid")
	var to M
	for _, s := range out.Sections {
		if str(s, "gid") != cur {
			to = s
			break
		}
	}
	c.cli("task move --my-tasks (CLI)", "task", "move", c.t1, "--my-tasks", "--section", str(to, "gid"))
	c.syncClean("move_my_section")
	c.eq("T1 のマイタスクのセクション", c.t1, []string{"my_section", "gid"}, []string{"assignee_section", "gid"}, str(to, "gid"))
}

func (c *Check) relations() {
	c.group("フォロワー・いいね・プロジェクト・複製")
	has := func(field, gidKey, want string) func(ca, as any) (bool, string) {
		return func(ca, as any) (bool, string) {
			cs, as2 := strs(get(ca, field, "#gid")), strs(get(as, field, "#"+gidKey))
			return slices.Contains(cs, want) == slices.Contains(as2, want), fmt.Sprintf("キャッシュ %v / Asana %v", cs, as2)
		}
	}
	for _, add := range []bool{false, true} {
		key := map[bool]string{false: "remove_followers", true: "add_followers"}[add]
		c.tool(key, "update_task", M{"gid": c.t1, key: []string{"me"}})
		c.syncClean(key)
		c.both(fmt.Sprintf("自分がフォロワーにいる = %v", add), c.t1, func(ca, as any) (bool, string) {
			ok, d := has("followers", "gid", c.Me)(ca, as)
			return ok && slices.Contains(strs(get(as, "followers", "#gid")), c.Me) == add, d
		})
	}
	for _, like := range []bool{true, false} {
		c.tool(fmt.Sprintf("liked=%v", like), "update_task", M{"gid": c.t1, "liked": like})
		c.syncClean("like")
		c.eq(fmt.Sprintf("いいね = %v", like), c.t1, []string{"liked"}, []string{"liked"}, like)
	}
	for _, add := range []bool{true, false} {
		key := map[bool]string{false: "remove_projects", true: "add_projects"}[add]
		c.tool(key, "update_task", M{"gid": c.t1, key: []string{c.p2}})
		c.syncClean(key)
		c.both(fmt.Sprintf("P2 に入っている = %v", add), c.t1, func(ca, as any) (bool, string) {
			cp, apg := strs(get(ca, "memberships", "#project_gid")), strs(get(as, "memberships", "#project", "#gid"))
			return slices.Contains(cp, c.p2) == add && slices.Contains(apg, c.p2) == add, fmt.Sprintf("キャッシュ %v / Asana %v", cp, apg)
		})
	}
	name := c.Made.Prefix + "-T3-copy"
	out := c.tool("duplicate_task", "duplicate_task", M{"gid": c.t3, "name": name})
	if g := str(out, "task_gid"); gidRE.MatchString(g) {
		c.Made.add(&c.Made.Tasks, g)
	}
	c.syncClean("duplicate_task")
	as, err := c.asana("/projects/"+c.p+"/tasks", "opt_fields=name")
	var copyGID string
	if a, ok := as.([]any); ok {
		for _, t := range a {
			if str(t, "name") == name {
				copyGID = str(t, "gid")
				c.Made.add(&c.Made.Tasks, copyGID)
			}
		}
	}
	c.copy = copyGID
	c.Rep.check("複製が P にできた", err == nil && copyGID != "", errText(err))
	if copyGID != "" {
		c.eq("複製がキャッシュにもある", copyGID, []string{"name"}, []string{"name"}, name)
		c.Rep.check("MCP が返した gid が複製の本当の gid", str(out, "task_gid") == copyGID, show(out))
	}
}

func (c *Check) outbox() []M {
	var ob []M
	if err := c.R.JSON(c.ctx, &ob, "outbox", "list"); err != nil {
		c.Rep.fail("outbox list", err.Error())
	}
	return ob
}

// discardFailed drops what the step left in the outbox (a paid feature refused) and
// records it: only entries that were not there before the step and are about our tasks.
func (c *Check) discardFailed(step string, before []M) {
	for _, e := range toDiscard(before, c.outbox(), c.Made.Tasks) {
		c.Rep.note(step+" の未送信", fmt.Sprintf("%s %s: %s", str(e, "kind"), str(e, "state"), str(e, "last_error")))
		id := outboxID(e)
		if _, err := c.R.CLI(c.ctx, "outbox", "discard", id); err != nil {
			c.Rep.fail("outbox discard "+id, err.Error())
		}
	}
}

func outboxID(e M) string {
	if f, ok := get(e, "id").(float64); ok {
		return fmt.Sprint(int64(f))
	}
	return fmt.Sprint(get(e, "id"))
}

// toDiscard is the entries of after that are new since before and about one of tasks.
func toDiscard(before, after []M, tasks []string) []M {
	old := map[string]bool{}
	for _, e := range before {
		old[outboxID(e)] = true
	}
	var out []M
	for _, e := range after {
		if !old[outboxID(e)] && slices.Contains(tasks, str(e, "task_gid")) {
			out = append(out, e)
		}
	}
	return out
}

// leftovers checks after cleanup that typeahead finds nothing with the prefix.
func (c *Check) leftovers() {
	for _, kind := range []string{"task", "tag", "project", "project_template", "goal", "portfolio"} {
		as, err := c.asana("/workspaces/"+TestWorkspace+"/typeahead", "resource_type="+kind, "query="+c.Made.Prefix, "opt_fields=name")
		var left []string
		for _, e := range asArray(as) {
			if !strings.HasPrefix(str(e, "name"), c.Made.Prefix) {
				continue
			}
			// typeahead's index lags behind deletes: ask the object itself.
			path := map[string]string{"task": "/tasks/", "tag": "/tags/", "project": "/projects/",
				"project_template": "/project_templates/", "goal": "/goals/", "portfolio": "/portfolios/"}[kind]
			if _, err := c.asana(path+str(e, "gid"), "opt_fields=name"); err != nil && gone(err.Error()) {
				continue
			}
			left = append(left, str(e, "gid")+" "+str(e, "name"))
		}
		c.Rep.check("typeahead で前置きの "+kind+" が見つからない", err == nil && len(left) == 0, errText(err)+strings.Join(left, ", "))
	}
}

func asArray(v any) []any {
	a, _ := v.([]any)
	return a
}

// premium tries the paid features' writes and records how this plan answers.
func (c *Check) premium() {
	c.group("有料機能の書き込み (依存関係・種類・承認・カスタムフィールド)")
	try := func(step string, args M, check func()) {
		c.gate()
		before := c.outbox()
		var out M
		err := c.R.Tool(c.ctx, "update_task", args, &out)
		c.Rep.note(step, describeWrite(out, err))
		_, _ = c.R.CLI(c.ctx, "sync")
		if err == nil && boolean(out, "sent") {
			check()
		}
		c.discardFailed(step, before)
	}
	try("add_dependencies T2 ← T3", M{"gid": c.t2, "add_dependencies": []string{c.t3}}, func() {
		c.eq("T2 が T3 を待つ", c.t2, []string{"dependencies", "#gid"}, nil, []any{c.t3})
		as, err := c.asana("/tasks/"+c.t2+"/dependencies", "opt_fields=gid")
		c.Rep.check("Asana でも T2 が T3 を待つ", err == nil && slices.Equal(gids(as), []any{c.t3}), errText(err)+show(as))
	})
	try("remove_dependencies", M{"gid": c.t2, "remove_dependencies": []string{c.t3}}, func() {
		as, err := c.asana("/tasks/"+c.t2+"/dependencies", "opt_fields=gid")
		ca := c.cacheTask(c.t2)
		c.Rep.check("依存関係が外れた", err == nil && normalize(as) == nil && normalize(get(ca, "dependencies")) == nil, errText(err)+show(as)+show(get(ca, "dependencies")))
	})
	try("resource_subtype=milestone", M{"gid": c.t3, "resource_subtype": "milestone"}, func() {
		c.eq("T3 がマイルストーン", c.t3, []string{"resource_subtype"}, []string{"resource_subtype"}, "milestone")
	})
	try("resource_subtype=approval", M{"gid": c.t3, "resource_subtype": "approval"}, func() {
		c.eq("T3 が承認", c.t3, []string{"resource_subtype"}, []string{"resource_subtype"}, "approval")
	})
	try("approval_status=approved", M{"gid": c.t3, "approval_status": "approved"}, func() {
		c.eq("T3 が承認済み", c.t3, []string{"approval_status"}, []string{"approval_status"}, "approved")
	})
	try("resource_subtype=default_task", M{"gid": c.t3, "resource_subtype": "default_task"}, func() {
		c.eq("T3 が普通のタスク", c.t3, []string{"resource_subtype"}, []string{"resource_subtype"}, "default_task")
	})
	c.customFields()
}

func describeWrite(out M, err error) string {
	if err != nil {
		return "エラー: " + err.Error()
	}
	if boolean(out, "sent") {
		return "送れた"
	}
	return "送れなかった: " + show(out)
}

// customFields checks the write format of number, date and people fields (#1): set them,
// then clear them with null. The fields are made for this run and added to P only.
func (c *Check) customFields() {
	kinds := []string{"number", "date", "people"}
	made := map[string]string{}
	for _, k := range kinds {
		name := c.Made.Prefix + "-" + k
		fields := []string{"workspace=" + TestWorkspace, "name=" + name, "resource_subtype=" + k}
		if k == "number" {
			fields = append(fields, "precision=0") // "precision: Missing input" without it
		}
		data, err := c.R.WriteAPI(c.ctx, c.Made, "POST", "/custom_fields", fields...)
		if err != nil {
			c.Rep.note("カスタムフィールドを作る ("+k+")", describe(nil, err))
			continue
		}
		g := str(decode(data), "gid")
		c.Made.add(&c.Made.CustomFields, g)
		if _, err := c.R.WriteAPI(c.ctx, c.Made, "POST", "/projects/"+c.p+"/addCustomFieldSetting", "custom_field="+g); err != nil {
			c.Rep.fail("P にカスタムフィールドを足す ("+k+")", err.Error())
			continue
		}
		made[k] = g
	}
	if len(made) == 0 {
		return
	}
	c.cf = made
	c.syncClean("カスタムフィールドの設定")
	set := M{}
	want := map[string]any{"number": 42.0, "date": "2030-03-01", "people": []any{c.Me}}
	in := map[string]any{"number": 42, "date": "2030-03-01", "people": []string{c.Me}}
	for k, g := range made {
		set[g] = in[k]
	}
	c.tool("update_task custom_fields (number・date・people)", "update_task", M{"gid": c.t1, "custom_fields": set})
	c.syncClean("set_custom_field")
	c.cfValues("カスタムフィールドを設定", made, want)
	clear := M{}
	for k, g := range made {
		if k != "people" {
			clear[g] = nil
		}
	}
	c.tool("update_task custom_fields = null (number・date)", "update_task", M{"gid": c.t1, "custom_fields": clear})
	c.syncClean("set_custom_field null")
	delete(made, "people")
	c.cfValues("カスタムフィールドを空にした", made, map[string]any{"number": nil, "date": nil})
	if g, ok := c.cf["people"]; ok {
		// Asana rejects null for people ("Not an array"); fixed in feat-fields-37 (#37).
		c.gate()
		before := c.outbox()
		var out M
		err := c.R.Tool(c.ctx, "update_task", M{"gid": c.t1, "custom_fields": M{g: nil}}, &out)
		_, _ = c.R.CLI(c.ctx, "sync")
		c.Rep.note("people を空にする (#37 で直す)", describeWrite(out, err))
		c.discardFailed("people を空にする", before)
	}
}

func (c *Check) cfValues(name string, made map[string]string, want map[string]any) {
	as := c.asanaTask(c.t1)
	ca := c.cacheTask(c.t1)
	find := func(v any, g string) any {
		if a, ok := get(v, "custom_fields").([]any); ok {
			for _, f := range a {
				if str(f, "gid") == g {
					return f
				}
			}
		}
		return nil
	}
	for k, g := range made {
		af, cf := find(as, g), find(ca, g)
		var av any
		switch k {
		case "number":
			av = get(af, "number_value")
		case "date":
			av = get(af, "date_value", "date")
		case "people":
			av = normalize(get(af, "people_value", "#gid"))
		}
		ok := fmt.Sprint(normalize(av)) == fmt.Sprint(want[k])
		c.Rep.check(fmt.Sprintf("%s (%s): Asana", name, k), ok, fmt.Sprintf("期待 %v / Asana %v", show(want[k]), show(av)))
		cv := normalize(get(cf, "value"))
		empty := want[k] == nil
		cok := (cv == nil) == empty
		c.Rep.check(fmt.Sprintf("%s (%s): キャッシュ", name, k), cok, fmt.Sprintf("期待 %v / キャッシュ %s", show(want[k]), show(cf)))
	}
}

// markNotification marks one unread notification read, then unread again, so the inbox
// ends as it was (mark_notification). Nothing happens when there is no unread one.
func (c *Check) markNotification() {
	c.group("受信トレイの既読・未読 (mark_notification)")
	var in struct {
		Notifications []M `json:"notifications"`
	}
	_ = c.R.JSON(c.ctx, &in, "inbox", "--unread", "--limit", "1")
	if len(in.Notifications) == 0 {
		c.Rep.note("mark_notification", "未読の通知がないので試していない")
		return
	}
	g := str(in.Notifications[0], "gid")
	for _, read := range []bool{true, false} {
		sub := map[bool]string{true: "read", false: "unread"}[read]
		out, err := c.R.CLI(c.ctx, "--json", "inbox", sub, g)
		c.Rep.check("inbox "+sub, err == nil, errText(err)+string(out))
		c.syncClean("inbox " + sub)
		as, aerr := c.asana("/notifications/"+g, "opt_fields=is_read,read")
		var now struct {
			Notifications []M `json:"notifications"`
		}
		_ = c.R.JSON(c.ctx, &now, "inbox", "--limit", "50")
		var cache any
		for _, n := range now.Notifications {
			if str(n, "gid") == g {
				cache = n["read"]
			}
		}
		c.Rep.check(fmt.Sprintf("キャッシュの既読 = %v", read), cache == read, show(cache))
		c.Rep.note("Asana の通知 (既読の形)", describe(nil, aerr)+" "+show(as))
	}
}
