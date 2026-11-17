package store

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var localGIDRe = regexp.MustCompile(`^local:[0-9a-f]{16}$`)

// Every creation site must still produce "local:" + 16 hex chars, all distinct.
func TestLocalGIDShapeAtEverySite(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seen := map[string]string{}
	check := func(site, g string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", site, err)
		}
		if !localGIDRe.MatchString(g) {
			t.Errorf("%s: gid %q has the wrong shape", site, g)
		}
		if !IsLocalGID(g) {
			t.Errorf("%s: IsLocalGID(%q) = false", site, g)
		}
		if prev, dup := seen[g]; dup {
			t.Errorf("%s: gid %q repeated (also %s)", site, g, prev)
		}
		seen[g] = site
	}
	task, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "t"}, testNow)
	check("task", task, err)
	sub, err := s.CreateTaskLocal(ctx, NewTask{ParentGID: task, Name: "sub"}, testNow)
	check("subtask", sub, err)
	c, err := s.AddCommentLocal(ctx, task, "hi", UserRef{}, testNow)
	check("comment", c, err)
	dup, err := s.DuplicateTaskLocal(ctx, "t1", "copy", testNow)
	check("duplicate", dup, err)
	tag, err := s.AddTagLocal(ctx, task, "", "newtag", testNow)
	check("tag", tag, err)
	st, err := s.CreateStatusLocal(ctx, "p1", "on_track", "t", "body", "", UserRef{}, testNow)
	check("status", st, err)

	src := filepath.Join(t.TempDir(), "a b.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	att, err := s.UploadAttachmentLocal(ctx, task, src, cache, testNow)
	check("attachment", att, err)
	// the cached file name starts with the random id, without the prefix
	matches, _ := filepath.Glob(filepath.Join(cache, att[len("local:"):]+"-*"))
	if len(matches) != 1 {
		t.Errorf("cache file for %s not found: %v", att, matches)
	}
}

func TestLocalPrefixAliasAndBoundary(t *testing.T) {
	if LocalPrefix != "local:" {
		t.Fatalf("LocalPrefix = %q", LocalPrefix)
	}
	for g, want := range map[string]bool{
		"local:x": true, "LOCAL:x": false, "Local:x": false, "1:local:x": false,
		"local": false, "": false, "local:\x00": true, " local:x": false,
	} {
		if IsLocalGID(g) != want {
			t.Errorf("IsLocalGID(%q) != %v", g, want)
		}
	}
}
