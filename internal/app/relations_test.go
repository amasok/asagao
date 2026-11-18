package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// relationStore records the calls the relation use cases make, in order. A name that is
// not in the maps does not resolve; a write to failWrite fails.
type relationStore struct {
	calls     []string
	users     map[string]string
	projects  map[string]string
	tasks     map[string]string
	fields    map[string]readmodel.CustomFieldView
	badValue  string // ParseCustomFieldValue refuses this input
	failWrite string // the write (its name) that fails
}

var errWrite = errors.New("write failed")

func newRelationStore() *relationStore {
	return &relationStore{
		users:    map[string]string{"me": "u-me", "佐藤": "u-sato"},
		projects: map[string]string{"新居": "p-home", "仕事": "p-work"},
		tasks:    map[string]string{"t-2": "t-2", "t-3": "t-3"},
		fields: map[string]readmodel.CustomFieldView{
			"優先度": {GID: "f-pri", Name: "優先度"}, "見積もり": {GID: "f-est", Name: "見積もり"},
		},
	}
}

func (f *relationStore) log(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *relationStore) write(name string, format string, a ...any) error {
	f.log(name+" "+format, a...)
	if f.failWrite == name {
		return errWrite
	}
	return nil
}

func (f *relationStore) ResolveTagEdit(_ context.Context, add, remove []string) (task.TagEdit, error) {
	f.log("ResolveTagEdit %v %v", add, remove)
	var e task.TagEdit
	for _, n := range add {
		if n == "ない" {
			return task.TagEdit{}, fmt.Errorf("tag %q: %w", n, errNotFound)
		}
		e.Add = append(e.Add, task.TagRef{Name: n})
	}
	for _, n := range remove {
		e.Remove = append(e.Remove, "tag-"+n)
	}
	return e, nil
}

func (f *relationStore) ApplyTagEdit(_ context.Context, taskGID string, e task.TagEdit, now time.Time) error {
	return f.write("ApplyTagEdit", "%s add=%d remove=%d at=%s", taskGID, len(e.Add), len(e.Remove), now.Format(time.TimeOnly))
}

func (f *relationStore) AddTagLocal(_ context.Context, taskGID, tagGID, tagName string, now time.Time) (string, error) {
	return "tag", f.write("AddTagLocal", "%s gid=%q name=%q at=%s", taskGID, tagGID, tagName, now.Format(time.TimeOnly))
}

func (f *relationStore) RemoveTagLocal(_ context.Context, taskGID, tagGID string, now time.Time) error {
	return f.write("RemoveTagLocal", "%s %s at=%s", taskGID, tagGID, now.Format(time.TimeOnly))
}

func resolveAll(m map[string]string, args []string, what string) ([]string, error) {
	var out []string
	for _, a := range args {
		g, ok := m[a]
		if !ok {
			return nil, fmt.Errorf("%s %q: %w", what, a, errNotFound)
		}
		out = append(out, g)
	}
	return out, nil
}

func (f *relationStore) ResolveUsers(_ context.Context, args []string) ([]string, error) {
	f.log("ResolveUsers %v", args)
	return resolveAll(f.users, args, "user")
}

func (f *relationStore) SetFollowerLocal(_ context.Context, taskGID, userGID string, follow bool, now time.Time) error {
	return f.write("SetFollowerLocal", "%s %s %v at=%s", taskGID, userGID, follow, now.Format(time.TimeOnly))
}

func (f *relationStore) ResolveProjects(_ context.Context, args []string) ([]string, error) {
	f.log("ResolveProjects %v", args)
	return resolveAll(f.projects, args, "project")
}

func (f *relationStore) SetProjectLocal(_ context.Context, taskGID, projectGID, sectionGID string, add bool, now time.Time) error {
	return f.write("SetProjectLocal", "%s %s section=%q %v at=%s", taskGID, projectGID, sectionGID, add, now.Format(time.TimeOnly))
}

func (f *relationStore) ResolveTaskGID(_ context.Context, gid string) (string, error) {
	f.log("ResolveTaskGID %s", gid)
	g, ok := f.tasks[gid]
	if !ok {
		return "", fmt.Errorf("task %q: %w", gid, errNotFound)
	}
	return g, nil
}

func (f *relationStore) SetDependencyLocal(_ context.Context, taskGID, dependencyGID string, add bool, now time.Time) error {
	return f.write("SetDependencyLocal", "%s waits for %s %v at=%s", taskGID, dependencyGID, add, now.Format(time.TimeOnly))
}

func (f *relationStore) ResolveCustomField(_ context.Context, taskGID, arg string) (readmodel.CustomFieldView, error) {
	f.log("ResolveCustomField %s %s", taskGID, arg)
	v, ok := f.fields[arg]
	if !ok {
		return readmodel.CustomFieldView{}, fmt.Errorf("field %q: %w", arg, errNotFound)
	}
	return v, nil
}

func (f *relationStore) ParseCustomFieldValue(_ context.Context, fieldGID string, input any, now time.Time) (any, error) {
	f.log("ParseCustomFieldValue %s %v at=%s", fieldGID, input, now.Format(time.TimeOnly))
	if input == f.badValue {
		return nil, fmt.Errorf("bad value %v", input)
	}
	return fmt.Sprintf("parsed(%v)", input), nil
}

func (f *relationStore) AddEnumOptionLocal(_ context.Context, fieldGID, name string, _ time.Time) (string, bool, error) {
	return "opt-" + name, true, f.write("AddEnumOptionLocal", "%s %s", fieldGID, name)
}

func (f *relationStore) SyncState(context.Context, string) (string, error) { return "", nil }

func (f *relationStore) TaskWorkspace(context.Context, string) (string, error) { return "w1", nil }

func (f *relationStore) DefaultWorkspace(context.Context) (string, error) { return "w1", nil }

func (f *relationStore) SetCustomFieldLocal(_ context.Context, taskGID, fieldGID string, value any, now time.Time) (string, error) {
	return "shown", f.write("SetCustomFieldLocal", "%s %s %v at=%s", taskGID, fieldGID, value, now.Format(time.TimeOnly))
}

// ticking returns a clock that moves one second at each read, from 10:00:00.
func ticking() Clock {
	t := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Second); return t }
}

func (f *relationStore) writes() []string {
	var out []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "Resolve") && !strings.HasPrefix(c, "Parse") {
			out = append(out, c)
		}
	}
	return out
}

func TestRelationsResolve(t *testing.T) {
	st := newRelationStore()
	r := &Relations{Store: st, Clock: ticking()}
	e, err := r.Resolve(context.Background(), "t-1", RelationsInput{
		AddTags: []string{"急ぎ"}, RemoveTags: []string{"買い物"},
		AddFollowers: []string{"佐藤"}, RemoveFollowers: []string{"me"},
		AddProjects: []string{"仕事"}, RemoveProjects: []string{"新居"},
		AddDependencies: []string{"t-2"}, RemoveDependencies: []string{"t-3"},
		AddDependents: []string{"t-3"}, RemoveDependents: []string{"t-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.writes()) != 0 {
		t.Errorf("Resolve wrote: %v", st.writes())
	}
	if len(e.Tags.Add) != 1 || len(e.Tags.Remove) != 1 {
		t.Errorf("tags = %+v", e.Tags)
	}
	if want := []Link{{"u-sato", true}, {"u-me", false}}; !slices.Equal(e.Followers, want) {
		t.Errorf("followers = %v, want %v", e.Followers, want)
	}
	if want := []Link{{"p-work", true}, {"p-home", false}}; !slices.Equal(e.Projects, want) {
		t.Errorf("projects = %v, want %v", e.Projects, want)
	}
	// dependents swap: the given task waits for this one
	want := []Dependency{{"t-1", "t-2", true}, {"t-1", "t-3", false}, {"t-3", "t-1", true}, {"t-2", "t-1", false}}
	if !slices.Equal(e.Dependencies, want) {
		t.Errorf("dependencies = %v, want %v", e.Dependencies, want)
	}
	if e.Empty() {
		t.Error("Empty() on a full edit")
	}
	if !(RelationEdit{}).Empty() {
		t.Error("the zero edit is not Empty()")
	}
}

// Every name is resolved before anything is written, in a fixed order, and the first one
// that does not resolve is reported with the argument's name (a tag's error as it is).
func TestRelationsResolveFailsInOrder(t *testing.T) {
	full := func() RelationsInput {
		return RelationsInput{
			AddTags: []string{"急ぎ"}, AddFollowers: []string{"佐藤"}, RemoveFollowers: []string{"me"},
			AddProjects: []string{"仕事"}, RemoveProjects: []string{"新居"},
			AddDependencies: []string{"t-2"}, RemoveDependencies: []string{"t-2"},
			AddDependents: []string{"t-3"}, RemoveDependents: []string{"t-3"},
		}
	}
	for _, c := range []struct {
		field string
		bad   func(*RelationsInput)
	}{
		{"", func(in *RelationsInput) { in.AddTags = []string{"ない"}; in.RemoveDependents = []string{"x"} }},
		{"add_followers", func(in *RelationsInput) { in.AddFollowers = []string{"x"}; in.RemoveFollowers = []string{"x"} }},
		{"remove_followers", func(in *RelationsInput) { in.RemoveFollowers = []string{"x"}; in.AddProjects = []string{"x"} }},
		{"add_projects", func(in *RelationsInput) { in.AddProjects = []string{"x"}; in.RemoveProjects = []string{"x"} }},
		{"remove_projects", func(in *RelationsInput) { in.RemoveProjects = []string{"x"}; in.AddDependencies = []string{"x"} }},
		{"add_dependencies", func(in *RelationsInput) { in.AddDependencies = []string{"x"}; in.RemoveDependencies = []string{"x"} }},
		{"remove_dependencies", func(in *RelationsInput) { in.RemoveDependencies = []string{"x"}; in.AddDependents = []string{"x"} }},
		{"add_dependents", func(in *RelationsInput) { in.AddDependents = []string{"x"}; in.RemoveDependents = []string{"x"} }},
		{"remove_dependents", func(in *RelationsInput) { in.RemoveDependents = []string{"t-2", "x"} }},
	} {
		t.Run(c.field, func(t *testing.T) {
			st := newRelationStore()
			r := &Relations{Store: st, Clock: ticking()}
			in := full()
			c.bad(&in)
			e, err := r.Resolve(context.Background(), "t-1", in)
			if !errors.Is(err, errNotFound) {
				t.Fatalf("err = %v, want not found", err)
			}
			var fe *FieldError
			if got := errors.As(err, &fe); got != (c.field != "") || (got && fe.Field != c.field) {
				t.Errorf("err = %#v, want a FieldError for %q", err, c.field)
			}
			if c.field != "" && !strings.HasPrefix(err.Error(), c.field+": ") {
				t.Errorf("err = %q, want it to start with %q", err, c.field+": ")
			}
			if !e.Empty() {
				t.Errorf("an edit came back with the error: %+v", e)
			}
			if len(st.writes()) != 0 {
				t.Errorf("wrote after a failed resolve: %v", st.writes())
			}
		})
	}
}

func TestRelationsApplyOrderAndClock(t *testing.T) {
	st := newRelationStore()
	r := &Relations{Store: st, Clock: ticking()}
	e := RelationEdit{
		Tags:         task.TagEdit{Add: []task.TagRef{{Name: "急ぎ"}}},
		Followers:    []Link{{"u-sato", true}, {"u-me", false}},
		Projects:     []Link{{"p-work", true}, {"p-home", false}},
		Dependencies: []Dependency{{"t-1", "t-2", true}, {"t-3", "t-1", false}},
	}
	fields := []FieldValue{{Field: readmodel.CustomFieldView{GID: "f-est"}, Value: 3.5}, {Field: readmodel.CustomFieldView{GID: "f-pri"}}}
	if err := r.Apply(context.Background(), "t-1", e, fields); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ApplyTagEdit t-1 add=1 remove=0 at=10:00:01",
		"SetCustomFieldLocal t-1 f-est 3.5 at=10:00:02",
		"SetCustomFieldLocal t-1 f-pri <nil> at=10:00:03",
		"SetDependencyLocal t-1 waits for t-2 true at=10:00:04",
		"SetDependencyLocal t-3 waits for t-1 false at=10:00:05",
		`SetFollowerLocal t-1 u-sato true at=10:00:06`,
		`SetFollowerLocal t-1 u-me false at=10:00:07`,
		`SetProjectLocal t-1 p-work section="" true at=10:00:08`,
		`SetProjectLocal t-1 p-home section="" false at=10:00:09`,
	}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(st.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestRelationsApplyStopsAtTheFirstError(t *testing.T) {
	for _, fail := range []string{"ApplyTagEdit", "SetCustomFieldLocal", "SetDependencyLocal", "SetFollowerLocal", "SetProjectLocal"} {
		t.Run(fail, func(t *testing.T) {
			st := newRelationStore()
			st.failWrite = fail
			r := &Relations{Store: st, Clock: ticking()}
			e := RelationEdit{
				Followers: []Link{{"u-sato", true}, {"u-me", true}}, Projects: []Link{{"p-work", true}, {"p-home", true}},
				Dependencies: []Dependency{{"t-1", "t-2", true}, {"t-1", "t-3", true}},
			}
			fields := []FieldValue{{Field: readmodel.CustomFieldView{GID: "f-est"}, Value: 1.0}, {Field: readmodel.CustomFieldView{GID: "f-pri"}}}
			if err := r.Apply(context.Background(), "t-1", e, fields); !errors.Is(err, errWrite) {
				t.Fatalf("err = %v, want the store's", err)
			}
			last := st.calls[len(st.calls)-1]
			if !strings.HasPrefix(last, fail+" ") {
				t.Errorf("went on after %s failed: %v", fail, st.calls)
			}
			if n := strings.Count(strings.Join(st.calls, "\n"), fail+" "); n != 1 {
				t.Errorf("%s called %d times after failing", fail, n)
			}
		})
	}
}

func TestRelationsResolveFields(t *testing.T) {
	st := newRelationStore()
	r := &Relations{Store: st, Clock: ticking()}
	got, err := r.ResolveFields(context.Background(), "t-1", map[string]any{"見積もり": "3", "優先度": nil})
	if err != nil {
		t.Fatal(err)
	}
	// sorted by name: 優先度 (U+512A) before 見積もり (U+898B)
	want := []FieldValue{{Field: st.fields["優先度"], Value: "parsed(<nil>)"}, {Field: st.fields["見積もり"], Value: "parsed(3)"}}
	if len(got) != 2 || got[0].Field.GID != want[0].Field.GID || got[0].Value != want[0].Value || got[1].Field.GID != want[1].Field.GID || got[1].Value != want[1].Value {
		t.Errorf("got %+v, want %+v", got, want)
	}
	wantCalls := []string{
		"ResolveCustomField t-1 優先度", "ParseCustomFieldValue f-pri <nil> at=10:00:01",
		"ResolveCustomField t-1 見積もり", "ParseCustomFieldValue f-est 3 at=10:00:02",
	}
	if !slices.Equal(st.calls, wantCalls) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(st.calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	if got, err := r.ResolveFields(context.Background(), "t-1", nil); err != nil || got != nil {
		t.Errorf("no fields: %v, %v", got, err)
	}

	for name, in := range map[string]map[string]any{
		"unknown field": {"優先度": "高", "ない": "x"},
		"bad value":     {"優先度": "高", "見積もり": "bad"},
	} {
		t.Run(name, func(t *testing.T) {
			st := newRelationStore()
			st.badValue = "bad"
			r := &Relations{Store: st, Clock: ticking()}
			got, err := r.ResolveFields(context.Background(), "t-1", in)
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != "custom_fields" || !strings.HasPrefix(err.Error(), "custom_fields: ") {
				t.Fatalf("err = %v, want a custom_fields FieldError", err)
			}
			if got != nil {
				t.Errorf("values came back with the error: %v", got)
			}
		})
	}
}

func TestRelationsSingleWrites(t *testing.T) {
	st := newRelationStore()
	r := &Relations{Store: st, Clock: ticking()}
	ctx := context.Background()
	for _, err := range []error{
		r.SetTag(ctx, "t-1", "tag-a", true),
		r.SetTag(ctx, "t-1", "tag-b", false),
		r.AddNewTag(ctx, "t-1", "新しい"),
		r.SetFollower(ctx, "t-1", "u-me", false),
		r.SetProject(ctx, "t-1", "p-work", true),
		r.SetDependency(ctx, "t-3", "t-1", true),
		func() error { _, err := r.ParseFieldValue(ctx, "f-pri", "高"); return err }(),
		func() error {
			d, err := r.SetFieldHere(ctx, "t-1", "f-pri", "opt")
			if d != "shown" {
				t.Errorf("display = %q", d)
			}
			return err
		}(),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		`AddTagLocal t-1 gid="tag-a" name="" at=10:00:01`,
		`RemoveTagLocal t-1 tag-b at=10:00:02`,
		`AddTagLocal t-1 gid="" name="新しい" at=10:00:03`,
		`SetFollowerLocal t-1 u-me false at=10:00:04`,
		`SetProjectLocal t-1 p-work section="" true at=10:00:05`,
		`SetDependencyLocal t-3 waits for t-1 true at=10:00:06`,
		`ParseCustomFieldValue f-pri 高 at=10:00:07`,
		`SetCustomFieldLocal t-1 f-pri opt at=10:00:08`,
	}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(st.calls, "\n"), strings.Join(want, "\n"))
	}
	st.failWrite = "AddTagLocal"
	if err := r.AddNewTag(ctx, "t-1", "x"); !errors.Is(err, errWrite) {
		t.Errorf("AddNewTag err = %v", err)
	}
	if err := r.SetTag(ctx, "t-1", "x", true); !errors.Is(err, errWrite) {
		t.Errorf("SetTag err = %v", err)
	}
}
