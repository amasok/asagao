package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var attackNow = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

func insertOp(t *testing.T, st *Store, kind, task, payload, base, state string) {
	t.Helper()
	if _, err := st.db.ExecContext(context.Background(),
		`INSERT INTO outbox(kind, task_gid, payload, base, state, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		kind, task, payload, base, state, attackNow.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

// A stamp that is broken, empty, in another format or in the future never lets a prune delete:
// it is replaced by "now" and the grace starts over.
func TestPruneResultsBrokenStampsNeverDelete(t *testing.T) {
	for _, stamp := range []string{"", "abc", "2026-11-16", "0", "-1", "9999-12-31T00:00:00Z", "2020-01-01T00:00:00", "2020-01-01 00:00:00", "\x00", "2026-13-45T25:61:61Z",
		attackNow.Add(11 * ResultGrace).Format(time.RFC3339), strings.Repeat("9", 5000)} {
		st := resultsStore(t)
		key := resultPrefixTask + "local:x"
		mustSet(t, st, key, "1200", keyResultSeen+key, stamp)
		if n := prune(t, st, attackNow.Add(10*ResultGrace)); n != 0 {
			t.Errorf("stamp %q: deleted %d on a stamp that cannot be trusted", stamp, n)
		}
		if syncStateOf(t, st, key) != "1200" {
			t.Errorf("stamp %q: mapping lost", stamp)
		}
	}
}

// The clock going back between two syncs must not shorten the grace: a prune "in the
// past" keeps everything, and when the clock is right again the grace counts from there.
func TestPruneResultsClockGoesBack(t *testing.T) {
	st := resultsStore(t)
	key := resultPrefixStory + "local:s"
	mustSet(t, st, key, "1500")
	prune(t, st, attackNow)
	if n := prune(t, st, attackNow.Add(-48*time.Hour)); n != 0 { // clock set back two days
		t.Fatalf("deleted %d with the clock behind", n)
	}
	// stamp was reset to the earlier time; at attackNow+grace-1d it is only 29+2 days later
	// the stamp was reset to T0-2d, so the grace ends at T0+28d; one day before that it is kept
	if n := prune(t, st, attackNow.Add(ResultGrace-3*24*time.Hour)); n != 0 || syncStateOf(t, st, key) == "" {
		t.Fatalf("deleted before the grace counted from the reset stamp was over")
	}
}

// An entry in any state (pending, conflict, failed) keeps a mapping that it names as its task,
// in its payload or in its base — and any kind of entry does, not only the verified ones.
func TestPruneResultsKeepsForOpsInAnyState(t *testing.T) {
	for _, state := range []string{"pending", "conflict", "failed"} {
		for _, where := range []string{"task", "payload", "base"} {
			st := resultsStore(t)
			key := resultPrefixTask + "local:abc123"
			mustSet(t, st, key, "1200")
			prune(t, st, attackNow)
			task, payload, base := "t1", "{}", "{}"
			switch where {
			case "task":
				task = "local:abc123"
			case "payload":
				payload = `{"parent":"local:abc123"}`
			case "base":
				base = `{"x":["local:abc123"]}`
			}
			insertOp(t, st, OpUpdateTask, task, payload, base, state)
			if n := prune(t, st, attackNow.Add(3*ResultGrace)); n != 0 || syncStateOf(t, st, key) == "" {
				t.Errorf("%s/%s: mapping deleted while an op names it", state, where)
			}
		}
	}
}

// A queued entry of a verified kind in a failed/conflict state still holds the mappings of
// its kind back (its verification will read them).
func TestPruneResultsHeldBackByFailedVerifiedKinds(t *testing.T) {
	for kind, prefix := range map[string]string{OpCreateTask: resultPrefixTask, OpDuplicateTask: resultPrefixTask, OpAddComment: resultPrefixStory, OpUploadAttachment: resultPrefixAttachment, OpCreateStatus: resultPrefixStatus} {
		for _, state := range []string{"pending", "conflict", "failed"} {
			st := resultsStore(t)
			key := prefix + "local:old"
			mustSet(t, st, key, "1")
			prune(t, st, attackNow)
			insertOp(t, st, kind, "local:other", "{}", "{}", state)
			if n := prune(t, st, attackNow.Add(3*ResultGrace)); n != 0 || syncStateOf(t, st, key) == "" {
				t.Errorf("%s/%s: %s mapping deleted while the op is queued", kind, state, prefix)
			}
		}
	}
}

// Keys that only look like mappings: wrong case, local: in the middle, empty rest, other
// prefixes, stamps of other keys. Nothing but real "<prefix>local:..." mappings is deleted.
func TestPruneResultsOnlyRealMappings(t *testing.T) {
	st := resultsStore(t)
	keep := []string{
		"GID:local:a", "xgid:local:a", "gid:xlocal:a", "gid: local:a", "tag:Local:a", "story:", "ui:collapsed:local:a",
		keyCustomFields + "local:a", keyEvents + "local:a", "inbox_status", "result_seen_x", "status", "att", "events:local:p",
	}
	for _, k := range keep {
		mustSet(t, st, k, "v")
	}
	prune(t, st, attackNow)
	if n := prune(t, st, attackNow.Add(5*ResultGrace)); n != 0 {
		t.Fatalf("deleted %d", n)
	}
	for _, k := range keep {
		if syncStateOf(t, st, k) != "v" {
			t.Errorf("%q touched", k)
		}
	}
}

// Gids with SQL/LIKE/regex-looking characters are matched literally.
func TestPruneResultsOddLocalGIDs(t *testing.T) {
	st := resultsStore(t)
	for _, g := range []string{"local:%", "local:_", "local:a'b", `local:a"b`, "local:a;DROP TABLE outbox;--", "local:日本語", "local:\x00x", "local:"} {
		mustSet(t, st, resultPrefixTask+g, "1")
	}
	insertOp(t, st, OpUpdateTask, "t1", `{"k":"unrelated"}`, "{}", "pending")
	prune(t, st, attackNow)
	if n := prune(t, st, attackNow.Add(ResultGrace)); n < 7 { // "local:" matches any payload with "local:" (none here) so all go
		t.Errorf("deleted %d of 8 unreferenced odd gids", n)
	}
	if ops, err := st.ListOps(context.Background()); err != nil || len(ops) != 1 {
		t.Fatalf("outbox damaged: %v %v", ops, err)
	}
}

// Prune while another connection (a push in another process) records mappings and completes
// creates: no error, no data race, and a mapping recorded during the run is never deleted.
func TestPruneResultsConcurrentWithPush(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := a.UpsertWorkspaces(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// old, unneeded mappings that are past their grace (the worst case for a wrong delete)
	for i := 0; i < 50; i++ {
		k := fmt.Sprintf("%slocal:old%d", resultPrefixTask, i)
		mustSet(t, a, k, "1", keyResultSeen+k, attackNow.Add(-2*ResultGrace).Format(time.RFC3339))
	}
	var wg sync.WaitGroup
	var created []string
	var mu sync.Mutex
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			local := fmt.Sprintf("local:new%d", i)
			// write transactions begin immediate (#50): a concurrent prune makes this wait,
			// never fail as busy
			if err := b.ReplaceLocalTaskGID(ctx, local, fmt.Sprint(5000+i)); err != nil {
				t.Errorf("push side: %v", err)
				continue
			}
			mu.Lock()
			created = append(created, local)
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			if _, err := a.PruneResults(ctx, attackNow.Add(10*ResultGrace)); err != nil {
				t.Errorf("prune: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	prune(t, a, attackNow.Add(10*ResultGrace)) // quiet now: must succeed
	for _, l := range created {
		if g, _ := a.RealGID(ctx, l); g == l {
			// a freshly recorded mapping was stamped by a prune that ran at +10 grace and then
			// "deleted" at the next one: both prunes use the same clock, so the grace is 0 here.
			// Only fail if the stamp is not older than the grace relative to its own first sight.
			if seen := syncStateOf(t, a, keyResultSeen+resultPrefixTask+l); seen != "" {
				t.Errorf("%s: stamp left without mapping", l)
			}
		}
	}
	for i := 0; i < 50; i++ {
		if syncStateOf(t, a, fmt.Sprintf("%slocal:old%d", resultPrefixTask, i)) != "" {
			t.Errorf("old%d should be pruned", i)
		}
	}
}

// A big outbox (many entries with large payloads) and many mappings: the prune at the end of a
// sync must stay fast; every mapping is looked up against every entry, so this is the quadratic
// case.
func TestPruneResultsLargeOutboxIsFast(t *testing.T) {
	ctx := context.Background()
	st := resultsStore(t)
	const maps, ops = 3000, 1500 // the scan per mapping took ~15 s here; now well under 1 s
	big := `{"notes":"` + strings.Repeat("あいうえお ", 100) + `"}`
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maps; i++ {
		if _, err := tx.ExecContext(ctx, "INSERT INTO sync_state(key, value) VALUES (?, '1')", fmt.Sprintf("%slocal:%032d", resultPrefixTask, i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < ops; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, created_at) VALUES ('update_task', 't1', ?, ?, ?)`, big, big, attackNow.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The old scan took ~15 s without -race. -race (and a loaded machine, as in CI with
	// other packages running) makes everything several times slower: the limit is looser
	// then, still well under what the scan took with -race.
	limit := 3 * time.Second
	if raceEnabled {
		limit = 30 * time.Second
	}
	start := time.Now()
	prune(t, st, attackNow)
	if d := time.Since(start); d > limit {
		t.Errorf("prune of %d mappings against %d entries took %v (limit %v)", maps, ops, d, limit)
	}
}
