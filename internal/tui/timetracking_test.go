package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// timeHarness is a TUI whose syncer reads time tracking entries from a fake Asana that
// answers every task's list with one entry of Partner's (or 402 when unpaid).
func timeHarness(t *testing.T, unpaid bool) (*harness, *store.Store) {
	t.Helper()
	st := fixtureStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unpaid {
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(w, `{"errors":[{"message":"Payment Required"}]}`)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/time_tracking_entries") {
			fmt.Fprint(w, `{"data":[{"gid":"te1","duration_minutes":30,"entered_on":"2026-11-10","assignee":{"gid":"u2","name":"Partner"}}]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"message":"not in the fake"}]}`)
	}))
	t.Cleanup(srv.Close)
	api := asana.NewClient(srv.Client())
	api.BaseURL, api.MaxRetries = srv.URL, 0
	m := New(context.Background(), Deps{Store: st, Now: func() time.Time { return testNow }})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 160, Height: 40})
	h.run(m.Init())
	m.syncer = &sync.Syncer{API: api, Store: st}
	return h, st
}

// space h lists the task's time entries (read from Asana) with the total; an entry can be
// recorded ("1h30m 昨日"), changed and deleted (asked first), and the list shows the result.
func TestTimeEntriesInTUI(t *testing.T) {
	h, st := timeHarness(t, false)
	ctx := context.Background()
	task := h.selected()
	h.key(" ", "h")
	if h.m.picker == nil || h.m.picker.kind != pickTimeEntries {
		t.Fatalf("no time entries:\n%s", h.screen())
	}
	s := h.screen()
	for _, want := range []string{"2026-11-10(火)", "30m", "Partner", "合計 30m（1 件）", "作業時間を記録する"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	h.m.syncer = nil // the writes stay local

	h.key("enter") // ＋ 作業時間を記録する…
	if h.m.promptMode != promptTimeAdd {
		t.Fatalf("no prompt:\n%s", h.screen())
	}
	h.typeText("1h30m 昨日")
	h.key("enter")
	es, err := st.TimeEntries(ctx, task)
	if err != nil || len(es.Entries) != 2 || es.TotalMinutes != 120 {
		t.Fatalf("entries %+v %v", es, err)
	}
	var local string
	for _, e := range es.Entries {
		if e.EnteredOn == "2026-11-12" && e.DurationMinutes == 90 {
			local = e.GID
		}
	}
	if local == "" {
		t.Fatalf("recorded %+v", es.Entries)
	}
	if h.m.picker == nil || h.m.picker.kind != pickTimeEntries || !strings.Contains(h.screen(), "合計 2h（2 件）") ||
		!strings.Contains(h.screen(), "未送信") {
		t.Fatalf("list not shown again:\n%s", h.screen())
	}

	// The new entry (newest day) is first: change it.
	h.key("j", "enter")
	if h.m.picker == nil || h.m.picker.kind != pickTimeAction {
		t.Fatalf("no actions:\n%s", h.screen())
	}
	h.key("enter") // 時間・日を直す…
	if h.m.promptMode != promptTimeEdit || h.m.prompt.Value() != "1h30m 2026-11-12" {
		t.Fatalf("edit prompt %q", h.m.prompt.Value())
	}
	h.m.prompt.SetValue("45m")
	h.key("enter")
	if es, _ := st.TimeEntries(ctx, task); es.TotalMinutes != 75 {
		t.Errorf("after the edit %+v", es)
	}

	// Delete asks first; "やめる" keeps it.
	h.key("j", "enter", "j", "enter")
	if h.m.picker == nil || h.m.picker.kind != pickTimeDelete {
		t.Fatalf("no confirmation:\n%s", h.screen())
	}
	h.key("enter") // やめる
	if es, _ := st.TimeEntries(ctx, task); len(es.Entries) != 2 {
		t.Errorf("deleted without confirming: %+v", es)
	}
	h.key("j", "enter", "j", "enter", "j", "enter")
	if es, _ := st.TimeEntries(ctx, task); len(es.Entries) != 1 || es.Entries[0].GID != "te1" {
		t.Errorf("after the delete %+v", es)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 0 { // the add was never sent: edit and delete changed it, then dropped it
		t.Errorf("ops %+v", ops)
	}

	// A bad time says why and records nothing.
	h.key("g", "enter")
	h.typeText("abc")
	h.key("enter")
	if !strings.Contains(h.screen(), "1h30m") {
		t.Errorf("no hint for a bad time:\n%s", h.screen())
	}
}

// Asana refusing time tracking (402) closes the list, says why, and hides the menu item.
func TestTimeEntriesPaymentRequiredInTUI(t *testing.T) {
	h, st := timeHarness(t, true)
	h.key(" ", "h")
	if h.m.picker != nil && h.m.picker.kind == pickTimeEntries {
		t.Fatalf("list still open:\n%s", h.screen())
	}
	if !strings.Contains(h.screen(), "タイムトラッキング") {
		t.Errorf("not said why:\n%s", h.screen())
	}
	if v, _ := st.SyncState(context.Background(), readmodel.KeyPlanUnavailable+readmodel.PlanTimeTracking); v == "" {
		t.Fatal("402 not recorded")
	}
	h.key(" ")
	if strings.Contains(h.screen(), "作業時間を見る") {
		t.Errorf("menu still offers time tracking:\n%s", h.screen())
	}
}
