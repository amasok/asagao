package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// startOnKey is the store's mark that start dates are hidden in the test workspace
// (plan.StartOn): set after a 402, and since #62 also when Asana answers a write that
// sets a start date with the task keeping another one.
const startOnKey = "plan_unavailable:" + plan.StartOn + ":" + TestWorkspace

// startOnDropped is the reason recorded with a start date taken for dropped.
var startOnDropped = readmodel.PlanUnavailableMessage(plan.StartOn)

// startMark reads the mark from this run's store (only reads; sqlite3 -readonly).
func (c *Check) startMark() (string, error) {
	db := filepath.Join(c.R.Env.Data, "asagao", "store.db")
	if !inside(c.R.Env.Run, db) {
		return "", fmt.Errorf("%s is not under %s", db, c.R.Env.Run)
	}
	// -init: not the user's ~/.sqliterc (its .headers or .mode would change the output).
	out, err := exec.CommandContext(c.ctxOr(), "sqlite3", "-init", "/dev/null", "-batch", "-noheader", "-list",
		"-readonly", db, "SELECT value FROM sync_state WHERE key = '"+startOnKey+"'").Output()
	if err != nil {
		return "", fmt.Errorf("sqlite3: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// startDrops lists the start dates asagao took for dropped by Asana (#62) that were not
// in seen, and adds them to seen.
func (c *Check) startDrops(seen *[]string) ([]M, error) {
	var ds []M
	if err := c.R.JSON(c.ctxOr(), &ds, "outbox", "discarded", "--limit", "200"); err != nil {
		return nil, fmt.Errorf("outbox discarded: %w", err)
	}
	var out []M
	for _, d := range ds {
		id := fmt.Sprint(d["id"])
		if slices.Contains(*seen, id) {
			continue
		}
		*seen = append(*seen, id)
		if str(d, "reason") == startOnDropped {
			out = append(out, d)
		}
	}
	return out, nil
}

// noStartMark checks that a start date Asana kept was not taken for dropped: no mark in
// the store and no dropped start date since the last call (a paid plan keeps them).
func (c *Check) noStartMark(step string, seen *[]string) {
	v, err := c.startMark()
	drops, derr := c.startDrops(seen)
	err = errors.Join(err, derr)
	detail := strings.TrimSpace(errText(err) + " ")
	if v != "" {
		detail += "印 " + v + " "
	}
	if len(drops) > 0 {
		detail += "捨てた変更 " + show(drops)
	}
	c.Rep.check(step+": 開始日の印 (plan.StartOn) も捨てた変更もない", err == nil && v == "" && len(drops) == 0, strings.TrimSpace(detail))
}

// startMarkNote records the mark and the dropped start dates without judging them.
func (c *Check) startMarkNote(step string, seen *[]string) {
	v, err := c.startMark()
	drops, derr := c.startDrops(seen)
	err = errors.Join(err, derr)
	mark := "なし"
	if v != "" {
		mark = v
	}
	if err != nil {
		mark = err.Error()
	}
	c.Rep.note(step, fmt.Sprintf("開始日の印: %s / 開始日を捨てた記録: %s", mark, show(drops)))
}

// milestoneStart records what Asana answers to a start date on a milestone (#62): a 400,
// a 200 that drops it, or a 200 that keeps it; and what asagao does with it when sent
// through the outbox (a dropped one would mark start dates as unavailable in the
// workspace). Only tasks of the run's project are written.
func (c *Check) milestoneStart() {
	pre := c.Made.Prefix
	c.group("#62 マイルストーンに開始日を送る")
	var seen []string
	if _, err := c.startDrops(&seen); err != nil { // what was dropped before is not this step's
		c.Rep.fail("outbox discarded", err.Error())
	}

	// Made through asagao with a due date, then turned into a milestone.
	m := c.createTask(M{"name": pre + "-M1", "project": c.p, "due_on": "2030-05-10"})
	c.syncClean("マイルストーンにするタスク")
	c.cli("task type milestone", "task", "type", m, "milestone")
	c.syncClean("task type milestone")
	c.reportDates("マイルストーン M1 (送る前)", m)

	// Through asagao first (a start date Asana kept would make a later one a conflict).
	before := c.outbox()
	out, err := c.R.CLI(c.ctx, "--json", "task", "start", m, "2030-05-03")
	c.Rep.note("asagao: task start (マイルストーン)", strings.TrimSpace(oneLine(string(out))+" "+errText(err)))
	sout, serr := c.R.CLI(c.ctx, "sync")
	c.Rep.note("asagao: そのあとの sync", strings.TrimSpace(oneLine(string(sout))+" "+errText(serr)))
	c.startMarkNote("asagao: マイルストーンに送ったあと", &seen)
	c.reportDates("asagao で送ったあとの M1", m)
	c.discardFailed("マイルストーンの開始日", before)

	// Asana itself: an update and a create.
	b, aerr := c.R.WriteBody(c.ctx, c.Made, "PUT", "/tasks/"+m, M{"data": M{"start_on": "2030-05-01"}})
	c.Rep.note("Asana: マイルストーンに PUT start_on", answerBody(b, aerr))
	c.reportDates("PUT のあとの M1 (Asana の GET)", m)
	b, aerr = c.R.WriteBody(c.ctx, c.Made, "POST", "/tasks", M{"data": M{
		"name": pre + "-M2", "projects": []string{c.p}, "resource_subtype": "milestone",
		"start_on": "2030-05-02", "due_on": "2030-05-12",
	}})
	if g := str(decode(b), "data", "gid"); gidRE.MatchString(g) {
		c.Made.add(&c.Made.Tasks, g)
	}
	c.Rep.note("Asana: start_on 付きでマイルストーンを POST", answerBody(b, aerr))

}

// answerBody is answer with the whole answer of Asana (the error carries its body).
func answerBody(b []byte, err error) string {
	if err != nil {
		return "エラー " + oneLine(err.Error())
	}
	v := decode(b)
	return fmt.Sprintf("成功 resource_subtype=%v start_on=%v due_on=%v / %s",
		get(v, "data", "resource_subtype"), get(v, "data", "start_on"), get(v, "data", "due_on"), oneLine(string(b)))
}
