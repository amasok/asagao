package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// web checks what needs the project the user prepared in Asana's web app (#55): custom
// task types (#59), creating a task from a task template (#2) and the custom ID lookup
// (#36). The run adds only prefixed tasks to that project (deleted at the end) and changes
// the type and status of the user's typed task, which it puts back.
func (c *Check) web(project, userTask string) {
	pre := c.Made.Prefix
	c.Made.HostProjects = []string{project}
	c.Made.HostTasks = []string{userTask}
	s := &shapes{c: c, dir: filepath.Join(c.R.Env.Run, "shapes")}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		c.must("shapes", err)
	}
	c.group("#55 の準備 (読み取りだけ)")
	p, err := c.asana("/projects/"+project, "opt_fields=name,workspace.gid")
	if err != nil || str(p, "workspace", "gid") != TestWorkspace || str(p, "name") != "asagao-realcheck-web" {
		c.must("確認用の Web のプロジェクト", fmt.Errorf("%v %s", err, show(p)))
	}
	types := s.get("custom-types", "/custom_types", "project="+project,
		"opt_fields=name,status_options.name,status_options.completion_state,status_options.color,status_options.enabled")
	tp := asArray(get(types, "data"))
	if len(tp) == 0 {
		c.must("カスタムタイプ", fmt.Errorf("none: %s", show(types)))
	}
	typeGID, typeName := str(tp[0], "gid"), str(tp[0], "name")
	status := map[string]string{} // name → gid
	states := map[string]string{} // name → completion_state
	for _, o := range asArray(get(tp[0], "status_options")) {
		status[str(o, "name")] = str(o, "gid")
		states[str(o, "name")] = str(o, "completion_state")
	}
	c.Rep.note("カスタムタイプ", fmt.Sprintf("%s: %v", typeName, states))
	var incomplete, complete []string
	for n, st := range states {
		if st == "Complete" {
			complete = append(complete, n)
		} else {
			incomplete = append(incomplete, n)
		}
	}
	c.Rep.check("仮定 1: completion_state は Complete / Incomplete", len(complete) > 0 && len(incomplete) > 0 &&
		len(complete)+len(incomplete) == len(states) && func() bool {
		for _, st := range states {
			if st != "Complete" && st != "Incomplete" {
				return false
			}
		}
		return true
	}(), show(states))
	u0, _ := c.asana("/tasks/"+userTask, "opt_fields=name,resource_subtype,custom_type.gid,custom_type.name,custom_type_status_option.gid,custom_type_status_option.name,completed")
	s.save("user-task-before", "GET /tasks/"+userTask, []byte(show(M{"data": u0})), nil)
	if str(u0, "resource_subtype") != "custom" || str(u0, "custom_type", "gid") != typeGID {
		c.must("ユーザーのタスク", fmt.Errorf("not of the custom type: %s", show(u0)))
	}
	// Whatever happens below (a failed step, Ctrl-C), the user's task gets its status back.
	defer func() {
		ua, err := c.R.API(context.Background(), "GET", "/tasks/"+userTask, "opt_fields=custom_type_status_option.gid,completed")
		if err == nil && str(decode(ua), "custom_type_status_option", "gid") == str(u0, "custom_type_status_option", "gid") {
			return
		}
		_, err = c.R.WriteBody(context.Background(), c.Made, "PUT", "/tasks/"+userTask, M{"data": M{
			"resource_subtype": "custom", "custom_type": typeGID, "custom_type_status_option": str(u0, "custom_type_status_option", "gid")}})
		c.Rep.check("ユーザーのタスクを元の種類とステータスに戻した (途中で止まったとき)", err == nil, errText(err))
	}()
	c.syncClean("最初")

	c.group("#59 カスタムタスクタイプ (task type / task status、MCP)")
	var ut M
	err = c.R.Tool(c.ctx, "get_task", M{"gid": userTask}, &ut)
	c.Rep.check("get_task にユーザーのタスクの種類とステータスが出る", err == nil && strings.Contains(show(ut), typeName) &&
		strings.Contains(show(ut), str(u0, "custom_type_status_option", "name")), errText(err))
	var ts M
	err = c.R.JSON(c.ctx, &ts, "task", "show", userTask)
	c.Rep.check("task show にユーザーのタスクの種類とステータスが出る", err == nil && str(ts, "custom_type", "gid") == typeGID &&
		str(ts, "custom_type_status", "name") == str(u0, "custom_type_status_option", "name"), errText(err)+show(get(ts, "custom_type"))+show(get(ts, "custom_type_status")))
	t := c.createTask(M{"name": pre + "-typed", "project": project})
	c.syncClean("タスクを作る")
	typeIs := func(name, subtype, ctype, st string, completed bool) {
		as, err := c.asana("/tasks/"+t, "opt_fields=resource_subtype,custom_type.gid,custom_type_status_option.name,completed")
		ok := err == nil && str(as, "resource_subtype") == subtype && str(as, "custom_type", "gid") == ctype &&
			str(as, "custom_type_status_option", "name") == st && boolean(as, "completed") == completed
		c.Rep.check(name, ok, errText(err)+show(as))
	}
	c.cli("task type <カスタムタイプ>", "task", "type", t, typeName)
	c.syncClean("task type custom")
	as, _ := c.asana("/tasks/"+t, "opt_fields=custom_type_status_option.name")
	first := str(as, "custom_type_status_option", "name")
	typeIs("カスタムタイプになった (ステータスは "+first+")", "custom", typeGID, first, states[first] == "Complete")
	for _, st := range append(incomplete, complete...) {
		c.cli("task status "+st, "task", "status", t, st)
		c.syncClean("task status")
		typeIs(fmt.Sprintf("ステータス %s (%s)", st, states[st]), "custom", typeGID, st, states[st] == "Complete")
	}
	c.Rep.note("仮定 4: 完了のステータスにするとタスクも完了になる", "上の「ステータス」の行の completed で確かめた")
	// From the complete status back to an incomplete one: the task is open again.
	c.cli("task status (完了から戻す) "+incomplete[0], "task", "status", t, incomplete[0])
	c.syncClean("task status 戻す")
	typeIs(fmt.Sprintf("完了のステータスから %s に戻すと、タスクも未完了に戻る", incomplete[0]), "custom", typeGID, incomplete[0], false)
	c.cli("task type milestone", "task", "type", t, "milestone")
	c.syncClean("task type milestone")
	typeIs("仮定 3: milestone に戻すと種類が消える", "milestone", "", "", false)
	c.cli("task type <カスタムタイプ> (戻したあと)", "task", "type", t, typeName)
	c.syncClean("task type custom again")
	as, _ = c.asana("/tasks/"+t, "opt_fields=custom_type_status_option.name,completed")
	typeIs("milestone からもう一度カスタムタイプにできる", "custom", typeGID, str(as, "custom_type_status_option", "name"), boolean(as, "completed"))
	c.cli("task type task", "task", "type", t, "task")
	c.syncClean("task type task")
	typeIs("仮定 3: default_task に戻すと種類が消える", "default_task", "", "", boolean(as, "completed"))
	c.tool("MCP update_task custom_type + custom_type_status", "update_task", M{"gid": t, "custom_type": typeName, "custom_type_status": incomplete[0]})
	c.syncClean("MCP custom_type")
	typeIs("MCP で種類とステータスを一緒に", "custom", typeGID, incomplete[0], false)

	// What Asana itself takes (assumptions 2 and 5), on the run's own tasks.
	b, aerr := c.R.WriteBody(c.ctx, c.Made, "PUT", "/tasks/"+t, M{"data": M{"resource_subtype": "default_task"}})
	c.Rep.note("Asana: PUT resource_subtype=default_task", answerType(b, aerr))
	b, aerr = c.R.WriteBody(c.ctx, c.Made, "PUT", "/tasks/"+t, M{"data": M{"custom_type": typeGID}})
	c.Rep.note("仮定 2: PUT custom_type だけ (resource_subtype なし)", answerType(b, aerr))
	b, aerr = c.R.WriteBody(c.ctx, c.Made, "PUT", "/tasks/"+t, M{"data": M{"resource_subtype": "custom", "custom_type": typeGID}})
	c.Rep.note("仮定 2: PUT resource_subtype=custom と custom_type", answerType(b, aerr))
	b, aerr = c.R.WriteBody(c.ctx, c.Made, "POST", "/tasks", M{"data": M{"name": pre + "-typed-post", "projects": []string{project},
		"resource_subtype": "custom", "custom_type": typeGID}})
	c.Rep.note("仮定 5: POST に種類を付ける", answerType(b, aerr))
	if g := str(decode(b), "data", "gid"); g != "" {
		c.Made.add(&c.Made.Tasks, g)
	}

	// A type Asana refuses: the run's own project has no custom types.
	out := c.tool("create_project (種類のないプロジェクト)", "create_project", M{"name": pre, "team": c.team})
	c.p = str(out, "project_gid")
	c.Made.add(&c.Made.Projects, c.p)
	t2 := c.createTask(M{"name": pre + "-untyped", "project": c.p})
	c.syncClean("種類のないプロジェクトのタスク")
	before := c.outbox()
	var mo M
	err = c.R.Tool(c.ctx, "update_task", M{"gid": t2, "name": pre + "-untyped-renamed", "custom_type": typeGID}, &mo)
	_, _ = c.R.CLI(c.ctx, "sync")
	c.Rep.note("種類のないプロジェクトのタスクに種類と名前を一緒に (MCP)", describeWrite(mo, err))
	a2, _ := c.asana("/tasks/"+t2, "opt_fields=name,resource_subtype,custom_type.gid")
	c.Rep.note("そのあとの Asana", show(a2))
	c.discardFailed("種類を断られたとき", before)

	// The user's task: change its status, then put it back as it was.
	other := incomplete[0]
	if other == str(u0, "custom_type_status_option", "name") && len(incomplete) > 1 {
		other = incomplete[1]
	}
	c.cli("task status (ユーザーのタスク)", "task", "status", userTask, other)
	c.syncClean("ユーザーのタスクのステータス")
	ua, _ := c.asana("/tasks/"+userTask, "opt_fields=custom_type_status_option.name,completed")
	c.Rep.check("ユーザーのタスクのステータスが変わった", str(ua, "custom_type_status_option", "name") == other, show(ua))
	c.cli("task status (元に戻す)", "task", "status", userTask, str(u0, "custom_type_status_option", "name"))
	c.syncClean("元に戻す")
	ua, _ = c.asana("/tasks/"+userTask, "opt_fields=resource_subtype,custom_type.name,custom_type_status_option.name,completed")
	c.Rep.check("ユーザーのタスクが元に戻った", str(ua, "custom_type_status_option", "name") == str(u0, "custom_type_status_option", "name") &&
		boolean(ua, "completed") == boolean(u0, "completed") && str(ua, "custom_type", "name") == str(u0, "custom_type", "name"), show(ua)+" / 前 "+show(u0))

	c.group("#2 タスクテンプレート (template tasks / new-task、MCP)")
	var tt []M
	err = c.R.JSON(c.ctx, &tt, "template", "tasks", project)
	tmpl := ""
	for _, x := range tt {
		if str(x, "name") == "asagao-realcheck-tt" {
			tmpl = str(x, "gid")
		}
	}
	c.Rep.check("template tasks に asagao-realcheck-tt", err == nil && tmpl != "", errText(err)+show(tt))
	if tmpl != "" {
		out := c.cli("template new-task", "template", "new-task", project, tmpl, "--name", pre+"-from-template")
		g := str(out, "gid")
		c.Rep.check("テンプレートからタスクを作れた (ジョブを待って本当の gid)", gidRE.MatchString(g), show(out))
		if gidRE.MatchString(g) {
			c.Made.add(&c.Made.Tasks, g)
			a, _ := c.asana("/tasks/"+g, "opt_fields=name,projects.gid")
			c.Rep.check("Asana にそのタスクがあり、プロジェクトに入っている", str(a, "name") == pre+"-from-template" &&
				strings.Contains(show(get(a, "projects")), project), show(a))
		}
		var mt M
		err = c.R.Tool(c.ctx, "create_task_from_template", M{"project": project, "template": tmpl, "name": pre + "-from-template-mcp"}, &mt)
		c.Rep.check("MCP create_task_from_template", err == nil && gidRE.MatchString(str(mt, "gid")), errText(err)+show(mt))
		c.Made.add(&c.Made.Tasks, str(mt, "gid"))
		c.syncClean("テンプレート")
	}

	c.group("#36 custom ID (読み取りだけ)")
	for _, id := range []string{"RC-1", "rc-1", "Rc-1", "RC-2"} {
		s.get("custom-id-"+id, "/workspaces/"+TestWorkspace+"/tasks/custom_id/"+id, "opt_fields=name")
	}
	c.Rep.note("custom ID の答え", "shapes の custom-id-* に保存 (status は 2 行目)")
	c.syncClean("最後")
}

func answerType(b []byte, err error) string {
	if err != nil {
		return "エラー " + oneLine(err.Error())
	}
	v := decode(b)
	return fmt.Sprintf("成功 resource_subtype=%v custom_type=%v status=%v completed=%v", get(v, "data", "resource_subtype"),
		get(v, "data", "custom_type", "name"), get(v, "data", "custom_type_status_option", "name"), get(v, "data", "completed"))
}
