package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// The conversions for comments and status updates mark their target, so that a test can
// see that each write gets its own (they accept different tags) and which output went where.
func (t *fakeText) Comment(md string) (html, plain string) {
	t.calls++
	return "<comment>" + md + "</comment>", "comment-plain:" + md
}

func (t *fakeText) StatusUpdate(md string) (html, plain string) {
	t.calls++
	return "<status>" + md + "</status>", "status-plain:" + md
}

// postStore records the calls the comment and status use cases make, in order.
type postStore struct {
	calls    []string
	projects map[string]string // arg -> gid
	me       readmodel.UserRef
	meErr    error
	writeErr error
	wrote    []string // the arguments of each write, as one line
}

func (f *postStore) ResolveProject(_ context.Context, arg string) (string, error) {
	f.calls = append(f.calls, "ResolveProject "+arg)
	if gid, ok := f.projects[arg]; ok {
		return gid, nil
	}
	return "", fmt.Errorf("project %q: %w", arg, errNotFound)
}

func (f *postStore) Me(context.Context) (readmodel.UserRef, error) {
	f.calls = append(f.calls, "Me")
	return f.me, f.meErr
}

func (f *postStore) AddRichCommentLocal(_ context.Context, taskGID, text, htmlText string, author readmodel.UserRef, now time.Time) (string, error) {
	f.calls = append(f.calls, "AddRichCommentLocal")
	if f.writeErr != nil {
		return "", f.writeErr
	}
	f.wrote = append(f.wrote, fmt.Sprintf("task=%s text=%s html=%s author=%+v now=%s", taskGID, text, htmlText, author, now.Format(time.RFC3339Nano)))
	return "local:c1", nil
}

func (f *postStore) EditCommentLocal(_ context.Context, storyGID, text, htmlText string, now time.Time) error {
	f.calls = append(f.calls, "EditCommentLocal")
	if f.writeErr != nil {
		return f.writeErr
	}
	f.wrote = append(f.wrote, fmt.Sprintf("story=%s text=%s html=%s now=%s", storyGID, text, htmlText, now.Format(time.RFC3339Nano)))
	return nil
}

func (f *postStore) DeleteCommentLocal(_ context.Context, storyGID string, now time.Time) error {
	f.calls = append(f.calls, "DeleteCommentLocal")
	if f.writeErr != nil {
		return f.writeErr
	}
	f.wrote = append(f.wrote, fmt.Sprintf("story=%s now=%s", storyGID, now.Format(time.RFC3339Nano)))
	return nil
}

func (f *postStore) CreateStatusLocal(_ context.Context, projectGID, statusType, title, text, htmlText string, author readmodel.UserRef, now time.Time) (string, error) {
	f.calls = append(f.calls, "CreateStatusLocal")
	if f.writeErr != nil {
		return "", f.writeErr
	}
	f.wrote = append(f.wrote, fmt.Sprintf("project=%s status=%s title=%s text=%s html=%s author=%+v now=%s",
		projectGID, statusType, title, text, htmlText, author, now.Format(time.RFC3339Nano)))
	return "local:s1", nil
}

func newPostStore() *postStore {
	return &postStore{projects: map[string]string{"新居": "p1"}, me: readmodel.UserRef{GID: "u-me", Name: "私"}}
}

// clock returns now, later each time it is read, and counts the readings.
func clock(reads *int) Clock {
	return func() time.Time {
		*reads++
		return now.Add(time.Duration(*reads-1) * time.Second)
	}
}

func TestCommentsAdd(t *testing.T) {
	st, text, reads := newPostStore(), &fakeText{}, 0
	c := &Comments{Store: st, Text: text, Clock: clock(&reads)}
	gid, err := c.Add(context.Background(), "123", "**よろしく**")
	if err != nil || gid != "local:c1" {
		t.Fatalf("gid %q, err %v", gid, err)
	}
	if want := []string{"Me", "AddRichCommentLocal"}; !slices.Equal(st.calls, want) {
		t.Errorf("calls %q, want %q", st.calls, want)
	}
	want := "task=123 text=comment-plain:**よろしく** html=<comment>**よろしく**</comment> author={GID:u-me Name:私} now=" + now.Format(time.RFC3339Nano)
	if len(st.wrote) != 1 || st.wrote[0] != want {
		t.Errorf("wrote %q\nwant %q", st.wrote, want)
	}
	if text.calls != 1 || reads != 1 {
		t.Errorf("converted %d times, read the clock %d times; want 1 and 1", text.calls, reads)
	}
}

// Edit converts the Markdown like Add (rich text and its plain text) and passes the
// comment's gid; Delete passes it as it is. Neither asks for the user: the store checks
// that the comment is theirs. The store's error comes back as it is.
func TestCommentsEditDelete(t *testing.T) {
	st, text, reads := newPostStore(), &fakeText{}, 0
	c := &Comments{Store: st, Text: text, Clock: clock(&reads)}
	ctx := context.Background()
	if err := c.Edit(ctx, "s1", "**直した**"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "s2"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"EditCommentLocal", "DeleteCommentLocal"}; !slices.Equal(st.calls, want) {
		t.Errorf("calls %q, want %q", st.calls, want)
	}
	want := []string{
		"story=s1 text=comment-plain:**直した** html=<comment>**直した**</comment> now=" + now.Format(time.RFC3339Nano),
		"story=s2 now=" + now.Add(time.Second).Format(time.RFC3339Nano),
	}
	if !slices.Equal(st.wrote, want) {
		t.Errorf("wrote %q\nwant %q", st.wrote, want)
	}
	st.writeErr = errWrite
	if err := c.Edit(ctx, "s1", "x"); !errors.Is(err, errWrite) {
		t.Errorf("Edit: err = %v, want the store's", err)
	}
	if err := c.Delete(ctx, "s1"); !errors.Is(err, errWrite) {
		t.Errorf("Delete: err = %v, want the store's", err)
	}
}

func TestStatusCreate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project string
		here    bool
		calls   []string
		gid     string
	}{
		{"by name", "新居", false, []string{"ResolveProject 新居", "Me", "CreateStatusLocal"}, "p1"},
		// CreateHere takes the gid as it is, even one that is also the name of a project
		{"here, by gid", "新居", true, []string{"Me", "CreateStatusLocal"}, "新居"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, text, reads := newPostStore(), &fakeText{}, 0
			s := &Status{Store: st, Text: text, Clock: clock(&reads)}
			in := StatusInput{Project: tc.project, Status: "at_risk", Title: "週報", Text: "# 遅れ"}
			create := s.Create
			if tc.here {
				create = s.CreateHere
			}
			gid, err := create(context.Background(), in)
			if err != nil || gid != "local:s1" {
				t.Fatalf("gid %q, err %v", gid, err)
			}
			if !slices.Equal(st.calls, tc.calls) {
				t.Errorf("calls %q, want %q", st.calls, tc.calls)
			}
			want := "project=" + tc.gid + " status=at_risk title=週報 text=status-plain:# 遅れ html=<status># 遅れ</status> author={GID:u-me Name:私} now=" + now.Format(time.RFC3339Nano)
			if len(st.wrote) != 1 || st.wrote[0] != want {
				t.Errorf("wrote %q\nwant %q", st.wrote, want)
			}
			if text.calls != 1 || reads != 1 {
				t.Errorf("converted %d times, read the clock %d times; want 1 and 1", text.calls, reads)
			}
		})
	}
}

// Errors are the store's, as they are (no *FieldError), and nothing comes after one: no
// conversion, no clock reading, no write.
func TestCommentsAndStatusErrors(t *testing.T) {
	errMe := errors.New("me: not synced")
	errWrite := errors.New("コメントが空です")
	for _, tc := range []struct {
		name  string
		setup func(*postStore)
		run   func(*postStore, *fakeText, Clock) error
		want  error
		calls []string
		// whether the conversion and the clock come before the error
		converted bool
	}{
		{"comment: no me", func(f *postStore) { f.meErr = errMe },
			func(f *postStore, x *fakeText, c Clock) error {
				_, err := (&Comments{Store: f, Text: x, Clock: c}).Add(context.Background(), "1", "a")
				return err
			}, errMe, []string{"Me"}, false},
		{"comment: the write fails", func(f *postStore) { f.writeErr = errWrite },
			func(f *postStore, x *fakeText, c Clock) error {
				_, err := (&Comments{Store: f, Text: x, Clock: c}).Add(context.Background(), "1", "a")
				return err
			}, errWrite, []string{"Me", "AddRichCommentLocal"}, true},
		{"status: no such project", func(*postStore) {},
			func(f *postStore, x *fakeText, c Clock) error {
				_, err := (&Status{Store: f, Text: x, Clock: c}).Create(context.Background(), StatusInput{Project: "旧居", Status: "on_track", Text: "a"})
				return err
			}, errNotFound, []string{"ResolveProject 旧居"}, false},
		{"status: no me", func(f *postStore) { f.meErr = errMe },
			func(f *postStore, x *fakeText, c Clock) error {
				_, err := (&Status{Store: f, Text: x, Clock: c}).Create(context.Background(), StatusInput{Project: "新居", Status: "on_track", Text: "a"})
				return err
			}, errMe, []string{"ResolveProject 新居", "Me"}, false},
		{"status here: no me", func(f *postStore) { f.meErr = errMe },
			func(f *postStore, x *fakeText, c Clock) error {
				_, err := (&Status{Store: f, Text: x, Clock: c}).CreateHere(context.Background(), StatusInput{Project: "p1", Status: "on_track", Text: "a"})
				return err
			}, errMe, []string{"Me"}, false},
		{"status: the write fails", func(f *postStore) { f.writeErr = errWrite },
			func(f *postStore, x *fakeText, c Clock) error {
				_, err := (&Status{Store: f, Text: x, Clock: c}).Create(context.Background(), StatusInput{Project: "新居", Status: "bad", Text: "a"})
				return err
			}, errWrite, []string{"ResolveProject 新居", "Me", "CreateStatusLocal"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, text, reads := newPostStore(), &fakeText{}, 0
			tc.setup(st)
			err := tc.run(st, text, clock(&reads))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			var fe *FieldError
			if errors.As(err, &fe) {
				t.Errorf("err is a *FieldError: %v", err)
			}
			if tc.want != errNotFound && err != tc.want {
				t.Errorf("err %v is not the store's error as it is", err)
			}
			if !slices.Equal(st.calls, tc.calls) {
				t.Errorf("calls %q, want %q", st.calls, tc.calls)
			}
			if len(st.wrote) != 0 {
				t.Errorf("wrote %q", st.wrote)
			}
			n := 0
			if tc.converted {
				n = 1
			}
			if text.calls != n || reads != n {
				t.Errorf("converted %d times, read the clock %d times; want %d and %d", text.calls, reads, n, n)
			}
		})
	}
}
