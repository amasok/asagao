package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

// AddCommentOnceLocal completes and comments in one go, then does nothing for the same
// marker (queued or sent).
func TestAddCommentOnceLocal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	me := UserRef{GID: "me", Name: "Me"}
	gid, added, err := s.AddCommentOnceLocal(ctx, "t1", "asagao-git: abc", map[string]any{"completed": true}, "done\nasagao-git: abc", "", me, now)
	if err != nil || !added || gid == "" {
		t.Fatalf("first: %q %v %v", gid, added, err)
	}
	d, _ := s.GetTask(ctx, "t1")
	if !d.Completed {
		t.Error("not completed")
	}
	ops, _ := s.ListOps(ctx)
	if len(ops) != 2 || ops[0].Kind != OpUpdateTask || ops[1].Kind != OpAddComment {
		t.Fatalf("ops %+v", ops)
	}
	// reopened, then the same marker again: nothing
	if err := s.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": false}, now); err != nil {
		t.Fatal(err)
	}
	gid, added, err = s.AddCommentOnceLocal(ctx, "t1", "asagao-git: abc", map[string]any{"completed": true}, "again\nasagao-git: abc", "", me, now)
	if err != nil || added || gid != "" {
		t.Errorf("second: %q %v %v", gid, added, err)
	}
	if d, _ := s.GetTask(ctx, "t1"); d.Completed {
		t.Error("completed again")
	}
	// a refresh that read the comments before ours was sent drops it: the record holds
	if err := s.ReplaceStories(ctx, "t1", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardOp(ctx, ops[1].ID); err != nil { // the queued comment, as if sent
		t.Fatal(err)
	}
	if d, _ := s.GetTask(ctx, "t1"); len(d.Stories) != 0 {
		t.Fatalf("stories left: %+v", d.Stories)
	}
	if _, added, err := s.AddCommentOnceLocal(ctx, "t1", "asagao-git: abc", nil, "x\nasagao-git: abc", "", me, now); err != nil || added {
		t.Errorf("after a stale refresh: %v %v", added, err)
	}
	// a sent comment (from Asana) counts too
	if err := s.ReplaceStories(ctx, "t2", []asana.Story{{GID: "st1", Type: "comment", Text: "x\nasagao-git: def"}}, now); err != nil {
		t.Fatal(err)
	}
	if _, added, err := s.AddCommentOnceLocal(ctx, "t2", "asagao-git: def", nil, "y\nasagao-git: def", "", me, now); err != nil || added {
		t.Errorf("sent comment: %v %v", added, err)
	}
	// a comment that only quotes the marker does not count
	if err := s.ReplaceStories(ctx, "t2", []asana.Story{{GID: "st2", Type: "comment", Text: "quote: asagao-git: ghi\nmore"}}, now); err != nil {
		t.Fatal(err)
	}
	if _, added, err := s.AddCommentOnceLocal(ctx, "t2", "asagao-git: ghi", nil, "z\nasagao-git: ghi", "", me, now); err != nil || !added {
		t.Errorf("quoted marker: %v %v", added, err)
	}
	// the rich text decides when there is one: a marker only in code is quoted
	if err := s.ReplaceStories(ctx, "t2", []asana.Story{{GID: "st3", Type: "comment", Text: "x\nasagao-git: jkl",
		HTMLText: "<body>x\n<pre>asagao-git: jkl</pre></body>"}}, now); err != nil {
		t.Fatal(err)
	}
	if _, added, err := s.AddCommentOnceLocal(ctx, "t2", "asagao-git: jkl", nil, "z\nasagao-git: jkl", "", me, now); err != nil || !added {
		t.Errorf("marker in code: %v %v", added, err)
	}
	// another task, another marker: added
	if _, added, err := s.AddCommentOnceLocal(ctx, "t2", "asagao-git: abc", nil, "asagao-git: abc", "", me, now); err != nil || !added {
		t.Errorf("t2: %v %v", added, err)
	}
	// errors write nothing
	before, _ := s.ListOps(ctx)
	for name, call := range map[string]func() error{
		"no task": func() error {
			_, _, err := s.AddCommentOnceLocal(ctx, "nope", "m", nil, "m", "", me, now)
			return err
		},
		"empty": func() error { _, _, err := s.AddCommentOnceLocal(ctx, "t1", "m", nil, " ", "", me, now); return err },
		"marker not in text": func() error {
			_, _, err := s.AddCommentOnceLocal(ctx, "t1", "zzz", nil, "text", "", me, now)
			return err
		},
		"marker not on the last line": func() error {
			_, _, err := s.AddCommentOnceLocal(ctx, "t1", "m", nil, "m\nmore", "", me, now)
			return err
		},
		"empty marker": func() error { _, _, err := s.AddCommentOnceLocal(ctx, "t1", "", nil, "text", "", me, now); return err },
		"bad change": func() error {
			_, _, err := s.AddCommentOnceLocal(ctx, "t1", "m", map[string]any{"completed": "yes"}, "m", "", me, now)
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if after, _ := s.ListOps(ctx); len(after) != len(before) {
		t.Errorf("errors wrote: %d ops, was %d", len(after), len(before))
	}
}

// Two processes (two connections to the file) adding the same marker at once add it once.
func TestAddCommentOnceLocalConcurrent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var stores []*Store
	for range 4 {
		o, err := Open(ctx, s.Path())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { o.Close() })
		stores = append(stores, o)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	adds := 0
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, added, err := stores[i%len(stores)].AddCommentOnceLocal(ctx, "t1", "asagao-git: abc", map[string]any{"completed": true},
				"c\nasagao-git: abc", "", UserRef{GID: "me"}, time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC))
			if err != nil {
				t.Error(err)
			}
			if added {
				mu.Lock()
				adds++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if adds != 1 {
		t.Errorf("added %d times", adds)
	}
	d, _ := s.GetTask(ctx, "t1")
	n := 0
	for _, st := range d.Stories {
		if strings.Contains(st.Text, "asagao-git: abc") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d comments", n)
	}
}
