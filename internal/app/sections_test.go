package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// sectionStore records the calls of the section use cases. Project 新居 (p1) has the
// sections 家具 (s1) and 完了 (s2); task t1 is in it.
type sectionStore struct{ relationStore }

func (f *sectionStore) ResolveProject(_ context.Context, arg string) (string, error) {
	f.log("ResolveProject %s", arg)
	if arg == "新居" || arg == "p1" {
		return "p1", nil
	}
	return "", fmt.Errorf("project %q: %w", arg, errNotFound)
}

func (f *sectionStore) ResolveSection(_ context.Context, projectGID, arg string) (string, error) {
	f.log("ResolveSection %s %s", projectGID, arg)
	if g, ok := map[string]string{"家具": "s1", "完了": "s2"}[arg]; ok && projectGID == "p1" {
		return g, nil
	}
	return "", fmt.Errorf("section %q: %w", arg, errNotFound)
}

func (f *sectionStore) ResolveMove(_ context.Context, taskGID, project, section string) (string, string, error) {
	f.log("ResolveMove %s %q %s", taskGID, project, section)
	if taskGID != "t1" || (project != "" && project != "新居") || section != "完了" {
		return "", "", fmt.Errorf("move %s: %w", taskGID, errNotFound)
	}
	return "p1", "s2", nil
}

func (f *sectionStore) MoveTaskLocal(_ context.Context, gid, projectGID, sectionGID string, now time.Time) error {
	return f.write("MoveTaskLocal", "%s %s %s at=%s", gid, projectGID, sectionGID, now.Format(time.TimeOnly))
}

func (f *sectionStore) RenameSectionLocal(_ context.Context, sectionGID, name string, now time.Time) error {
	return f.write("RenameSectionLocal", "%s %q at=%s", sectionGID, name, now.Format(time.TimeOnly))
}

func TestSectionsMoveAndRename(t *testing.T) {
	st := &sectionStore{}
	s := &Sections{Store: st, Clock: ticking()}
	ctx := context.Background()
	if err := s.Move(ctx, "t1", "新居", "完了"); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveHere(ctx, "t1", "p1", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(ctx, "新居", "家具", "大型家具"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameHere(ctx, "s2", "済み"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`ResolveMove t1 "新居" 完了`, "MoveTaskLocal t1 p1 s2 at=10:00:01",
		"MoveTaskLocal t1 p1 s1 at=10:00:02",
		"ResolveProject 新居", "ResolveSection p1 家具", `RenameSectionLocal s1 "大型家具" at=10:00:03`,
		`RenameSectionLocal s2 "済み" at=10:00:04`,
	}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(st.calls, "\n"), strings.Join(want, "\n"))
	}
	if p, sec, err := s.ResolveMove(ctx, "t1", "", "完了"); p != "p1" || sec != "s2" || err != nil {
		t.Errorf("ResolveMove = %s, %s, %v", p, sec, err)
	}
}

// What does not resolve is the store's error, as it is, and nothing is written.
func TestSectionsFailWritesNothing(t *testing.T) {
	ctx := context.Background()
	for name, f := range map[string]func(*Sections) error{
		"move: no section":   func(s *Sections) error { return s.Move(ctx, "t1", "", "ない") },
		"move: no project":   func(s *Sections) error { return s.Move(ctx, "t1", "ない", "完了") },
		"rename: no project": func(s *Sections) error { return s.Rename(ctx, "ない", "家具", "x") },
		"rename: no section": func(s *Sections) error { return s.Rename(ctx, "新居", "ない", "x") },
	} {
		t.Run(name, func(t *testing.T) {
			st := &sectionStore{}
			if err := f(&Sections{Store: st, Clock: ticking()}); !errors.Is(err, errNotFound) {
				t.Fatalf("err = %v", err)
			}
			var fe *FieldError
			if err := f(&Sections{Store: st, Clock: ticking()}); errors.As(err, &fe) {
				t.Errorf("err = %#v, want the store's as it is", err)
			}
			if w := st.writes(); len(w) != 0 {
				t.Errorf("wrote: %v", w)
			}
		})
	}
	st := &sectionStore{}
	st.failWrite = "MoveTaskLocal"
	if err := (&Sections{Store: st, Clock: ticking()}).Move(ctx, "t1", "", "完了"); !errors.Is(err, errWrite) {
		t.Errorf("a failed write: err = %v", err)
	}
}
