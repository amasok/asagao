package main

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// features checks the features of #2, #6, #7, #36 and #37 on the real Asana through the
// asagao CLI and MCP (-features). Everything is made inside a project of the run, or is a
// prefixed template / goal / portfolio / custom field of the run, and is deleted at the end.
func (c *Check) features() {
	pre := c.Made.Prefix
	c.group("準備 (確認用のプロジェクト・セクション・タスク)")
	out := c.tool("create_project", "create_project", M{"name": pre, "team": c.team})
	c.p = str(out, "project_gid")
	if c.p == "" {
		c.must("create_project の gid", fmt.Errorf("%v", out))
	}
	c.Made.add(&c.Made.Projects, c.p)
	// dates, so a template made from it asks for a start date
	c.cli("project edit (日付)", "project", "edit", c.p, "--start", "2030-01-10", "--due", "2030-01-20")
	c.s1 = str(c.tool("create_section S1", "create_section", M{"project": c.p, "name": "S1"}), "section_gid")
	c.t1 = c.createTask(M{"name": pre + "-T1", "project": c.p, "section": c.s1, "due_on": "2030-01-15"})
	c.t2 = c.createTask(M{"name": pre + "-T2", "project": c.p, "section": c.s1})
	c.syncClean("準備")

	c.featureTime()
	c.featureTemplates()
	c.featureGoals()
	c.featureSearch()
	c.featureFields()

	c.group("機能の確認のあと")
	c.syncClean("最後")
}

func localDate(offsetDays int) string {
	return time.Now().AddDate(0, 0, offsetDays).Format("2006-01-02")
}

// asanaEntries is T's time entries in Asana: gid → "minutes date".
func (c *Check) asanaEntries(task string) map[string]string {
	as, err := c.asana("/tasks/"+task+"/time_tracking_entries", "opt_fields=duration_minutes,entered_on")
	if err != nil {
		c.Rep.fail("Asana の作業時間", err.Error())
		return nil
	}
	m := map[string]string{}
	for _, e := range asArray(as) {
		m[str(e, "gid")] = fmt.Sprintf("%v %s", get(e, "duration_minutes"), str(e, "entered_on"))
	}
	return m
}

// cacheEntries is what `time list --offline` shows: gid → "minutes date".
func (c *Check) cacheEntries(task string) map[string]string {
	var l M
	if err := c.R.JSON(c.ctx, &l, "time", "list", task, "--offline"); err != nil {
		c.Rep.fail("time list --offline", err.Error())
		return nil
	}
	m := map[string]string{}
	for _, e := range asArray(get(l, "entries")) {
		m[str(e, "gid")] = fmt.Sprintf("%v %s", get(e, "duration_minutes"), str(e, "entered_on"))
	}
	return m
}

func (c *Check) entriesAre(name, task string, want map[string]string) {
	as, ca := c.asanaEntries(task), c.cacheEntries(task)
	ok := fmt.Sprint(as) == fmt.Sprint(want) && fmt.Sprint(ca) == fmt.Sprint(want)
	c.Rep.check(name, ok, fmt.Sprintf("期待 %v / キャッシュ %v / Asana %v", want, ca, as))
}

func (c *Check) featureTime() {
	c.group("#6 作業時間 (time add / edit / delete、MCP)")
	out := c.cli("time add 1h30m --date yesterday", "time", "add", c.t1, "1h30m", "--date", "yesterday")
	if g := str(out, "time_entry_gid"); strings.HasPrefix(g, "local:") {
		c.Rep.note("time add --json の time_entry_gid", "送れたあとも "+g+" のまま (本当の gid は time list で分かる)")
	}
	c.syncClean("time add")
	as := c.asanaEntries(c.t1)
	var gid string
	for g := range as {
		gid = g
		c.Made.add(&c.Made.TimeEntries, g)
	}
	c.entriesAre("90 分・昨日で 1 件", c.t1, map[string]string{gid: "90 " + localDate(-1)})
	c.cli("time edit --duration 45m --date today", "time", "edit", gid, "--duration", "45m", "--date", "today")
	c.syncClean("time edit")
	c.entriesAre("45 分・今日に変わった", c.t1, map[string]string{gid: "45 " + localDate(0)})
	_, err := c.R.CLI(c.ctx, "time", "delete", gid)
	c.Rep.check("time delete は --yes なしで断る", err != nil && strings.Contains(err.Error(), "--yes"), errText(err))
	c.entriesAre("断ったあとも残っている", c.t1, map[string]string{gid: "45 " + localDate(0)})
	c.cli("time delete --yes", "time", "delete", gid, "--yes")
	c.syncClean("time delete")
	c.entriesAre("消えた", c.t1, map[string]string{})

	mo := c.tool("add_time_entry 30m", "add_time_entry", M{"task": c.t1, "duration": "30m"})
	mg := str(mo, "time_entry_gid")
	c.Rep.check("add_time_entry が本当の gid を返す", gidRE.MatchString(mg), show(mo))
	c.Made.add(&c.Made.TimeEntries, mg)
	c.syncClean("add_time_entry")
	var lt M
	err = c.R.Tool(c.ctx, "list_time_entries", M{"task": c.t1}, &lt)
	c.Rep.check("list_time_entries に 30 分が 1 件", err == nil && len(asArray(get(lt, "entries"))) == 1 &&
		fmt.Sprint(get(asArray(get(lt, "entries"))[0], "duration_minutes")) == "30", errText(err)+show(lt))
	c.tool("update_time_entry 1h", "update_time_entry", M{"gid": mg, "duration": "1h"})
	c.syncClean("update_time_entry")
	c.entriesAre("MCP で 60 分に変わった", c.t1, map[string]string{mg: "60 " + localDate(0)})
	c.tool("delete_time_entry", "delete_time_entry", M{"gid": mg})
	c.syncClean("delete_time_entry")
	c.entriesAre("MCP で消えた", c.t1, map[string]string{})
}

// waitJob polls a job until it is done (up to a minute) and records what it made.
func (c *Check) waitJob(job any) any {
	c.Made.recordJob(job)
	g := str(job, "data", "gid")
	if g == "" {
		return job
	}
	c.Made.add(&c.Made.Jobs, g)
	var v any
	for i := 0; i < 30; i++ {
		data, err := c.R.API(c.ctx, "GET", "/jobs/"+g)
		if err != nil {
			return nil
		}
		v = M{"data": decode(data)}
		c.Made.recordJob(v)
		if st := str(v, "data", "status"); st == "succeeded" || st == "failed" {
			return v
		}
		time.Sleep(2 * time.Second)
	}
	return v
}

// projectHas checks that a project made by a template or a copy has the section S1 and the
// task T1 (by name), and the start date wanted ("" not checked).
func (c *Check) projectHas(name, gid, startOn string) {
	if !gidRE.MatchString(gid) {
		c.Rep.fail(name, "gid がない: "+gid)
		return
	}
	c.Made.add(&c.Made.Projects, gid)
	p, err := c.asana("/projects/"+gid, "opt_fields=name,start_on,due_on")
	secs, _ := c.asana("/projects/"+gid+"/sections", "opt_fields=name")
	tasks, _ := c.asana("/projects/"+gid+"/tasks", "opt_fields=name")
	ok := err == nil && slices.Contains(strs(get(secs, "#name")), "S1") && slices.Contains(strs(get(tasks, "#name")), c.Made.Prefix+"-T1")
	if startOn != "" {
		ok = ok && str(p, "start_on") == startOn
	}
	c.Rep.check(name, ok, fmt.Sprintf("%v %s / sections %v / tasks %v", err, show(p), strs(get(secs, "#name")), strs(get(tasks, "#name"))))
	for _, t := range asArray(tasks) {
		c.Made.add(&c.Made.Tasks, str(t, "gid"))
	}
}

func (c *Check) featureTemplates() {
	c.group("#2 テンプレート・複製 (template projects / new-project、project duplicate、MCP)")
	pre := c.Made.Prefix
	job, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/projects/"+c.p+"/saveAsTemplate",
		M{"data": M{"name": pre + "-template", "team": c.team, "public": false}})
	done := c.waitJob(decode(job))
	tmpl := str(done, "data", "new_project_template", "gid")
	if err != nil || tmpl == "" {
		c.Rep.fail("テンプレートを作る (saveAsTemplate)", errText(err)+show(done))
		return
	}
	var list []M
	err = c.R.JSON(c.ctx, &list, "template", "projects")
	var found M
	for _, t := range list {
		if str(t, "gid") == tmpl {
			found = t
		}
	}
	dates := strs(get(found, "requested_dates", "#name"))
	c.Rep.check("template projects にテンプレートと求める日付が出る", err == nil && found != nil && len(dates) == 1, errText(err)+show(found))
	date := "Start Date"
	if len(dates) == 1 {
		date = dates[0]
	}
	out := c.cli("template new-project --date", "template", "new-project", tmpl, pre+"-from-template", "--date", date+"=2030-04-01")
	c.projectHas("テンプレートから作ったプロジェクト (開始日 2030-04-01)", str(out, "gid"), "2030-04-01")
	out = c.cli("template new-project (日付なし = 今日)", "template", "new-project", tmpl, pre+"-from-template-today")
	c.projectHas("日付を省くと開始日は今日", str(out, "gid"), localDate(0))
	var mo M
	err = c.R.Tool(c.ctx, "create_project_from_template", M{"template": tmpl, "name": pre + "-from-template-mcp", "dates": M{date: "2030-05-01"}}, &mo)
	c.Rep.check("create_project_from_template", err == nil && str(mo, "note") == "", errText(err)+show(mo))
	c.projectHas("MCP でテンプレートから作ったプロジェクト", str(mo, "gid"), "2030-05-01")
	out = c.cli("project duplicate", "project", "duplicate", c.p, pre+"-duplicate")
	c.projectHas("複製したプロジェクト", str(out, "gid"), "")
	mo = nil
	err = c.R.Tool(c.ctx, "duplicate_project", M{"project": c.p, "name": pre + "-duplicate-mcp"}, &mo)
	c.Rep.check("duplicate_project", err == nil && str(mo, "note") == "", errText(err)+show(mo))
	c.projectHas("MCP で複製したプロジェクト", str(mo, "gid"), "")
	var tt []M
	err = c.R.JSON(c.ctx, &tt, "template", "tasks", c.p)
	c.Rep.note("template tasks (タスクテンプレートは API で作れない。#55 の準備待ち)", fmt.Sprintf("%v %d 件", err, len(tt)))
	c.syncClean("テンプレート・複製")
}

func (c *Check) featureGoals() {
	c.group("#7 ゴール・ポートフォリオ (goal list / show / periods、portfolio list / show、MCP)")
	pre := c.Made.Prefix
	periods, _ := c.asana("/time_periods", "workspace="+TestWorkspace, "opt_fields=display_name,start_on,end_on,period")
	quarter := currentQuarter(M{"data": periods}, localDate(0))
	mk := func(name string) string {
		b, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/goals", M{"data": M{"name": name, "workspace": TestWorkspace, "team": c.team,
			"owner": c.Me, "time_period": quarter}})
		g := str(decode(b), "data", "gid")
		if err != nil || g == "" {
			c.Rep.fail("ゴールを作る "+name, errText(err))
			return ""
		}
		c.Made.add(&c.Made.Goals, g)
		return g
	}
	goal, sub := mk(pre+"-goal"), mk(pre+"-subgoal")
	if goal == "" || sub == "" {
		return
	}
	for _, step := range []struct {
		path string
		body M
	}{
		{"/goals/" + goal + "/setMetricCurrentValue", M{"data": M{"current_number_value": 0.4}}},
		{"/goals/" + goal + "/addSupportingRelationship", M{"data": M{"supporting_resource": sub}}},
		{"/goals/" + goal + "/addSupportingRelationship", M{"data": M{"supporting_resource": c.p}}},
	} {
		if _, err := c.R.WriteBody(c.ctx, c.Made, "POST", step.path, step.body); err != nil {
			c.Rep.fail("POST "+step.path, err.Error())
		}
	}
	pf := func(name string) string {
		b, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/portfolios", M{"data": M{"name": name, "workspace": TestWorkspace, "public": false}})
		g := str(decode(b), "data", "gid")
		if err != nil || g == "" {
			c.Rep.fail("ポートフォリオを作る "+name, errText(err))
			return ""
		}
		c.Made.add(&c.Made.Portfolios, g)
		return g
	}
	outer, inner := pf(pre+"-portfolio"), pf(pre+"-portfolio-inner")
	for _, item := range []string{c.p, inner} {
		if _, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/portfolios/"+outer+"/addItem", M{"data": M{"item": item}}); err != nil {
			c.Rep.fail("addItem", err.Error())
		}
	}

	var gl M
	err := c.R.JSON(c.ctx, &gl, "goal", "list")
	var g1 any
	for _, g := range asArray(get(gl, "goals")) {
		if str(g, "gid") == goal {
			g1 = g
		}
	}
	c.Rep.check("goal list に作ったゴール (percentage 0.4 / 1)", err == nil && str(g1, "metric", "kind") == "percentage" &&
		fmt.Sprint(get(g1, "metric", "current")) == "0.4" && fmt.Sprint(get(g1, "metric", "target")) == "1", errText(err)+show(g1))
	var gc M
	err = c.R.JSON(c.ctx, &gc, "goal", "list", "--period", "current")
	c.Rep.check("goal list --period current にも出る", err == nil && slices.Contains(strs(get(gc, "goals", "#gid")), goal), errText(err))
	var gp []M
	err = c.R.JSON(c.ctx, &gp, "goal", "periods")
	c.Rep.check("goal periods に今の四半期", err == nil && slices.Contains(strs(get(anySlice(gp), "#gid")), quarter), errText(err))
	var gs M
	err = c.R.JSON(c.ctx, &gs, "goal", "show", goal)
	types := map[string]string{}
	for _, s := range asArray(get(gs, "supporting")) {
		types[str(s, "gid")] = str(s, "type")
	}
	c.Rep.check("goal show の supporting にサブゴールとプロジェクト", err == nil && types[sub] == "goal" && types[c.p] == "project", errText(err)+show(types))
	text, err := c.R.CLI(c.ctx, "goal", "show", goal)
	c.Rep.check("goal show の表示が「40% / 100%（40%）」", err == nil && strings.Contains(string(text), "40% / 100%（40%）"), errText(err)+string(text))
	var pl []M
	err = c.R.JSON(c.ctx, &pl, "portfolio", "list")
	c.Rep.check("portfolio list に作ったポートフォリオ", err == nil && slices.Contains(strs(get(anySlice(pl), "#gid")), outer), errText(err))
	var ps M
	err = c.R.JSON(c.ctx, &ps, "portfolio", "show", outer)
	items := map[string]string{}
	for _, it := range asArray(get(ps, "items")) {
		items[str(it, "gid")] = str(it, "type")
	}
	c.Rep.check("portfolio show の items にプロジェクトと入れ子", err == nil && items[c.p] == "project" && items[inner] == "portfolio", errText(err)+show(items))
	_, err = c.R.CLI(c.ctx, "portfolio", "show", inner)
	c.Rep.check("入れ子のポートフォリオも開ける", err == nil, errText(err))
	var mg M
	err = c.R.Tool(c.ctx, "get_goal", M{"goal": goal}, &mg)
	c.Rep.check("MCP get_goal", err == nil && str(mg, "goal", "gid") == goal && len(asArray(get(mg, "goal", "supporting"))) == 2, errText(err))
	var mp M
	err = c.R.Tool(c.ctx, "get_portfolio", M{"portfolio": outer}, &mp)
	c.Rep.check("MCP get_portfolio", err == nil && len(asArray(get(mp, "portfolio", "items"))) == 2, errText(err))
	var ml M
	err = c.R.Tool(c.ctx, "list_goals", M{}, &ml)
	c.Rep.check("MCP list_goals", err == nil && slices.Contains(strs(get(ml, "goals", "#gid")), goal), errText(err))
	var mpl M
	err = c.R.Tool(c.ctx, "list_portfolios", M{}, &mpl)
	c.Rep.check("MCP list_portfolios", err == nil && slices.Contains(strs(get(mpl, "portfolios", "#gid")), outer), errText(err))
}

func anySlice(ms []M) []any {
	out := make([]any, len(ms))
	for i, m := range ms {
		out[i] = m
	}
	return out
}

func (c *Check) featureSearch() {
	c.group("#36 Asana での検索 (search --remote、MCP search_tasks)")
	name := c.Made.Prefix + "-remote-only"
	b, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/tasks", M{"data": M{"name": name, "projects": []string{c.p}}})
	g := str(decode(b), "data", "gid")
	if err != nil || g == "" {
		c.Rep.fail("asagao を通さずにタスクを作る", errText(err))
		return
	}
	c.Made.add(&c.Made.Tasks, g)
	var local []M
	err = c.R.JSON(c.ctx, &local, "search", name)
	c.Rep.check("キャッシュだけの search では見つからない", err == nil && len(local) == 0, errText(err)+show(local))
	// Asana's search index lags behind a new task: retry for up to two minutes.
	var remote []M
	for i := 0; i < 12; i++ {
		remote = nil
		err = c.R.JSON(c.ctx, &remote, "search", name, "--remote")
		if err == nil && slices.Contains(strs(get(anySlice(remote), "#gid")), g) {
			break
		}
		time.Sleep(10 * time.Second)
	}
	c.Rep.check("search --remote で見つかる", err == nil && slices.Contains(strs(get(anySlice(remote), "#gid")), g), errText(err)+show(remote))
	var ca M
	cerr := c.R.Tool(c.ctx, "get_task", M{"gid": g, "offline": true}, &ca)
	c.Rep.check("見つけたタスクがキャッシュに入った", cerr == nil && str(ca, "task", "name") == name, errText(cerr))
	var ms M
	err = c.R.Tool(c.ctx, "search_tasks", M{"query": name, "remote": true}, &ms)
	c.Rep.check("MCP search_tasks (remote)", err == nil && slices.Contains(strs(get(ms, "tasks", "#gid")), g), errText(err)+show(get(ms, "note")))
}

// fieldOptions is the enum options of a custom field in Asana: name → how many.
func (c *Check) fieldOptions(field string) map[string]int {
	f, err := c.asana("/custom_fields/"+field, "opt_fields=enum_options.name,enum_options.enabled")
	if err != nil {
		c.Rep.fail("Asana のフィールド", err.Error())
	}
	n := map[string]int{}
	for _, name := range strs(get(f, "enum_options", "#name")) {
		n[name]++
	}
	return n
}

// taskFieldValue is T's value of a field in Asana: option names, text or number.
func (c *Check) taskFieldValue(task, field string) string {
	t, _ := c.asana("/tasks/"+task, "opt_fields=custom_fields.gid,custom_fields.display_value")
	for _, f := range asArray(get(t, "custom_fields")) {
		if str(f, "gid") == field {
			return str(f, "display_value")
		}
	}
	return "(なし)"
}

func (c *Check) attached(field string) bool {
	s, _ := c.asana("/projects/"+c.p+"/custom_field_settings", "opt_fields=custom_field.gid")
	return slices.Contains(strs(get(s, "#custom_field", "#gid")), field)
}

func (c *Check) featureFields() {
	c.group("#37 カスタムフィールド (選択肢を足す、プロジェクトへの付け外し、MCP)")
	pre := c.Made.Prefix
	mkField := func(kind, name string, options ...string) string {
		data := M{"workspace": TestWorkspace, "name": name, "resource_subtype": kind}
		if len(options) > 0 {
			var o []M
			for _, n := range options {
				o = append(o, M{"name": n})
			}
			data["enum_options"] = o
		}
		b, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/custom_fields", M{"data": data})
		g := str(decode(b), "data", "gid")
		if err != nil || g == "" {
			c.Rep.fail("フィールドを作る "+name, errText(err))
			return ""
		}
		c.Made.add(&c.Made.CustomFields, g)
		return g
	}
	enum := mkField("enum", pre+"-prio", pre+"-high", pre+"-mid")
	multi := mkField("multi_enum", pre+"-label", pre+"-A", pre+"-B")
	memo := mkField("text", pre+"-memo")
	if enum == "" || multi == "" || memo == "" {
		return
	}
	for _, f := range []string{enum, multi} {
		if _, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/projects/"+c.p+"/addCustomFieldSetting", M{"data": M{"custom_field": f}}); err != nil {
			c.Rep.fail("addCustomFieldSetting", err.Error())
		}
	}
	_, _ = c.R.CLI(c.ctx, "sync", "--full")

	low := pre + "-low"
	c.cli("task field --add-option (enum)", "task", "field", c.t1, enum, low, "--add-option")
	c.syncClean("add-option enum")
	opts := c.fieldOptions(enum)
	c.Rep.check("選択肢が 1 つ増え、タスクの値になった", opts[low] == 1 && c.taskFieldValue(c.t1, enum) == low, show(opts))
	c.cli("task field --add-option (multi_enum)", "task", "field", c.t1, multi, pre+"-B,"+pre+"-C", "--add-option")
	c.syncClean("add-option multi_enum")
	mopts := c.fieldOptions(multi)
	c.Rep.check("multi_enum に C が 1 つ増え、値が B, C", mopts[pre+"-C"] == 1 && c.taskFieldValue(c.t1, multi) == pre+"-B, "+pre+"-C",
		show(mopts)+" "+c.taskFieldValue(c.t1, multi))
	// An option added in Asana since the last sync is not added twice.
	lowest := pre + "-lowest"
	if _, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/custom_fields/"+enum+"/enum_options", M{"data": M{"name": lowest}}); err != nil {
		c.Rep.fail("Asana で選択肢を足す", err.Error())
	}
	c.cli("task field --add-option (Asana にもうある名前)", "task", "field", c.t2, enum, lowest, "--add-option")
	c.syncClean("add-option existing")
	opts = c.fieldOptions(enum)
	c.Rep.check("Asana にもうある選択肢は 2 つにならない", opts[lowest] == 1 && c.taskFieldValue(c.t2, enum) == lowest, show(opts))
	c.cli("task field --clear (multi_enum)", "task", "field", c.t1, multi, "--clear")
	c.syncClean("clear multi_enum")
	c.Rep.check("multi_enum を空にできる", c.taskFieldValue(c.t1, multi) == "", c.taskFieldValue(c.t1, multi))

	var all []M
	err := c.R.JSON(c.ctx, &all, "project", "fields", c.p, "--all")
	var memoRow any
	for _, f := range all {
		if str(f, "gid") == memo {
			memoRow = f
		}
	}
	c.Rep.check("project fields --all に付いていないフィールドが出る", err == nil && memoRow != nil && get(memoRow, "attached") == false, errText(err)+show(memoRow))
	c.cli("project fields --add", "project", "fields", c.p, "--add", memo)
	c.syncClean("project fields --add")
	c.Rep.check("フィールドがプロジェクトに付いた", c.attached(memo), "")
	c.cli("task field (text)", "task", "field", c.t1, memo, "dummy")
	c.syncClean("task field text")
	c.Rep.check("付けたフィールドに値が入った", c.taskFieldValue(c.t1, memo) == "dummy", c.taskFieldValue(c.t1, memo))
	_, err = c.R.CLI(c.ctx, "project", "fields", c.p, "--remove", memo)
	c.Rep.check("--remove は --yes なしで断る", err != nil && c.attached(memo), errText(err))
	c.cli("project fields --remove --yes", "project", "fields", c.p, "--remove", memo, "--yes")
	c.syncClean("project fields --remove")
	c.Rep.check("フィールドがプロジェクトから外れた", !c.attached(memo), "")

	var gp M
	err = c.R.Tool(c.ctx, "get_project", M{"project": c.p, "include_available_fields": true}, &gp)
	c.Rep.check("MCP get_project の available_custom_fields に memo", err == nil && slices.Contains(strs(get(gp, "available_custom_fields", "#gid")), memo),
		errText(err)+show(get(gp, "available_custom_fields")))
	var up M
	err = c.R.Tool(c.ctx, "update_project_custom_fields", M{"project": c.p, "add": []string{memo}}, &up)
	c.syncClean("update_project_custom_fields add")
	c.Rep.check("MCP update_project_custom_fields で付く", err == nil && c.attached(memo), errText(err)+show(up))
	err = c.R.Tool(c.ctx, "update_project_custom_fields", M{"project": c.p, "remove": []string{memo}}, &up)
	c.syncClean("update_project_custom_fields remove")
	c.Rep.check("MCP update_project_custom_fields で外れる", err == nil && !c.attached(memo), errText(err)+show(up))
	mcpOpt := pre + "-mcp"
	c.tool("update_task add_missing_options", "update_task", M{"gid": c.t2, "custom_fields": M{enum: mcpOpt}, "add_missing_options": true})
	c.syncClean("add_missing_options")
	opts = c.fieldOptions(enum)
	c.Rep.check("MCP add_missing_options で選択肢が足され、値になった", opts[mcpOpt] == 1 && c.taskFieldValue(c.t2, enum) == mcpOpt, show(opts))
}
