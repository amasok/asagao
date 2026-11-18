package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// goalsServer is an online server whose Asana has a goal g1 (with a subgoal) and a
// portfolio pf1 holding project p1.
func goalsServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st := seed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/goals":
			fmt.Fprint(w, `{"data":[{"gid":"g1","name":"売上を伸ばす","status":"green","notes":"説明",
				"time_period":{"gid":"tp1","display_name":"Q4 FY26","start_on":"2026-10-01","end_on":"2026-12-31"}}]}`)
		case "/time_periods":
			fmt.Fprint(w, `{"data":[{"gid":"tp1","display_name":"Q4 FY26","period":"Q4","start_on":"2026-10-01","end_on":"2026-12-31"}]}`)
		case "/goals/g1":
			fmt.Fprint(w, `{"data":{"gid":"g1","name":"売上を伸ばす","status":"green","notes":"説明","html_notes":"<body><strong>説明</strong></body>"}}`)
		case "/goal_relationships":
			fmt.Fprint(w, `{"data":[{"resource_subtype":"subgoal","supporting_resource":{"gid":"g2","resource_type":"goal","name":"子"}}]}`)
		case "/portfolios":
			fmt.Fprint(w, `{"data":[{"gid":"pf1","name":"今期の施策"}]}`)
		case "/portfolios/pf1":
			fmt.Fprint(w, `{"data":{"gid":"pf1","name":"今期の施策"}}`)
		case "/portfolios/pf1/items":
			fmt.Fprint(w, `{"data":[{"gid":"p1","resource_type":"project","name":"新居"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return &Server{Store: st, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		api.MaxRetries = 0
		return &sync.Syncer{API: api, Store: st}, nil
	}}, st
}

// The goal tools fetch from Asana and read the cache; get_goal finds a goal by name after
// fetching the list, and gives its description as Markdown.
func TestGoalTools(t *testing.T) {
	s, _ := goalsServer(t)
	cs := connect(t, s)
	var goals GoalsOutput
	call(t, cs, "list_goals", map[string]any{"period": "Q4 FY26"}, &goals)
	if len(goals.Goals) != 1 || goals.Goals[0].Name != "売上を伸ばす" || len(goals.Periods) != 1 || goals.Note != "" {
		t.Fatalf("list_goals = %+v", goals)
	}
	var g GoalOutput
	call(t, cs, "get_goal", map[string]any{"goal": "売上"}, &g)
	if g.Goal.GID != "g1" || !strings.Contains(g.Markdown, "**説明**") || len(g.Goal.Supporting) != 1 || g.Goal.HTMLNotes != "" {
		t.Fatalf("get_goal = %+v", g)
	}
	res := call(t, cs, "list_goals", map[string]any{"period": "来年"}, nil)
	if msg := toolError(res); !strings.Contains(msg, "Q4 FY26") {
		t.Fatalf("unknown period: %q", msg)
	}
	var ps PortfoliosOutput
	call(t, cs, "list_portfolios", nil, &ps)
	if len(ps.Portfolios) != 1 {
		t.Fatalf("list_portfolios = %+v", ps)
	}
	var p PortfolioOutput
	call(t, cs, "get_portfolio", map[string]any{"portfolio": "今期の施策"}, &p)
	if len(p.Portfolio.Items) != 1 || p.Portfolio.Items[0].GID != "p1" {
		t.Fatalf("get_portfolio = %+v", p)
	}
}

// Offline, the tools read the cache; an unknown goal says it is not there.
func TestGoalToolsOffline(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	if err := st.ReplaceGoals(ctx, "w1", []model.Goal{{GID: "g1", Name: "売上"}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	var goals GoalsOutput
	call(t, cs, "list_goals", nil, &goals)
	if len(goals.Goals) != 1 {
		t.Fatalf("list_goals = %+v", goals)
	}
	if msg := toolError(call(t, cs, "get_goal", map[string]any{"goal": "なし"}, nil)); msg == "" {
		t.Fatal("an unknown goal is not an error")
	}
}

// Goals or portfolios that Asana refused with a 402 are not offered.
func TestGoalToolsHiddenByPlan(t *testing.T) {
	ctx := context.Background()
	st := seed(t)
	if err := st.SetSyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanGoals, testNow.Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	if slices.Contains(got, "list_goals") || slices.Contains(got, "get_goal") || !slices.Contains(got, "list_portfolios") {
		t.Fatalf("tools = %v", got)
	}
}
