package main

import (
	"fmt"
	"strings"
)

// dates checks the start date of tasks (#57) on the real Asana through the CLI and MCP
// (-dates): set, change, clear, with the due date, refused after the due date, and what
// Asana answers to a due date cleared under a start date. Only tasks of a project of the
// run are written. After each start date sent, it checks that asagao did not take it for
// dropped (#62), and last records what Asana does with a start date on a milestone.
func (c *Check) dates() {
	pre := c.Made.Prefix
	c.group("#57 開始日 (task create --start、task start、MCP の start_on)")
	out := c.tool("create_project", "create_project", M{"name": pre, "team": c.team})
	c.p = str(out, "project_gid")
	if c.p == "" {
		c.must("create_project の gid", fmt.Errorf("%v", out))
	}
	c.Made.add(&c.Made.Projects, c.p)
	var seen []string // dropped changes already looked at (#62)
	c.startDrops(&seen)

	// CLI: a task made with a start and a due date (task create prints no gid once sent).
	c.cli("task create --start --due", "task", "create", pre+"-T1", "--project", c.p, "--start", "2030-03-01", "--due", "2030-03-10")
	c.syncClean("task create --start")
	c.t1 = c.taskByName(pre + "-T1")
	c.datesAre("作るときの開始日と期日", c.t1, "2030-03-01", "2030-03-10")
	c.noStartMark("#62 作るとき (CLI)", &seen)

	c.cli("task start (変える)", "task", "start", c.t1, "2030-03-03")
	c.syncClean("task start")
	c.datesAre("開始日だけ変わった", c.t1, "2030-03-03", "2030-03-10")
	c.noStartMark("#62 開始日を変える (CLI)", &seen)

	c.cli("task start --due (一緒に変える)", "task", "start", c.t1, "2030-03-05", "--due", "2030-03-20")
	c.syncClean("task start --due")
	c.datesAre("開始日と期日が一緒に変わった", c.t1, "2030-03-05", "2030-03-20")
	c.noStartMark("#62 期日と一緒に変える (CLI)", &seen)

	_, err := c.R.CLI(c.ctx, "--json", "task", "start", c.t1, "2030-03-25")
	c.Rep.check("期日より後の開始日は CLI で断る", err != nil, errText(err))
	var mo M
	err = c.R.Tool(c.ctx, "update_task", M{"gid": c.t1, "start_on": "2030-03-30"}, &mo)
	c.Rep.check("期日より後の開始日は MCP でも断る", err != nil, errText(err)+show(mo))
	err = c.R.Tool(c.ctx, "update_task", M{"gid": c.t1, "start_on": "2030-03-21", "due_on": "2030-03-15"}, &mo)
	c.Rep.check("一緒に変えても、期日より後なら断る", err != nil, errText(err)+show(mo))
	c.syncClean("断ったあと")
	c.datesAre("断ったあとは変わらない", c.t1, "2030-03-05", "2030-03-20")

	c.cli("task start \"\" (外す)", "task", "start", c.t1, "")
	c.syncClean("task start \"\"")
	c.datesAre("開始日だけ外れた", c.t1, "", "2030-03-20")
	c.noStartMark("#62 外す (CLI)", &seen)

	// MCP: set again, then clear the due date (asagao clears the start date with it).
	c.tool("update_task start_on", "update_task", M{"gid": c.t1, "start_on": "2030-03-12"})
	c.syncClean("update_task start_on")
	c.datesAre("MCP で開始日を付けた", c.t1, "2030-03-12", "2030-03-20")
	c.noStartMark("#62 付ける (MCP)", &seen)
	before := c.outbox()
	err = c.R.Tool(c.ctx, "update_task", M{"gid": c.t1, "due_on": ""}, &mo)
	_, _ = c.R.CLI(c.ctx, "sync")
	c.Rep.note("開始日があるタスクで、期日だけ外す (MCP)", describeWrite(mo, err))
	c.discardFailed("期日だけ外す", before)
	c.reportDates("期日だけ外したあと", c.t1)
	c.tool("update_task due_on と start_on を外す", "update_task", M{"gid": c.t1, "due_on": "", "start_on": ""})
	c.syncClean("両方外す")
	c.datesAre("両方外れた", c.t1, "", "")
	c.noStartMark("#62 期日と一緒に外す (MCP)", &seen)

	// MCP create_task with both.
	c.t2 = c.createTask(M{"name": pre + "-T2", "project": c.p, "start_on": "2030-04-01", "due_on": "2030-04-05"})
	c.syncClean("create_task start_on")
	c.datesAre("MCP で作るときの開始日と期日", c.t2, "2030-04-01", "2030-04-05")
	c.noStartMark("#62 作るとき (MCP)", &seen)

	// No due date: refused here; what Asana itself answers is recorded.
	c.t3 = c.createTask(M{"name": pre + "-T3", "project": c.p})
	c.syncClean("期日なしのタスク")
	_, err = c.R.CLI(c.ctx, "--json", "task", "start", c.t3, "2030-04-01")
	c.Rep.check("期日のないタスクに開始日だけは CLI で断る", err != nil, errText(err))
	b, aerr := c.R.WriteBody(c.ctx, c.Made, "PUT", "/tasks/"+c.t3, M{"data": M{"start_on": "2030-04-01"}})
	c.Rep.note("Asana: 期日のないタスクに start_on だけ", answer(b, aerr))
	// T2 has 2030-04-01 → 2030-04-05: a start after the due date first, then the due date cleared.
	b, aerr = c.R.WriteBody(c.ctx, c.Made, "PUT", "/tasks/"+c.t2, M{"data": M{"start_on": "2030-04-09"}})
	c.Rep.note("Asana: 期日より後の start_on", answer(b, aerr))
	b, aerr = c.R.WriteBody(c.ctx, c.Made, "PUT", "/tasks/"+c.t2, M{"data": M{"due_on": nil}})
	c.Rep.note("Asana: 開始日のあるタスクで due_on を null", answer(b, aerr))
	_, _ = c.R.CLI(c.ctx, "sync")
	c.reportDates("Asana に直接送ったあとの T2", c.t2)
	c.syncClean("最後")

	// Last: a milestone may make asagao mark start dates as unavailable (#62).
	c.milestoneStart()
}

func answer(b []byte, err error) string {
	if err != nil {
		return "エラー " + oneLine(err.Error())
	}
	v := decode(b)
	return fmt.Sprintf("成功 start_on=%v due_on=%v", get(v, "data", "start_on"), get(v, "data", "due_on"))
}

// taskByName finds a task of P by name in Asana and records it.
func (c *Check) taskByName(name string) string {
	ts, err := c.asana("/projects/"+c.p+"/tasks", "opt_fields=name")
	for _, t := range asArray(ts) {
		if str(t, "name") == name {
			g := str(t, "gid")
			c.Made.add(&c.Made.Tasks, g)
			return g
		}
	}
	c.must("タスク "+name, fmt.Errorf("not in Asana: %v", err))
	return ""
}

// datesAre checks a task's start and due dates in Asana and in the cache.
func (c *Check) datesAre(name, gid, start, due string) {
	as, err := c.asana("/tasks/"+gid, "opt_fields=start_on,due_on")
	ca := c.cacheTask(gid)
	ok1, d1 := same("start_on", str(ca, "start_on"), str(as, "start_on"), start)
	ok2, d2 := same("due_on", str(ca, "due_on"), str(as, "due_on"), due)
	c.Rep.check(name, err == nil && ok1 && ok2, strings.TrimSpace(errText(err)+" "+d1+" "+d2))
}

func (c *Check) reportDates(name, gid string) {
	as, _ := c.asana("/tasks/"+gid, "opt_fields=start_on,due_on")
	ca := c.cacheTask(gid)
	c.Rep.note(name, fmt.Sprintf("Asana start_on=%v due_on=%v / キャッシュ start_on=%v due_on=%v",
		get(as, "start_on"), get(as, "due_on"), get(ca, "start_on"), get(ca, "due_on")))
}
