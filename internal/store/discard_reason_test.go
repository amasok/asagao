package store

import (
	"context"
	"path/filepath"
	"testing"
)

// legacyResetReason is the Japanese text recorded as the reason of a reset discard before
// Issue #82, when the reason was the display text (ui/format's ResetReason).
const legacyResetReason = "ローカルのデータを初期化したため、送らずに破棄しました"

// A store written before Issue #82 keeps telling its reset discards apart: the migration
// turns the text recorded then into the code, and leaves every other reason as it was.
func TestMigrateResetReasonToCode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := Open(ctx, path)
	must(t, err)
	for _, reason := range []string{legacyResetReason, "Asana で削除されています (404)", legacyResetReason + "。"} {
		_, err := st.db.Exec(`INSERT INTO discarded_changes(kind, task_gid, task_name, summary, reason, discarded_at, payload)
			VALUES('add_comment', 't1', 't', 'コメント「x」', ?, '2026-11-01T00:00:00Z', '{}')`, reason)
		must(t, err)
	}
	rollbackTo(t, st.db, len(migrations)-1)
	st.Close()

	st, err = Open(ctx, path)
	must(t, err)
	defer st.Close()
	ds, err := st.PendingDiscards(ctx)
	must(t, err)
	if len(ds) != 3 {
		t.Fatalf("discards = %+v", ds)
	}
	for i, want := range []struct {
		reason string
		reset  bool
	}{{"reset", true}, {"Asana で削除されています (404)", false}, {legacyResetReason + "。", false}} {
		if ds[i].Reason != want.reason || ds[i].IsReset() != want.reset {
			t.Errorf("discard %d: reason %q, IsReset %v; want %q, %v", i, ds[i].Reason, ds[i].IsReset(), want.reason, want.reset)
		}
		if ds[i].Summary != "コメント「x」" {
			t.Errorf("discard %d: summary %q changed", i, ds[i].Summary)
		}
	}
}

// A reset records its discards with the code, and the summary the wiring's describer gives;
// a store without a describer records no summary (the front ends describe the change from
// its kind and payload).
func TestResetDiscardReasonAndSummary(t *testing.T) {
	for _, describe := range []bool{true, false} {
		ctx := context.Background()
		s := newTestStore(t)
		if describe {
			s.SetDescribeChange(func(op Op) string { return "summary of " + op.Kind })
		}
		_, err := s.db.Exec(`INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
			VALUES('add_comment', 't1', json_object('text', 'メモ'), '{}', '', 'pending', '{}', '')`)
		must(t, err)
		_, err = s.Reset(ctx, true, now)
		must(t, err)
		ds, err := s.PendingDiscards(ctx)
		must(t, err)
		if len(ds) != 1 || !ds[0].IsReset() || ds[0].Reason != "reset" {
			t.Fatalf("describe %v: discards = %+v", describe, ds)
		}
		want := map[bool]string{true: "summary of add_comment", false: ""}[describe]
		if ds[0].Summary != want {
			t.Errorf("describe %v: summary %q, want %q", describe, ds[0].Summary, want)
		}
	}
}
