package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const orderEventFields = "action,resource,resource.resource_type,parent,parent.resource_type,change.field,change.action,created_at"

// order checks whether reordering tasks within the same section emits Asana Events that
// keep the cache in Asana's order (#69), and checks the custom ID 500 fallback (#65) with
// a read-only `git scan --dry-run` against the test workspace.
func (c *Check) order() {
	pre := c.Made.Prefix
	c.group("#69 同じセクション内の並べ替えと Events (#51)")
	out := c.tool("create_project", "create_project", M{"name": pre, "team": c.team})
	c.p = str(out, "project_gid")
	if c.p == "" {
		c.must("create_project の gid", fmt.Errorf("%v", out))
	}
	c.Made.add(&c.Made.Projects, c.p)

	c.s1 = str(c.tool("create_section S1", "create_section", M{"project": c.p, "name": "S1"}), "section_gid")
	if c.s1 == "" {
		c.must("create_section の gid", fmt.Errorf("empty section_gid"))
	}
	c.Made.add(&c.Made.Sections, c.s1)

	t1 := c.createTask(M{"name": pre + "-T1", "project": c.p, "section": c.s1})
	t2 := c.createTask(M{"name": pre + "-T2", "project": c.p, "section": c.s1})
	t3 := c.createTask(M{"name": pre + "-T3", "project": c.p, "section": c.s1})
	c.syncClean("タスク 3 件作成")

	initAsana, err := c.sectionTaskGIDs(c.s1)
	if err != nil {
		c.must("Asana のセクション並び取得", err)
	}
	initCache, err := c.cacheSectionTaskGIDs(c.p, c.s1)
	if err != nil {
		c.must("キャッシュのセクション並び取得", err)
	}
	c.Rep.note("初期の並び (Asana)", fmt.Sprintf("%v (T1=%s T2=%s T3=%s)", initAsana, t1, t2, t3))
	c.Rep.check("初期の並びが Asana とキャッシュで一致", len(initAsana) == 3 && slices.Equal(initCache, initAsana),
		fmt.Sprintf("Asana=%v cache=%v", initAsana, initCache))

	// Get a fresh project Events sync token and drain any creation events.
	token, err := c.freshEventsToken(c.p)
	if err != nil {
		c.must("Events トークン取得", err)
	}
	var drained []any
	drained, token, err = c.pollEvents(c.p, token)
	if err != nil {
		c.must("Events の読み捨て", err)
	}
	c.Rep.note("並べ替え前の残余 Events 件数", fmt.Sprint(len(drained)))

	// Reorder externally in Asana (bypassing asagao): move the last task before the first.
	first, mid, last := initAsana[0], initAsana[1], initAsana[2]
	if _, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/sections/"+c.s1+"/addTask",
		M{"data": M{"task": last, "insert_before": first}}); err != nil {
		c.must("Asana で同じセクション内の並べ替え", err)
	}
	afterAsana, err := c.sectionTaskGIDs(c.s1)
	if err != nil {
		c.must("並べ替え後の Asana 並び取得", err)
	}
	wantAfter := []string{last, first, mid}
	c.Rep.check("Asana で同一セクション内の並べ替えが効いた", slices.Equal(afterAsana, wantAfter),
		fmt.Sprintf("got=%v want=%v", afterAsana, wantAfter))

	// Poll raw Asana Events to observe what Asana emits on an intra-section reorder.
	var events []any
	for i := 0; i < 3; i++ {
		time.Sleep(2 * time.Second)
		var batch []any
		batch, token, err = c.pollEvents(c.p, token)
		if err != nil {
			c.Rep.note("raw Events 取得エラー", err.Error())
			break
		}
		events = append(events, batch...)
		if len(events) > 0 {
			break
		}
	}
	c.Rep.note("同一セクション内並べ替えで Asana が返した Events", show(events))

	// Sync incrementally via asagao and observe what incremental Events sync does.
	c.syncClean("外部並べ替え後の通常同期")
	afterCache, err := c.cacheSectionTaskGIDs(c.p, c.s1)
	if err != nil {
		c.Rep.fail("通常同期後のキャッシュ並び取得", err.Error())
	} else {
		c.Rep.note("通常同期後のキャッシュ並び (Asana が Events を返さないため全件同期まで変わらない)",
			fmt.Sprintf("Asana=%v cache=%v", afterAsana, afterCache))
	}

	// Full sync picks up the external intra-section reorder.
	if _, err := c.R.CLI(c.ctx, "sync", "--full"); err != nil {
		c.Rep.fail("外部並べ替え後の全件同期 (sync --full)", err.Error())
	} else {
		fullCache, err := c.cacheSectionTaskGIDs(c.p, c.s1)
		c.Rep.check("外部の同一セクション内並べ替えは sync --full でキャッシュに反映される",
			err == nil && slices.Equal(fullCache, afterAsana),
			fmt.Sprintf("err=%v Asana=%v cache=%v", err, afterAsana, fullCache))
	}

	// External reorder #2: move `first` before `last` in Asana while cache still has
	// [last, first, mid], making Asana [first, last, mid]. Then move `mid` before
	// `first` with asagao WITHOUT `sync --full`: pushMove refreshes s1's order directly
	// so both Asana and the cache become [mid, first, last] (#69).
	if _, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/sections/"+c.s1+"/addTask",
		M{"data": M{"task": first, "insert_before": last}}); err != nil {
		c.must("Asana で 2 回目の同一セクション内並べ替え", err)
	}
	c.cli("task move --before (同一セクション)", "task", "move", mid, "--before", first)
	c.syncClean("task move 後の通常同期")
	wantMoved := []string{mid, first, last}
	asanaMoved, aerr := c.sectionTaskGIDs(c.s1)
	cacheMoved, cerr := c.cacheSectionTaskGIDs(c.p, c.s1)
	c.Rep.check("外部並べ替えのあとでも asagao task move 直後の並びが Asana・キャッシュとも一致 (#69)",
		aerr == nil && cerr == nil && slices.Equal(asanaMoved, wantMoved) && slices.Equal(cacheMoved, wantMoved),
		fmt.Sprintf("err=%v/%v Asana=%v cache=%v want=%v", aerr, cerr, asanaMoved, cacheMoved, wantMoved))

	c.orderCustomIDCheck()
}

// orderCustomIDCheck tests #65 (500 -> search API fallback) against the real workspace's
// RC-1 task via a read-only `git scan --dry-run` (writes nothing to Asana).
func (c *Check) orderCustomIDCheck() {
	c.group("#65 custom ID の 500 フォールバック実機確認 (読み取りだけ)")
	repo := filepath.Join(c.R.Env.Work, "git-customid")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		c.must("git-customid ディレクトリ", err)
	}
	for _, args := range [][]string{
		{"-C", repo, "init"},
		{"-C", repo, "config", "user.name", "realcheck"},
		{"-C", repo, "config", "user.email", "realcheck@example.invalid"},
		{"-C", repo, "commit", "--allow-empty", "-m", "see asana#RC-1 and asana#rc-1 and asana#RC-99999"},
	} {
		cmd := exec.CommandContext(c.ctx, "git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			c.must("git "+strings.Join(args, " "), fmt.Errorf("%w: %s", err, out))
		}
	}
	out, err := c.R.CLI(c.ctx, "--json", "git", "scan", "--dry-run", "-C", repo, "HEAD")
	if err != nil {
		c.Rep.fail("git scan --dry-run", err.Error()+"\n"+string(out))
		return
	}
	var scanRes M
	if jerr := json.Unmarshal(out, &scanRes); jerr != nil {
		c.Rep.fail("git scan --dry-run JSON デコード", fmt.Sprintf("%v: %s", jerr, out))
		return
	}
	c.Rep.note("git scan --dry-run (RC-1 / rc-1 / RC-99999)", show(scanRes))

	acts := map[string]M{}
	for _, cm := range asArray(scanRes["commits"]) {
		for _, a := range asArray(get(cm, "actions")) {
			if m, ok := a.(M); ok {
				acts[str(m, "custom_id")] = m
			}
		}
	}
	rc1 := acts["RC-1"]
	c.Rep.check("asana#RC-1 が実在するタスク GID に解決される",
		rc1 != nil && gidRE.MatchString(str(rc1, "task_gid")) && str(rc1, "result") != "not_found",
		show(rc1))
	c.Rep.note("asana#rc-1 の結果", show(acts["rc-1"]))
	c.Rep.check("存在しない asana#RC-99999 は not_found になる",
		acts["RC-99999"] != nil && str(acts["RC-99999"], "result") == "not_found" && str(acts["RC-99999"], "task_gid") == "",
		show(acts["RC-99999"]))
}

func (c *Check) sectionTaskGIDs(section string) ([]string, error) {
	as, err := c.asana("/sections/"+section+"/tasks", "opt_fields=gid")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, item := range asArray(as) {
		if g := str(item, "gid"); g != "" {
			out = append(out, g)
		}
	}
	return out, nil
}

func (c *Check) cacheSectionTaskGIDs(project, section string) ([]string, error) {
	var tasks []M
	if err := c.R.JSON(c.ctx, &tasks, "task", "list", "--project", project, "--section", section, "--all", "--offline"); err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tasks {
		if g := str(t, "gid"); g != "" {
			out = append(out, g)
		}
	}
	return out, nil
}

func (c *Check) freshEventsToken(resource string) (string, error) {
	out, err := c.R.CLI(c.ctx, "api", "/events", "-f", "resource="+resource, "-f", "opt_fields="+orderEventFields)
	var body struct {
		Sync string `json:"sync"`
	}
	if jerr := json.Unmarshal(out, &body); jerr == nil && body.Sync != "" {
		return body.Sync, nil
	}
	return "", fmt.Errorf("sync token not found (%v): %s", err, out)
}

func (c *Check) pollEvents(resource, token string) ([]any, string, error) {
	var all []any
	for i := 0; i < 10; i++ {
		out, err := c.R.CLI(c.ctx, "api", "/events", "-f", "resource="+resource, "-f", "sync="+token, "-f", "opt_fields="+orderEventFields)
		if err != nil {
			return all, token, fmt.Errorf("%w: %s", err, out)
		}
		var body struct {
			Data    []any  `json:"data"`
			Sync    string `json:"sync"`
			HasMore bool   `json:"has_more"`
		}
		if jerr := json.Unmarshal(out, &body); jerr != nil {
			return all, token, jerr
		}
		all = append(all, body.Data...)
		if body.Sync != "" {
			token = body.Sync
		}
		if !body.HasMore {
			break
		}
	}
	return all, token, nil
}
