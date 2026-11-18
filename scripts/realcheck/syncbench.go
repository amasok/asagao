package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A bench variant: an asagao build, and whether its config turns lazy_projects off.
type benchVariant struct {
	Name  string
	Bin   string
	Eager bool // [sync] lazy_projects = false in a copy of the test config
	// Offline: `task list` takes --offline (the lazy builds; main's reads only the cache).
	Offline bool
}

// syncbench compares the first sync of two builds from an empty store (#54): time and the
// number of requests (counted from Go's HTTP/2 debug log), My Tasks after it, a project
// fetched when it is opened, completed tasks after open ones, Events after that, and a
// second sync. It first makes prefixed projects with open and completed tasks, so the
// difference shows; they are deleted at the end. Nothing else is written.
func (c *Check) syncbench(variants []benchVariant) {
	pre := c.Made.Prefix
	c.group("#54 遅延同期: 準備 (前置き付きのプロジェクト 4 つ、各 15 件の未完了と 10 件の完了)")
	pause := c.R.Pause
	c.R.Pause = 500 * time.Millisecond // 120 writes a minute at most
	var projects []string
	for i := 1; i <= 4; i++ {
		out := c.tool("create_project", "create_project", M{"name": fmt.Sprintf("%s-P%d", pre, i), "team": c.team})
		p := str(out, "project_gid")
		if p == "" {
			c.must("create_project の gid", fmt.Errorf("%v", out))
		}
		c.Made.add(&c.Made.Projects, p)
		projects = append(projects, p)
		for j := 1; j <= 25; j++ {
			c.gate()
			data := M{"name": fmt.Sprintf("%s-P%d-T%02d", pre, i, j), "projects": []string{p}, "completed": j > 15}
			if j <= 3 {
				data["assignee"] = c.Me
			}
			b, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/tasks", M{"data": data})
			if g := str(decode(b), "data", "gid"); g != "" {
				c.Made.add(&c.Made.Tasks, g)
			} else {
				c.Rep.fail("タスクを作る", errText(err))
			}
		}
	}
	c.R.Pause = pause
	myOpen := c.asanaCount("/tasks", "assignee=me", "workspace="+TestWorkspace, "completed_since=now", "opt_fields=gid")
	p1Open := 15

	var tokenCopies []string
	defer func() {
		for _, f := range tokenCopies { // the copied token is not kept after the run
			_ = os.Remove(f)
		}
	}()
	rows := []string{"| | 最初の sync | 2 回目の sync | P1 を開く (task list --project) | P2 を開く (MCP list_tasks) |", "|---|---|---|---|---|"}
	for _, v := range variants {
		c.group("#54 遅延同期: " + v.Name)
		b := c.newBench(v)
		if b == nil {
			continue
		}
		if v.Eager {
			tokenCopies = append(tokenCopies, filepath.Join(b.env.Config, "asagao", "token.json"))
		}
		first := b.run("--json", "sync")
		var mine []M
		_ = b.json(&mine, b.local("task", "list")...)
		c.Rep.check(v.Name+": 最初の sync でマイタスクが全部ある", len(mine) == myOpen, fmt.Sprintf("キャッシュ %d / Asana %d", len(mine), myOpen))
		second := b.run("--json", "sync")
		var p1 []M
		open := b.run("--json", "task", "list", "--project", projects[0])
		_ = jsonInto(open.out, &p1)
		c.Rep.check(v.Name+": P1 を開くと未完了がその場で全部取れる", len(p1) == p1Open, fmt.Sprintf("%d 件 (期待 %d) %s", len(p1), p1Open, open.errText()))
		var p1all []M
		_ = b.json(&p1all, b.local("task", "list", "--project", projects[0], "--all")...)
		c.Rep.note(v.Name+": P1 を開いた直後のキャッシュ (完了も含む)", fmt.Sprintf("%d 件 (Asana は 25 件)", len(p1all)))
		later := b.run("--json", "sync")
		p1all = nil
		_ = b.json(&p1all, b.local("task", "list", "--project", projects[0], "--all")...)
		c.Rep.check(v.Name+": そのあとの sync で完了済みも入る", len(p1all) == 25, fmt.Sprintf("%d 件", len(p1all)))

		// Events: a task of the opened P1 renamed in Asana is followed by the next sync.
		target := str(p1[0], "gid")
		newName := fmt.Sprintf("%s-renamed-%s", pre, strings.ReplaceAll(v.Name, " ", "-"))
		if _, err := c.R.WriteBody(c.ctx, c.Made, "PUT", "/tasks/"+target, M{"data": M{"name": newName}}); err != nil {
			c.Rep.fail("Asana でタスクの名前を変える", err.Error())
		}
		time.Sleep(3 * time.Second)
		events := b.run("--json", "sync")
		var after []M
		_ = b.json(&after, b.local("task", "list", "--project", projects[0])...)
		c.Rep.check(v.Name+": 開いたプロジェクトは Events の差分で追える", slices.Contains(strs(get(anySlice(after), "#name")), newName),
			fmt.Sprintf("sync のリクエスト %d", events.requests))

		mcp := b.mcpListTasks(projects[1])
		c.Rep.check(v.Name+": MCP list_tasks で開いていないプロジェクトもその場で取れる", mcp.count == p1Open, fmt.Sprintf("%d 件 %s", mcp.count, mcp.err))
		rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s | %s |", v.Name, first.cell(), second.cell(), open.cell(), mcp.cell()))
		c.Rep.note(v.Name+": 完了済みを取った sync / Events の sync", later.cell()+" / "+events.cell())
	}
	c.group("#54 遅延同期: 比較")
	c.Rep.lines = append(c.Rep.lines, rows...)
}

func (c *Check) asanaCount(path string, fields ...string) int {
	data, err := c.R.API(c.ctx, "GET", path, fields...)
	if err != nil {
		c.Rep.fail("Asana "+path, err.Error())
		return -1
	}
	return len(asArray(decode(data)))
}

type bench struct {
	c   *Check
	env Env
	v   benchVariant
}

// local is a `task list` that reads only the cache.
func (b *bench) local(args ...string) []string {
	if b.v.Offline {
		return append(args, "--offline")
	}
	return args
}

type benchResult struct {
	out      []byte
	err      error
	requests int
	took     time.Duration
	count    int
}

func (r benchResult) cell() string {
	if r.err != nil {
		return "エラー " + oneLine(r.err.Error())
	}
	if r.requests < 0 {
		return fmt.Sprintf("未計測・%.1f 秒", r.took.Seconds())
	}
	return fmt.Sprintf("%d 回・%.1f 秒", r.requests, r.took.Seconds())
}

func (r benchResult) errText() string { return errText(r.err) }

// newBench makes the variant's own run directory (an empty store) and, for Eager, a copy
// of the test config with lazy_projects = false.
func (c *Check) newBench(v benchVariant) *bench {
	env := NewEnv(filepath.Dir(c.R.Env.Root), v.Bin, os.Getenv("PATH"), filepath.Base(c.R.Env.Run)+"-"+strings.ReplaceAll(v.Name, " ", "-"))
	if v.Eager {
		cfg := filepath.Join(env.Run, "config")
		if err := os.MkdirAll(filepath.Join(cfg, "asagao"), 0o700); err != nil {
			c.Rep.fail(v.Name, err.Error())
			return nil
		}
		for _, f := range []string{"token.json", "config.toml"} {
			b, err := os.ReadFile(filepath.Join(env.Config, "asagao", f))
			if err != nil {
				c.Rep.fail(v.Name, err.Error())
				return nil
			}
			if f == "config.toml" {
				b = append(b, []byte("\n[sync]\nlazy_projects = false\n")...)
			}
			if err := os.WriteFile(filepath.Join(cfg, "asagao", f), b, 0o600); err != nil {
				c.Rep.fail(v.Name, err.Error())
				return nil
			}
		}
		env.Config = cfg
		for i, kv := range env.Vars {
			if strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
				env.Vars[i] = "XDG_CONFIG_HOME=" + cfg
			}
		}
	}
	if err := CheckEnv(env); err != nil {
		c.Rep.fail(v.Name+": 止める条件", err.Error())
		return nil
	}
	if err := env.MakeRunDirs(); err != nil {
		c.Rep.fail(v.Name, err.Error())
		return nil
	}
	return &bench{c: c, env: env, v: v}
}

// run runs the variant's asagao. The requests are what asagao itself says it sent.
func (b *bench) run(args ...string) benchResult {
	defer time.Sleep(b.c.R.Pause)
	ctx, cancel := context.WithTimeout(b.c.ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.env.Bin, append([]string{"--no-context"}, args...)...)
	// Never GODEBUG=http2debug: Go would log the authorization header (the token).
	cmd.Env = slices.Clone(b.env.Vars)
	cmd.Dir = b.env.Work
	detach(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	err := cmd.Run()
	r := benchResult{out: out.Bytes(), took: time.Since(start), requests: asagaoRequests(out.Bytes(), errb.String())}
	fmt.Fprintf(b.c.R.Log, "  $ asagao %s (%d requests, %s)\n", strings.Join(args, " "), r.requests, r.took.Round(time.Millisecond))
	if err != nil {
		r.err = fmt.Errorf("%w: %s", err, lastLines(errb.String()))
	}
	return r
}

var requestsLine = regexp.MustCompile(`リクエスト (\d+) 回`)

// asagaoRequests is the number of requests asagao says it sent: "requests" of
// `--json sync`, or the stderr line of a project fetched when opened ("… / リクエスト M
// 回"); 0 for a task list that fetched nothing (it says nothing then); -1 when asagao
// does not say (main's build: not counted).
func asagaoRequests(stdout []byte, stderr string) int {
	var v struct {
		Requests *int `json:"requests"`
	}
	if json.Unmarshal(stdout, &v) == nil && v.Requests != nil {
		return *v.Requests
	}
	if m := requestsLine.FindStringSubmatch(stderr); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return -1
}

func (b *bench) json(v any, args ...string) error {
	r := b.run(append([]string{"--json"}, args...)...)
	if r.err != nil {
		return r.err
	}
	return jsonInto(r.out, v)
}

// mcpListTasks asks the variant's MCP server for a project's open tasks.
func (b *bench) mcpListTasks(project string) benchResult {
	r := &Runner{Env: b.env, Pause: b.c.R.Pause, Log: b.c.R.Log}
	defer r.Close()
	var out M
	start := time.Now()
	err := r.Tool(b.c.ctx, "list_tasks", M{"project": project}, &out)
	n := -1 // main's build does not say
	if f, ok := get(out, "fetched", "requests").(float64); ok {
		n = int(f)
	}
	return benchResult{err: err, count: len(asArray(get(out, "tasks"))), took: time.Since(start), requests: n}
}

func jsonInto(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode: %w: %.200s", err, b)
	}
	return nil
}

// lastLines is the end of a stderr, for an error message.
func lastLines(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > 5 {
		keep = keep[len(keep)-5:]
	}
	return strings.Join(keep, " / ")
}

// parseBench reads -syncbench main=<bin>,lazy=<bin>: main, lazy, and lazy with
// lazy_projects = false.
func parseBench(arg string) []benchVariant {
	m := map[string]string{}
	for _, kv := range strings.Split(arg, ",") {
		k, v, _ := strings.Cut(kv, "=")
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	var out []benchVariant
	if m["main"] != "" {
		out = append(out, benchVariant{Name: "main", Bin: m["main"]})
	}
	if m["lazy"] != "" {
		out = append(out, benchVariant{Name: "lazy", Bin: m["lazy"], Offline: true},
			benchVariant{Name: "lazy (lazy_projects=false)", Bin: m["lazy"], Eager: true, Offline: true})
	}
	return out
}
