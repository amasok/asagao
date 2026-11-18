package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// goalsCLIEnv is writeCLIEnv with two goals (one of the current quarter), the year and
// the quarter, and a portfolio.
func goalsCLIEnv(t *testing.T) string {
	t.Helper()
	path := writeCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	today := time.Now()
	q := &model.TimePeriod{GID: "tpq", DisplayName: "今の四半期", Period: "Q4", StartOn: today.AddDate(0, -1, 0).Format("2006-01-02"),
		EndOn: today.AddDate(0, 1, 0).Format("2006-01-02")}
	old := &model.TimePeriod{GID: "tpo", DisplayName: "昔の年", Period: "FY", StartOn: "2001-01-01", EndOn: "2001-12-31"}
	g1 := model.Goal{GID: "g1", Name: "売上を伸ばす", Notes: "今期の柱", Status: "green", Owner: &model.Ref{GID: "u1", Name: "山田"}, TimePeriod: q,
		Metric: &model.GoalMetric{ResourceSubtype: "number", TargetNumberValue: ptrF(100), CurrentNumberValue: ptrF(40), CurrentDisplayValue: "40"}}
	must(st.ReplaceGoals(ctx, "w1", []model.Goal{g1, {GID: "g2", Name: "昔のゴール", Status: "achieved", TimePeriod: old}}))
	must(st.SaveGoal(ctx, g1,
		[]model.GoalRelationship{{SupportingResource: &model.SupportingWork{GID: "p1", ResourceType: "project", Name: "新居"}}},
		time.Now()))
	must(st.ReplaceTimePeriods(ctx, "w1", []model.TimePeriod{*q, *old}))
	must(st.SetSyncState(ctx, readmodel.KeyGoals+"w1", time.Now().UTC().Format(time.RFC3339)))
	must(st.ReplacePortfolios(ctx, "w1", []model.Portfolio{{GID: "pf1", Name: "今期の施策", CurrentStatusUpdate: &model.StatusBrief{StatusType: "at_risk"}}}))
	return path
}

func ptrF(v float64) *float64 { return &v }

// Offline, goals and portfolios are shown from the cache, narrowed by period.
func TestGoalCommandsOffline(t *testing.T) {
	goalsCLIEnv(t)
	out, err := runCLI(t, "goal", "list")
	if err != nil || !strings.Contains(out, "[順調] 売上を伸ばす · 40 / 100（40%） · 今の四半期 · 山田  (g1)") || !strings.Contains(out, "昔のゴール") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = runCLI(t, "goal", "list", "--period", "current")
	if err != nil || !strings.Contains(out, "売上を伸ばす") || strings.Contains(out, "昔のゴール") {
		t.Fatalf("current: %v\n%s", err, out)
	}
	out, err = runCLI(t, "goal", "list", "--period", "昔の年")
	if err != nil || strings.Contains(out, "売上を伸ばす") || !strings.Contains(out, "期間: 昔の年") {
		t.Fatalf("by name: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "goal", "list", "--period", "来年"); err == nil || !strings.Contains(err.Error(), "今の四半期") {
		t.Fatalf("unknown period: %v\n%s", err, out)
	}
	out, err = runCLI(t, "goal", "show", "売上")
	if err != nil || !strings.Contains(out, "今期の柱") || !strings.Contains(out, "プロジェクト  新居  (p1)") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	out, err = runCLI(t, "goal", "show", "昔")
	if err != nil || !strings.Contains(out, "オンラインで開いたときに取得します") {
		t.Fatalf("show unfetched: %v\n%s", err, out)
	}
	out, err = runCLI(t, "goal", "periods")
	if err != nil || !strings.Contains(out, "▶ 今の四半期") || !strings.Contains(out, "  昔の年") {
		t.Fatalf("periods: %v\n%s", err, out)
	}
	stdout, _, err := runCLISplit(t, "--json", "goal", "list")
	var l readmodel.GoalList
	if err != nil || json.Unmarshal([]byte(stdout), &l) != nil || len(l.Goals) != 2 || len(l.Periods) != 2 {
		t.Fatalf("--json: %v %s", err, stdout)
	}
	out, err = runCLI(t, "portfolio", "list")
	if err != nil || !strings.Contains(out, "[要注意] 今期の施策  (pf1)") {
		t.Fatalf("portfolios: %v\n%s", err, out)
	}
	out, err = runCLI(t, "portfolio", "show", "今期")
	if err != nil || !strings.Contains(out, "オンラインで開いたときに取得します") {
		t.Fatalf("portfolio show: %v\n%s", err, out)
	}
}

// A plan without goals (a recent 402) says so instead of showing an empty list.
func TestGoalCommandsPlanUnavailable(t *testing.T) {
	path := goalsCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(context.Background(), readmodel.KeyPlanUnavailable+readmodel.PlanGoals, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if out, err := runCLI(t, "goal", "list"); err == nil || !strings.Contains(err.Error(), "ゴールを API で使えません") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "portfolio", "list"); err != nil {
		t.Fatalf("portfolios hidden too: %v\n%s", err, out)
	}
}

// goalsCLIAsana answers the goal and portfolio endpoints; goalsStatus answers GET /goals
// with that status instead.
type goalsCLIAsana struct {
	mu          gosync.Mutex
	goalsStatus int
	paths       []string
}

func (f *goalsCLIAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	status := f.goalsStatus
	f.mu.Unlock()
	switch r.URL.Path {
	case "/goals":
		if status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"errors":[{"message":"Payment Required"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"gid":"g5","name":"新しいゴール\u001b[2J","status":"yellow"}]}`)
	case "/time_periods":
		fmt.Fprint(w, `{"data":[]}`)
	case "/goals/g5":
		fmt.Fprint(w, `{"data":{"gid":"g5","name":"新しいゴール","notes":"取ってきた説明","status":"yellow"}}`)
	case "/goals/777":
		fmt.Fprint(w, `{"data":{"gid":"777","name":"一覧にないゴール","workspace":{"gid":"w1"}}}`)
	case "/goal_relationships":
		fmt.Fprint(w, `{"data":[{"resource_subtype":"subgoal","supporting_resource":{"gid":"g6","resource_type":"goal","name":"子"}}]}`)
	case "/portfolios":
		fmt.Fprint(w, `{"data":[{"gid":"pf9","name":"新しいポートフォリオ"}]}`)
	case "/portfolios/pf9":
		fmt.Fprint(w, `{"data":{"gid":"pf9","name":"新しいポートフォリオ"}}`)
	case "/portfolios/pf9/items":
		fmt.Fprint(w, `{"data":[{"gid":"p1","resource_type":"project","name":"新居","current_status_update":{"status_type":"off_track"}}]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
	}
}

func runOnlineCLI(t *testing.T, f http.Handler, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv(auth.EnvToken, "test-pat")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cmd := newRoot(&app{baseURL: srv.URL})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// Online, goals and portfolios are fetched first; a goal not cached is found by fetching
// the list, or taken as a gid; a 402 hides goals.
func TestGoalCommandsOnline(t *testing.T) {
	goalsCLIEnv(t)
	f := &goalsCLIAsana{}
	out, _, err := runOnlineCLI(t, f, "goal", "list")
	if err != nil || !strings.Contains(out, "[要注意] 新しいゴール") || strings.Contains(out, "売上を伸ばす") || strings.Contains(out, "\x1b") {
		t.Fatalf("list: %v\n%q", err, out)
	}
	out, _, err = runOnlineCLI(t, f, "goal", "show", "新しい")
	if err != nil || !strings.Contains(out, "取ってきた説明") || !strings.Contains(out, "サブゴール  子  (g6)") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	out, _, err = runOnlineCLI(t, f, "goal", "show", "777")
	if err != nil || !strings.Contains(out, "一覧にないゴール") {
		t.Fatalf("show by gid: %v\n%s", err, out)
	}
	out, _, err = runOnlineCLI(t, f, "portfolio", "show", "新しいポートフォリオ")
	if err != nil || !strings.Contains(out, "プロジェクト  [遅れ] 新居  (p1)") {
		t.Fatalf("portfolio show: %v\n%s", err, out)
	}
	f.mu.Lock()
	f.goalsStatus = http.StatusPaymentRequired
	f.mu.Unlock()
	if out, _, err := runOnlineCLI(t, f, "goal", "list"); err == nil || !strings.Contains(err.Error(), "402") {
		t.Fatalf("402: %v\n%s", err, out)
	}
}
