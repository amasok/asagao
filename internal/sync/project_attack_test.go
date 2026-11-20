package sync

import (
	"context"
	"strings"
	"testing"
)

// failing PUTs (429, 5xx, 402) keep the edit queued once, the local value, and send nothing twice.
func TestAttackPushUpdateProjectTransientFailures(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503} {
		ctx := context.Background()
		ps, s, st := setupProject(t)
		if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"color": "light-red"}, now); err != nil {
			t.Fatal(err)
		}
		ps.putStatus = status
		res, _ := s.Push(ctx)
		if res.Sent != 0 {
			t.Errorf("%d: sent = %+v", status, res)
		}
		ops, _ := st.ListOps(ctx)
		if len(ops) != 1 {
			t.Fatalf("%d: ops = %+v (edit lost or doubled)", status, ops)
		}
		if o, _ := st.ProjectOverview(ctx, "p1"); o.Color != "light-red" {
			t.Errorf("%d: local color lost: %q", status, o.Color)
		}
		// recovers
		ps.putStatus = 0
		res, err := s.Push(ctx)
		if err != nil || res.Sent != 1 || ps.color != "light-red" {
			t.Fatalf("%d: retry = %+v %v color=%s", status, res, err, ps.color)
		}
		if ops, _ := st.ListOps(ctx); len(ops) != 0 {
			t.Errorf("%d: left %+v", status, ops)
		}
	}
}

// clearing due date alone, and start+due together, are sent as null.
func TestAttackPushClearDates(t *testing.T) {
	ctx := context.Background()
	ps, s, st := setupProject(t)
	ps.startOn = "2026-12-01"
	p, _ := s.API.Project(ctx, "p1")
	_ = saveProject(ctx, s.Store, p)
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"start_on": "", "due_on": ""}, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push %+v %v", res, err)
	}
	put := ps.puts[0]
	if v, ok := put["due_on"]; !ok || v != nil {
		t.Errorf("due_on = %#v", v)
	}
	if v, ok := put["start_on"]; !ok || v != nil {
		t.Errorf("start_on = %#v", v)
	}
}

// start date alone while Asana has no due date: must not send an invalid request silently.
func TestAttackStartWithoutDueRejectedLocally(t *testing.T) {
	ctx := context.Background()
	ps, s, st := setupProject(t)
	ps.dueOn = ""
	p, _ := s.API.Project(ctx, "p1")
	_ = saveProject(ctx, s.Store, p)
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"start_on": "2026-12-01"}, now); err == nil {
		t.Fatal("start without due accepted")
	}
}

// Control characters from Asana never reach the store's readers: owner name, team name, brief.
func TestAttackTermsafeFromAsana(t *testing.T) {
	ctx := context.Background()
	ps, s, st := setupProject(t)
	ps.brief = "b1"
	ps.owner = "u9"
	ps.ownerName = "太郎\x1b[31m赤\x07"
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	o, _ := st.ProjectOverview(ctx, "p1")
	if o.Owner == nil || strings.ContainsAny(o.Owner.Name, "\x1b\x07") {
		t.Errorf("owner name: %+v", o.Owner)
	}
	if o.Brief != nil && strings.ContainsAny(o.Brief.Title+o.Brief.Text+o.Brief.HTMLText, "\x1b\x07") {
		t.Errorf("brief: %+v", o.Brief)
	}
}
