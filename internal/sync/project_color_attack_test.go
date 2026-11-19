package sync

import (
	"context"
	"testing"
)

// "none" is read as "" only for the color: real conflicts on it are still found, and
// other fields keep conflicting next to it (#31).
func TestPushUpdateProjectColorAttack(t *testing.T) {
	for _, c := range []struct {
		name                   string
		base, local, asana     string // base is the cached color before the edit
		other                  bool   // due_on also changed in Asana
		wantConflict, wantSent bool
		wantDueConflict        bool
	}{
		{name: "asana added a color", base: "", local: "dark-green", asana: "dark-blue", wantConflict: true},
		{name: "asana added a color, cached none", base: "none", local: "dark-green", asana: "dark-blue", wantConflict: true},
		{name: "asana removed the color", base: "dark-blue", local: "dark-green", asana: "none", wantConflict: true},
		{name: "asana removed the color (empty)", base: "dark-blue", local: "dark-green", asana: "", wantConflict: true},
		{name: "user removes color, untouched there", base: "dark-blue", local: "none", asana: "dark-blue", wantSent: true},
		{name: "user removes color, asana already none", base: "dark-blue", local: "none", asana: "none", wantSent: true},
		{name: "user removes color, asana changed it", base: "dark-blue", local: "none", asana: "light-red", wantConflict: true},
		{name: "user sets color, asana same", base: "dark-blue", local: "none", asana: "none", wantSent: true},
		{name: "color fine but due_on conflicts", base: "", local: "dark-green", asana: "none", other: true, wantDueConflict: true},
		{name: "no color to no color", base: "", local: "none", asana: "none"}, // nothing queued (#52)
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			ps, s, st := setupProject(t)
			ps.color = c.base
			p, _ := s.API.Project(ctx, "p1")
			if err := s.saveProject(ctx, p); err != nil {
				t.Fatal(err)
			}
			changes := map[string]any{"color": c.local}
			if c.other {
				changes["due_on"] = "2030-01-01"
				ps.dueOn = "2031-01-01"
			}
			if err := st.UpdateProjectLocal(ctx, "p1", changes, now); err != nil {
				t.Fatal(err)
			}
			ps.color = c.asana
			res, err := s.Push(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ops, _ := st.ListOps(ctx)
			conflict := res.Conflicts == 1
			if c.wantDueConflict {
				if !conflict || ops[0].Conflict["due_on"] != "2031-01-01" {
					t.Fatalf("res=%+v ops=%+v", res, ops)
				}
				if _, ok := ops[0].Conflict["color"]; ok {
					t.Errorf("color is not a conflict here: %v", ops[0].Conflict)
				}
				return
			}
			if conflict != c.wantConflict || (res.Sent == 1) != c.wantSent {
				t.Fatalf("res=%+v ops=%+v puts=%v", res, ops, ps.puts)
			}
			if c.wantConflict && len(ps.puts) != 0 {
				t.Errorf("conflicting edit was sent: %v", ps.puts)
			}
		})
	}
}
