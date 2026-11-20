package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/store"
)

// failTags is a store whose tag edit fails, as when the task is gone or the disk is full.
type failTags struct {
	localStore
	calls int
}

func (f *failTags) ApplyTagEdit(context.Context, string, task.TagEdit, time.Time) error {
	f.calls++
	return errors.New("tags: disk full\x1b[2J")
}

// The tags cannot be added: the task is still created, selected, and the status says
// so (without control characters).
func TestQuickAddAttackTagFailureStillCreatesTask(t *testing.T) {
	st := quickAddStore(t)
	h := projectHarness(t, st, nil)
	ft := &failTags{localStore: st}
	h.m.deps = onStore(ft, h.m.deps)
	h.key("a")
	h.typeText("棚を買う 明日 #買い物")
	h.key("enter")
	if ft.calls == 0 {
		t.Fatal("tags were not attempted")
	}
	got := h.m.selectedTask()
	if got == nil || got.Name != "棚を買う" || got.DueOn != "2026-11-14" {
		t.Fatalf("the task must exist and be selected: %+v (status %q)", got, h.m.status)
	}
	if !strings.Contains(h.m.status, "タグを付けられませんでした") || !strings.Contains(h.m.status, "棚を買う") {
		t.Errorf("status %q", h.m.status)
	}
	if strings.ContainsAny(h.m.status, "\x1b\x00") {
		t.Errorf("status has control characters: %q", h.m.status)
	}
}

// Whitespace only (even full-width) creates nothing.
func TestQuickAddAttackBlankLineCreatesNothing(t *testing.T) {
	st := quickAddStore(t)
	h := projectHarness(t, st, nil)
	h.key("a")
	h.typeText("   　 ")
	h.key("enter")
	if ops, _ := st.ListOps(context.Background()); len(ops) != 0 {
		t.Fatalf("ops queued for a blank name: %+v", ops)
	}
}

// Names from Asana with control characters never reach the status line raw.
func TestQuickAddAttackStatusIsClean(t *testing.T) {
	st := quickAddStore(t)
	ctx := context.Background()
	if err := st.UpsertUsers(ctx, []asana.User{{GID: "u9", Name: "Evil\x1b]0;pwn\x07", Email: "evil@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTags(ctx, []asana.Tag{{GID: "g9", Name: "bad\x1b[31m"}}); err != nil {
		t.Fatal(err)
	}
	h := projectHarness(t, st, nil)
	h.run(h.m.createTask("x @evil #bad\x1b[31m 明日"))
	h.run(h.m.createTask("y @evil"))
	if strings.ContainsAny(h.m.status, "\x1b\x07\x00") || !strings.Contains(h.m.status, "担当: ") {
		t.Fatalf("status %q", h.m.status)
	}
}

// A name pasted with control characters (it is the user's own text, but a paste can carry
// them) must not reach the terminal raw through the status line either.
func TestQuickAddAttackStatusNameWithControlCharacters(t *testing.T) {
	st := quickAddStore(t)
	h := projectHarness(t, st, nil)
	h.run(h.m.createTask("z #\x1b[31mnope"))
	if strings.ContainsAny(h.m.status, "\x1b\x07\x00") {
		t.Fatalf("status %q", h.m.status)
	}
}

// The time of the key press is used for relative dates and the op's created_at, even if
// the clock has moved by the time the command runs.
func TestQuickAddAttackUsesTimeOfKeyPress(t *testing.T) {
	st := quickAddStore(t)
	h := projectHarness(t, st, nil)
	cur := testNow
	h.m.deps.Now = func() time.Time { return cur }
	cmd := h.m.createTask("牛乳 +1d")
	cur = cur.AddDate(0, 0, 5) // the command runs later
	h.run(cmd)
	ops, _ := st.ListOps(context.Background())
	if len(ops) == 0 {
		t.Fatal("no op")
	}
	d, _ := st.GetTask(context.Background(), ops[0].TaskGID)
	if d == nil || d.DueOn != testNow.AddDate(0, 0, 1).Format(time.DateOnly) {
		t.Fatalf("due %+v", d)
	}
	if !strings.HasPrefix(ops[0].CreatedAt, testNow.UTC().Format("2006-01-02")) && !strings.HasPrefix(ops[0].CreatedAt, testNow.Format("2006-01-02")) {
		t.Errorf("created_at %q is not the key press time %v", ops[0].CreatedAt, testNow)
	}
}

// In the calendar, a date read from the line wins over the selected day; a line without
// one gets the selected day.
func TestQuickAddAttackCalendarDayPriority(t *testing.T) {
	st := quickAddStore(t)
	h := projectHarness(t, st, nil)
	for i := 0; i < 4 && !h.m.calendarActive(); i++ {
		h.key("v")
	}
	if !h.m.calendarActive() {
		t.Skip("cannot reach the calendar")
	}
	day := h.m.cal.day.Format(time.DateOnly)
	h.run(h.m.createTask("A 2026-12-24"))
	h.run(h.m.createTask("B"))
	ops, _ := st.ListOps(context.Background())
	due := map[string]string{}
	for _, op := range ops {
		if op.Kind == store.OpCreateTask {
			d, _ := st.GetTask(context.Background(), op.TaskGID)
			due[d.Name] = d.DueOn
		}
	}
	if due["A"] != "2026-12-24" || due["B"] != day {
		t.Fatalf("due %v, calendar day %s", due, day)
	}
}
