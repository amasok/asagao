package sync

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/ui/format"
)

// #62: a create whose answer was lost, found in Asana by reconcile, is checked for its
// start date as an answered one is: Asana may have created the task without it.

func TestReconcileCreateStartOn(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		ignoreStart, omitStart bool
		dropped                bool // the start date reported as dropped, and hidden in w1
		wantStart              string
	}{
		{"dropped", true, false, true, ""},
		{"kept", false, false, false, "2026-12-01"},
		{"not listed", true, true, false, ""}, // start_on not in the list: not judged
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, st := setupStart(t, nil)
			st.SetDescribeChange(format.DescribeChange)
			ctx := context.Background()
			s.now = func() time.Time { return now }
			f.ignoreStart, f.omitStart, f.loseCreate = tc.ignoreStart, tc.omitStart, true
			gid, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "見積もり", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
			if err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil || res.Failed != 0 || res.Sent != 1 {
				t.Fatalf("push = %+v, %v", res, err)
			}
			if real, err := st.RealGID(ctx, gid); err != nil || real != "new1" {
				t.Fatalf("real gid = %q, %v: the task Asana made was not found", real, err)
			}
			if ops, _ := st.ListOps(ctx); len(ops) != 0 {
				t.Fatalf("ops = %+v", ops)
			}
			_, hidden, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1")
			if !tc.dropped {
				if len(res.Discarded) != 0 || hidden {
					t.Fatalf("discarded %+v, start dates hidden %v", res.Discarded, hidden)
				}
				if got, _ := st.GetTask(ctx, "new1"); got == nil || got.StartOn != tc.wantStart {
					t.Fatalf("new1 = %+v", got)
				}
				return
			}
			if len(res.Discarded) != 1 {
				t.Fatalf("discarded = %+v", res.Discarded)
			}
			if d := res.Discarded[0]; d.TaskGID != "new1" || !strings.Contains(d.Reason, "タスクの開始日") || !strings.Contains(d.Summary, "開始日を 2026-12-01") {
				t.Fatalf("discard = %+v", d)
			}
			if ds, _ := st.PendingDiscards(ctx); len(ds) != 1 {
				t.Fatalf("pending discards = %+v", ds)
			}
			if !hidden {
				t.Fatal("start dates not hidden in w1")
			}
			if got, _ := st.GetTask(ctx, "new1"); got == nil || got.StartOn != "" || got.DueOn != "2026-12-10" || got.Pending {
				t.Fatalf("new1 = %+v", got)
			}
		})
	}
}

// The tasks reconcile looks among (lists, and a task read) say whether Asana's answer had
// start_on, as an answer to a write does (TestWrittenStartOnKnown).
func TestListedStartOnKnown(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		known      bool
		start      string
	}{
		{"date", `{"gid":"x","start_on":"2026-12-01"}`, true, "2026-12-01"},
		{"null", `{"gid":"x","start_on":null}`, true, ""},
		{"absent", `{"gid":"x"}`, false, ""},
		{"number", `{"gid":"x","start_on":20261201}`, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			list, _ := answerClient(t, `{"data":[`+tc.data+`]}`)
			one, _ := answerClient(t, `{"data":`+tc.data+`}`)
			for name, call := range map[string]func() (*asana.Task, error){
				"tasks": func() (*asana.Task, error) {
					ts, err := list.Tasks(ctx, asana.TaskQuery{Project: "p1"})
					if err != nil || len(ts) != 1 {
						return nil, fmt.Errorf("%d tasks, %v", len(ts), err)
					}
					return &ts[0], nil
				},
				"subtasks": func() (*asana.Task, error) {
					ts, err := list.Subtasks(ctx, "p")
					if err != nil || len(ts) != 1 {
						return nil, fmt.Errorf("%d tasks, %v", len(ts), err)
					}
					return &ts[0], nil
				},
				"task": func() (*asana.Task, error) { return one.Task(ctx, "x") },
			} {
				got, err := call()
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if got.StartOnKnown != tc.known || got.StartOn != tc.start || got.GID != "x" {
					t.Errorf("%s: got known=%v start=%q gid=%q", name, got.StartOnKnown, got.StartOn, got.GID)
				}
			}
		})
	}
}
