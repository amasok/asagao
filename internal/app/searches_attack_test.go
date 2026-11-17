package app

import (
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

func TestValidateSearchAttack(t *testing.T) {
	long := strings.Repeat("あ", MaxSearchNameLen)
	for name, tc := range map[string]struct {
		x  readmodel.SavedSearch
		ok bool
	}{
		"max length name":       {readmodel.SavedSearch{Name: long}, true},
		"name too long":         {readmodel.SavedSearch{Name: long + "あ"}, false},
		"empty":                 {readmodel.SavedSearch{}, false},
		"space only":            {readmodel.SavedSearch{Name: "  "}, false},
		"esc in name":           {readmodel.SavedSearch{Name: "a\x1b[2Jb"}, false},
		"osc in name":           {readmodel.SavedSearch{Name: "a\x1b]0;x\x07"}, false},
		"c1 in name":            {readmodel.SavedSearch{Name: "a\u009bb"}, false},
		"nul":                   {readmodel.SavedSearch{Name: "a\x00b"}, false},
		"invalid utf8":          {readmodel.SavedSearch{Name: "a\xffb"}, false},
		"newline in query":      {readmodel.SavedSearch{Name: "a", Query: "x\ny"}, false},
		"esc in tag":            {readmodel.SavedSearch{Name: "a", Tag: "\x1b[31m"}, false},
		"bad due":               {readmodel.SavedSearch{Name: "a", DueBefore: "next week"}, false},
		"bad due after":         {readmodel.SavedSearch{Name: "a", DueAfter: "2026-13-40"}, false},
		"relative due":          {readmodel.SavedSearch{Name: "a", DueBefore: "+7d", DueAfter: "-3d"}, true},
		"huge relative":         {readmodel.SavedSearch{Name: "a", DueBefore: "+99999999999999999999d"}, false},
		"bad completion":        {readmodel.SavedSearch{Name: "a", Completion: "OPEN"}, false},
		"section no project":    {readmodel.SavedSearch{Name: "a", Section: "x"}, false},
		"negative limit":        {readmodel.SavedSearch{Name: "a", Limit: -1}, false},
		"negative modified":     {readmodel.SavedSearch{Name: "a", ModifiedWithinDays: -1}, false},
		"modified over max":     {readmodel.SavedSearch{Name: "a", ModifiedWithinDays: readmodel.MaxModifiedWithinDays + 1}, false},
		"assignee none":         {readmodel.SavedSearch{Name: "a", Assignee: "none"}, false},
		"name with inner space": {readmodel.SavedSearch{Name: "a b"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateSearch(tc.x)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v, want ok=%v", err, tc.ok)
			}
			if err != nil && strings.ContainsAny(err.Error(), "\x1b\x00‮\u009b\x07") {
				t.Fatalf("error text carries control characters: %q", err.Error())
			}
		})
	}
}

// Names that differ only by case or Unicode form are different names (exact match), and
// Save trims the name consistently with Get.
func TestSearchesAttackNameMatching(t *testing.T) {
	s := Searches{File: &fakeSearchFile{}}
	if err := s.Save(readmodel.SavedSearch{Name: " 週 "}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("週"); err != nil {
		t.Fatalf("saved name should be the trimmed one: %v", err)
	}
	if err := s.Save(readmodel.SavedSearch{Name: "週"}, false); err == nil {
		t.Fatal("same name after trim must be a duplicate")
	}
}
