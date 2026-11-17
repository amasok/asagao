package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/amasok/asagao/internal/dates"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// SearchFile is where the saved searches are kept (searches.toml, next to config.toml).
// config.SearchFile satisfies it. The file is not the cache: it is not rebuilt by a sync.
type SearchFile interface {
	// LoadSearches reads every saved search, in the file's order (none when there is no
	// file). A file that cannot be read as saved searches is an error.
	LoadSearches() ([]readmodel.SavedSearch, error)
	// SaveSearches replaces the file with ss, at once (a crash leaves the old file or the
	// new one, never a part of it).
	SaveSearches(ss []readmodel.SavedSearch) error
}

// ErrSearchNotFound is returned for a saved search that is not in the file.
var ErrSearchNotFound = errors.New("no such saved search")

// ErrSearchExists is returned when saving under a name that is taken, without overwrite.
var ErrSearchExists = errors.New("a saved search with this name exists")

// MaxSearchNameLen is the longest name of a saved search, in characters.
const MaxSearchNameLen = 100

// SearchError is a saved search that cannot be used: an invalid name or condition. Name
// is the search's name as written (it may hold control characters; Error removes them).
type SearchError struct {
	Name  string
	Field string // the condition's key in searches.toml ("" for the search itself)
	Err   error
}

func (e *SearchError) Error() string {
	name := termsafe.String(e.Name)
	if e.Field == "" {
		return fmt.Sprintf("saved search %q: %v", name, e.Err)
	}
	return fmt.Sprintf("saved search %q: %s: %v", name, e.Field, e.Err)
}

func (e *SearchError) Unwrap() error { return e.Err }

// Searches are the saved searches' use cases: list, find, save and remove them. Running
// one is Query.BuildFilter on its Spec, like any list.
type Searches struct {
	File SearchFile
}

// List returns the saved searches in the file's order. Any search that is not valid (a
// hand edit) makes it an error, so that nothing is dropped or run half-understood.
func (s Searches) List() ([]readmodel.SavedSearch, error) {
	if s.File == nil {
		return nil, nil
	}
	ss, err := s.File.LoadSearches()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, x := range ss {
		if err := ValidateSearch(x); err != nil {
			return nil, err
		}
		if seen[x.Name] {
			return nil, &SearchError{Name: x.Name, Err: errors.New("the name is used twice")}
		}
		seen[x.Name] = true
	}
	return ss, nil
}

// Get returns the saved search named name (exactly).
func (s Searches) Get(name string) (readmodel.SavedSearch, error) {
	ss, err := s.List()
	if err != nil {
		return readmodel.SavedSearch{}, err
	}
	if i := slices.IndexFunc(ss, func(x readmodel.SavedSearch) bool { return x.Name == name }); i >= 0 {
		return ss[i], nil
	}
	return readmodel.SavedSearch{}, &SearchError{Name: name, Err: ErrSearchNotFound}
}

// Save adds x, or replaces the search of the same name when overwrite is set (in its
// place in the file). Without overwrite a taken name is ErrSearchExists.
func (s Searches) Save(x readmodel.SavedSearch, overwrite bool) error {
	if s.File == nil {
		return errors.New("saved searches are not available")
	}
	x.Name = strings.TrimSpace(x.Name)
	if err := ValidateSearch(x); err != nil {
		return err
	}
	ss, err := s.List()
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(ss, func(y readmodel.SavedSearch) bool { return y.Name == x.Name }); i >= 0 {
		if !overwrite {
			return &SearchError{Name: x.Name, Err: ErrSearchExists}
		}
		ss[i] = x
	} else {
		ss = append(ss, x)
	}
	return s.File.SaveSearches(ss)
}

// Remove deletes the saved search named name. A name that is not saved is
// ErrSearchNotFound (the file is left as it is).
func (s Searches) Remove(name string) error {
	if s.File == nil {
		return errors.New("saved searches are not available")
	}
	ss, err := s.List()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ss, func(x readmodel.SavedSearch) bool { return x.Name == name })
	if i < 0 {
		return &SearchError{Name: name, Err: ErrSearchNotFound}
	}
	return s.File.SaveSearches(slices.Delete(ss, i, i+1))
}

// spellingDay is the day ValidateSearch reads relative dates from: only their spelling is
// checked, the day they mean is the one the search runs on.
var spellingDay = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ValidateSearch checks what can be checked without the cache: the name, the completion,
// the dates' spelling (relative dates are read again when the search runs), a section
// without a project, and the numbers. Names of projects, users and tags are resolved when
// the search runs.
func ValidateSearch(x readmodel.SavedSearch) error {
	bad := func(field string, err error) error { return &SearchError{Name: x.Name, Field: field, Err: err} }
	switch {
	case x.Name == "" || strings.TrimSpace(x.Name) != x.Name:
		return bad("name", errors.New("must not be empty or start or end with a space"))
	case !utf8.ValidString(x.Name) || !termsafe.Safe(x.Name) || strings.ContainsAny(x.Name, "\t\n\r"):
		return bad("name", errors.New("must not contain control characters"))
	case utf8.RuneCountInString(x.Name) > MaxSearchNameLen:
		return bad("name", fmt.Errorf("must be at most %d characters", MaxSearchNameLen))
	}
	for _, f := range []struct{ key, v string }{
		{"query", x.Query}, {"assignee", x.Assignee}, {"project", x.Project}, {"section", x.Section},
		{"tag", x.Tag}, {"field", x.Field}, {"due_before", x.DueBefore}, {"due_after", x.DueAfter},
	} {
		if !utf8.ValidString(f.v) || !termsafe.Safe(f.v) {
			return bad(f.key, errors.New("must not contain control characters"))
		}
	}
	switch x.Completion {
	case "", "open", "completed", "all":
	default:
		return bad("completion", fmt.Errorf("must be open, completed or all (got %q)", termsafe.String(x.Completion)))
	}
	if x.Assignee == "none" {
		return bad("assignee", errors.New(`filtering unassigned tasks is not supported; use "any"`))
	}
	if x.Section != "" && x.Project == "" {
		return bad("section", readmodel.ErrSectionWithoutProject)
	}
	for _, f := range []struct{ key, v string }{{"due_before", x.DueBefore}, {"due_after", x.DueAfter}} {
		if _, err := dates.Parse(f.v, spellingDay); err != nil {
			return bad(f.key, err)
		}
	}
	if x.ModifiedWithinDays < 0 || x.ModifiedWithinDays > readmodel.MaxModifiedWithinDays {
		return bad("modified_within_days", fmt.Errorf("must be between 0 and %d", readmodel.MaxModifiedWithinDays))
	}
	if x.Limit < 0 {
		return bad("limit", errors.New("must not be negative"))
	}
	return nil
}
