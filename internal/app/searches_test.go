package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

type fakeSearchFile struct {
	ss      []readmodel.SavedSearch
	loadErr error
	saves   int
}

func (f *fakeSearchFile) LoadSearches() ([]readmodel.SavedSearch, error) {
	return append([]readmodel.SavedSearch(nil), f.ss...), f.loadErr
}

func (f *fakeSearchFile) UpdateSearches(change func([]readmodel.SavedSearch) ([]readmodel.SavedSearch, error)) error {
	ss, err := f.LoadSearches()
	if err != nil {
		return err
	}
	if ss, err = change(ss); err != nil {
		return err
	}
	f.saves++
	f.ss = append([]readmodel.SavedSearch(nil), ss...)
	return nil
}

func names(ss []readmodel.SavedSearch) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Name)
	}
	return out
}

func TestSearchesSaveListRemove(t *testing.T) {
	f := &fakeSearchFile{}
	s := Searches{File: f}
	if err := s.Save(readmodel.SavedSearch{Name: " 今週 ", DueBefore: "+7d"}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(readmodel.SavedSearch{Name: "請求", Query: "請求書"}, false); err != nil {
		t.Fatal(err)
	}
	// a taken name is an error without overwrite, and the file is not written
	err := s.Save(readmodel.SavedSearch{Name: "今週", DueBefore: "+14d"}, false)
	if !errors.Is(err, ErrSearchExists) || f.saves != 2 {
		t.Fatalf("taken name: %v (saves %d)", err, f.saves)
	}
	// overwrite replaces it in its place
	if err := s.Save(readmodel.SavedSearch{Name: "今週", DueBefore: "+14d"}, true); err != nil {
		t.Fatal(err)
	}
	ss, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := names(ss); !reflect.DeepEqual(got, []string{"今週", "請求"}) || ss[0].DueBefore != "+14d" {
		t.Fatalf("list %+v", ss)
	}
	got, err := s.Get("請求")
	if err != nil || got.Query != "請求書" {
		t.Fatalf("get %+v %v", got, err)
	}
	if _, err := s.Get("今"); !errors.Is(err, ErrSearchNotFound) {
		t.Fatalf("a part of a name is not a name: %v", err)
	}
	if err := s.Remove("今週"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("今週"); !errors.Is(err, ErrSearchNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	if got := names(f.ss); !reflect.DeepEqual(got, []string{"請求"}) {
		t.Fatalf("after remove %v", got)
	}
}

func TestSearchesValidate(t *testing.T) {
	for name, x := range map[string]readmodel.SavedSearch{
		"empty name":          {Name: ""},
		"blank name":          {Name: "  "},
		"escape in name":      {Name: "a\x1b[2Jb"},
		"newline in name":     {Name: "a\nb"},
		"long name":           {Name: strings.Repeat("あ", MaxSearchNameLen+1)},
		"bad completion":      {Name: "x", Completion: "done"},
		"bad due":             {Name: "x", DueBefore: "+7x"},
		"bad due after":       {Name: "x", DueAfter: "someday"},
		"section only":        {Name: "x", Section: "Done"},
		"unassigned":          {Name: "x", Assignee: "none"},
		"negative days":       {Name: "x", ModifiedWithinDays: -1},
		"too many days":       {Name: "x", ModifiedWithinDays: readmodel.MaxModifiedWithinDays + 1},
		"negative limit":      {Name: "x", Limit: -1},
		"escape in condition": {Name: "x", Project: "a\x1b]0;t\x07"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeSearchFile{}
			err := Searches{File: f}.Save(x, false)
			var se *SearchError
			if !errors.As(err, &se) || f.saves != 0 {
				t.Fatalf("saved: %v (saves %d)", err, f.saves)
			}
			if strings.ContainsAny(err.Error(), "\x1b\x07") {
				t.Errorf("control characters in the error: %q", err.Error())
			}
		})
	}
	ok := readmodel.SavedSearch{Name: "x", DueBefore: "today", DueAfter: "-3d", Completion: "all", Section: "s", Project: "p"}
	if err := ValidateSearch(ok); err != nil {
		t.Fatal(err)
	}
}

// A hand-edited file with an invalid or doubled search is an error for every use: nothing
// is dropped, and Save and Remove do not rewrite the file without it.
func TestSearchesBadFileIsNotRewritten(t *testing.T) {
	for name, ss := range map[string][]readmodel.SavedSearch{
		"duplicate": {{Name: "a"}, {Name: "a", Tag: "t"}},
		"invalid":   {{Name: "a"}, {Name: "b", Completion: "nope"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeSearchFile{ss: ss}
			s := Searches{File: f}
			if _, err := s.List(); err == nil {
				t.Fatal("listed")
			}
			if err := s.Save(readmodel.SavedSearch{Name: "c"}, true); err == nil {
				t.Fatal("saved")
			}
			if err := s.Remove("a"); err == nil {
				t.Fatal("removed")
			}
			if f.saves != 0 {
				t.Fatalf("file rewritten %d times", f.saves)
			}
		})
	}
	f := &fakeSearchFile{loadErr: errors.New("parse searches.toml: boom")}
	if err := (Searches{File: f}).Save(readmodel.SavedSearch{Name: "c"}, true); err == nil || f.saves != 0 {
		t.Fatalf("saved over a broken file: %v", err)
	}
}

func TestSavedSearchSpecDefaults(t *testing.T) {
	list := readmodel.SavedSearch{Name: "l", DueBefore: "+7d"}.Spec()
	if list.DefaultAssignee != "me" || list.DefaultCompleted == nil || *list.DefaultCompleted {
		t.Errorf("without a query it runs like task list: %+v", list)
	}
	search := readmodel.SavedSearch{Name: "s", Query: "請求書", Completion: "open"}.Spec()
	if search.DefaultAssignee != "any" || search.DefaultCompleted != nil || search.Query != "請求書" || search.Completion != "open" {
		t.Errorf("with a query it runs like search: %+v", search)
	}
}
