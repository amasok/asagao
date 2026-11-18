// Command realcheck checks asagao against the real Asana, in the test workspace only
// (#31, #47). It refuses to start unless every path points into ~/asagao-test and the
// account is in exactly that workspace. It writes only inside two projects it creates
// (named asagao-realcheck-<time>), and deletes everything it made at the end, also when
// a step fails or it is interrupted.
//
//	go build -o ~/asagao-test/bin/asagao-realcheck ./cmd/asagao
//	go run ./scripts/realcheck -bin ~/asagao-test/bin/asagao-realcheck -out report.md
//
// With -probe it only reads (which paid features answer 402 on this plan).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

func main() {
	os.Exit(realMain())
}

func realMain() int {
	home, _ := os.UserHomeDir()
	bin := flag.String("bin", filepath.Join(home, "asagao-test", "bin", "asagao-realcheck"), "asagao built from this branch")
	out := flag.String("out", "", "write the report (Markdown) here as well (default: the run directory)")
	probe := flag.Bool("probe", false, "only read: probe the paid features")
	cleanupPrefix := flag.String("cleanup", "", "only delete what an earlier run left (its prefix, asagao-realcheck-<date>-<time>)")
	syncBench := flag.String("syncbench", "", "compare the first sync of builds (#54): main=<bin>,lazy=<bin>")
	briefF := flag.Bool("brief", false, "check project briefs (#60) instead of the outbox kinds")
	dates := flag.Bool("dates", false, "check the start date of tasks (#57) instead of the outbox kinds")
	features := flag.Bool("features", false, "check the features of #2, #6, #7, #36 and #37 instead of the outbox kinds")
	colors := flag.Bool("colors", false, "only probe the color names (a tag and a project of the run)")
	shapesF := flag.Bool("shapes", false, "also save the raw responses of the paid features (in the run directory)")
	templates := flag.Bool("templates", false, "with -shapes: also save a project template from the check project and instantiate it")
	pause := flag.Duration("pause", 2*time.Second, "pause after every command (free plan: 150 requests a minute)")
	flag.Parse()

	runID := time.Now().Format("20060102-150405")
	env := NewEnv(home, *bin, os.Getenv("PATH"), runID)
	if err := CheckEnv(env); err != nil {
		fmt.Fprintln(os.Stderr, "realcheck: refusing to run:", err)
		return 2
	}
	if err := env.MakeRunDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "realcheck:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rep := &Report{}
	r := &Runner{Env: env, Pause: *pause, Log: os.Stderr}
	prefix := "asagao-realcheck-" + runID
	if *cleanupPrefix != "" {
		if !runPrefix.MatchString(*cleanupPrefix) {
			fmt.Fprintln(os.Stderr, "realcheck: -cleanup takes a run's prefix like asagao-realcheck-20261119-051348")
			return 2
		}
		prefix = *cleanupPrefix
	}
	c := &Check{R: r, Rep: rep, CleanupOnly: *cleanupPrefix != "", Made: &Created{Prefix: prefix}, Shapes: *shapesF, Templates: *templates, ColorsOnly: *colors, Features: *features, Dates: *dates, Brief: *briefF, SyncBench: *syncBench}
	go func() {
		// The first Ctrl-C stops the steps and cleanup runs; a second one leaves at once
		// and says what is left in Asana.
		<-ctx.Done()
		stop()
		again := make(chan os.Signal, 1)
		signal.Notify(again, os.Interrupt, syscall.SIGTERM)
		<-again
		fmt.Fprintln(os.Stderr, "realcheck: interrupted twice; these may be left in Asana:", show(c.Made))
		os.Exit(130)
	}()
	code := run(ctx, c, *probe)
	md := rep.Markdown()
	fmt.Print(md)
	if *out == "" {
		*out = filepath.Join(env.Run, "report.md")
	}
	if err := os.WriteFile(*out, []byte(md), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Fprintln(os.Stderr, "realcheck: report:", *out)
	return code
}

func run(ctx context.Context, c *Check, probeOnly bool) (code int) {
	r, rep := c.R, c.Rep
	data, err := r.CLI(ctx, "api", "/users/me", "-f", "opt_fields=gid,workspaces.gid")
	if err != nil {
		fmt.Fprintln(os.Stderr, "realcheck: users/me:", err)
		return 2
	}
	if c.Me, err = CheckMe(data); err != nil {
		fmt.Fprintln(os.Stderr, "realcheck: refusing to run:", err)
		return 2
	}
	team, err := r.API(ctx, "GET", "/teams/"+TestTeam, "opt_fields=organization.gid")
	if err != nil || str(decode(team), "organization", "gid") != TestWorkspace {
		fmt.Fprintln(os.Stderr, "realcheck: refusing to run: team", TestTeam, "is not in the test workspace:", err)
		return 2
	}
	c.team = TestTeam
	c.ctx = ctx
	if c.CleanupOnly {
		// What a stopped run left: collected by its prefix (typeahead, the projects' tasks)
		// and deleted with the same checks as any cleanup.
		rep.group("片付けだけ (" + c.Made.Prefix + ")")
		left := r.Cleanup(ctx, c.Made)
		rep.check("前の実行が残したものを全部消した", len(left) == 0, strings.Join(left, "\n"))
		rep.note("消したもの", fmt.Sprintf("projects %d / tasks %d / tags %d", len(c.Made.Projects), len(c.Made.Tasks), len(c.Made.Tags)))
		c.leftovers()
		return rep.exitCode()
	}

	c.group("sync (受信トレイ・依存関係・承認の取得)")
	c.syncClean("最初の同期 (全件)")
	c.inbox()
	c.probes()
	if probeOnly {
		return rep.exitCode()
	}

	// Cleanup runs whatever happens below; a fresh context so Ctrl-C still cleans up.
	defer func() {
		if p := recover(); p != nil && p != errStop {
			rep.fail("panic", fmt.Sprint(p))
		}
		cctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer cancel()
		c.ctx = cctx
		rep.group("片付け")
		left := r.Cleanup(cctx, c.Made)
		rep.check("作ったものを全部消した", len(left) == 0, strings.Join(left, "\n"))
		c.leftovers()
		c.syncClean("片付けのあとの同期")
		code = rep.exitCode()
	}()
	switch {
	case c.ColorsOnly:
		c.colorNames()
	case c.Features:
		c.features()
	case c.Dates:
		c.dates()
	case c.Brief:
		c.brief()
	case c.SyncBench != "":
		c.syncbench(parseBench(c.SyncBench))
	default:
		c.writes()
	}
	return rep.exitCode()
}

// runPrefix is the prefix of one run (-cleanup takes nothing broader).
var runPrefix = regexp.MustCompile(`^asagao-realcheck-[0-9]{8}-[0-9]{6}$`)

// errStop aborts the write steps (a step whose result the next ones need failed).
var errStop = fmt.Errorf("stop")

// Check holds the run's state and the shared helpers of the steps.
type Check struct {
	R    *Runner
	Rep  *Report
	Me   string
	Made *Created
	ctx  context.Context

	team     string
	p, p2    string // projects
	s1, s2   string // sections
	t1, t2   string // tasks
	t3, c1   string
	c2, copy string
	tag      string
	cf       map[string]string // custom fields made for the run, by type

	Shapes, Templates, ColorsOnly, Features, Dates, Brief bool   // -shapes, -templates, -colors, -features, -dates, -brief
	SyncBench                                             string // -syncbench
	CleanupOnly                                           bool   // -cleanup
}

func (c *Check) group(name string) { c.Rep.group(name) }

// must stops the run when err is set (the following steps depend on this one).
func (c *Check) must(name string, err error) {
	if err != nil {
		c.Rep.fail(name, err.Error())
		panic(errStop)
	}
}

func (c *Check) ctxOr() context.Context {
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

// syncClean runs `asagao sync` and checks that nothing is left unsent.
func (c *Check) syncClean(name string) {
	ctx := c.ctxOr()
	_, err := c.R.CLI(ctx, "sync")
	c.Rep.check(name+": sync が成功する", err == nil, errText(err))
	var ob []map[string]any
	err = c.R.JSON(ctx, &ob, "outbox", "list")
	detail := errText(err)
	if len(ob) > 0 {
		b, _ := json.MarshalIndent(ob, "", "  ")
		detail = string(b)
	}
	c.Rep.check(name+": 未送信の一覧が空", err == nil && len(ob) == 0, detail)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
