package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// briefStore is a project p1 (in w1) with a brief, recording the brief changes.
type briefStore struct {
	overview readmodel.ProjectOverview
	plan     string // the PlanRefusedAt RFC3339 value of w1
	edits    []readmodel.BriefEdit
}

func (b *briefStore) ResolveProject(_ context.Context, arg string) (string, error) {
	if arg == "新居" || arg == "p1" {
		return "p1", nil
	}
	return "", errors.New("no such project")
}
func (b *briefStore) ProjectWorkspace(context.Context, string) (string, error) { return "w1", nil }
func (b *briefStore) PlanRefusedAt(_ context.Context, feature, workspaceGID string) (time.Time, bool, error) {
	if feature == plan.ProjectBrief && workspaceGID == "w1" && b.plan != "" {
		if at, err := time.Parse(time.RFC3339, b.plan); err == nil {
			return at, true, nil
		}
	}
	return time.Time{}, false, nil
}
func (b *briefStore) ProjectOverview(context.Context, string) (*readmodel.ProjectOverview, error) {
	o := b.overview
	return &o, nil
}
func (b *briefStore) UpdateProjectBriefLocal(_ context.Context, _ string, e readmodel.BriefEdit, _ time.Time) error {
	b.edits = append(b.edits, e)
	return nil
}

// briefText is a BriefText that marks what it converted.
type briefText struct{}

func (briefText) BriefMarkdown(h string) string {
	return strings.TrimSuffix(strings.TrimPrefix(h, "<body>"), "</body>")
}
func (briefText) ProjectBrief(md, _ string) (string, string, error) {
	if strings.Contains(md, "bad") {
		return "", "", errors.New("bad block")
	}
	return "<body>" + md + "</body>", "plain:" + md, nil
}

func newBriefs(o readmodel.ProjectOverview) (*briefStore, *Briefs) {
	st := &briefStore{overview: o}
	return st, &Briefs{Store: st, Text: briefText{}, Clock: func() time.Time { return now }}
}

var withBrief = readmodel.ProjectOverview{GID: "p1", BriefState: readmodel.BriefAvailable,
	Brief: &readmodel.ProjectBrief{Title: "目的", Text: "年内", HTMLText: "<body>年内</body>"}}

// #60: a title alone keeps the body as it is; the body as it was (give or take
// whitespace) with the same title writes nothing.
func TestBriefSaveKeepsWhatIsNotGiven(t *testing.T) {
	st, briefs := newBriefs(withBrief)
	if changed, err := briefs.SaveHere(context.Background(), "p1", BriefInput{Title: ptr("計画")}); err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	if e := st.edits[0]; e.Title != "計画" || e.HTMLText != "<body>年内</body>" || e.Text != "年内" {
		t.Fatalf("edit = %+v", e)
	}
	if changed, err := briefs.SaveHere(context.Background(), "p1", BriefInput{Title: ptr("目的"), Markdown: ptr("年内\n\n")}); err != nil || changed || len(st.edits) != 1 {
		t.Fatalf("unchanged: %v %v %+v", changed, err, st.edits)
	}
}

// A new brief needs a title or a body; with a title only its body is an empty <body>
// (Asana is not sent an empty html_text).
func TestBriefCreate(t *testing.T) {
	st, briefs := newBriefs(readmodel.ProjectOverview{GID: "p1", BriefState: readmodel.BriefNone})
	if _, err := briefs.SaveHere(context.Background(), "p1", BriefInput{Markdown: ptr("  \n")}); !errors.Is(err, ErrEmptyBrief) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := briefs.SaveHere(context.Background(), "p1", BriefInput{Title: ptr("概要")}); err != nil {
		t.Fatal(err)
	}
	if e := st.edits[0]; e.Title != "概要" || e.HTMLText != "<body></body>" {
		t.Fatalf("edit = %+v", e)
	}
}

// A body the converter refuses is a FieldError; a workspace refused with 402 is
// ErrPlanUnavailable, a project's brief refused with 403 ErrBriefForbidden; deleting a brief that is not there is ErrNoBrief. Nothing written.
func TestBriefRefusals(t *testing.T) {
	ctx := context.Background()
	st, briefs := newBriefs(withBrief)
	var fe *FieldError
	if _, err := briefs.SaveHere(ctx, "p1", BriefInput{Markdown: ptr("bad")}); !errors.As(err, &fe) {
		t.Fatalf("bad block: %v", err)
	}
	st.plan = now.Add(-time.Hour).UTC().Format(time.RFC3339)
	var pe *ErrPlanUnavailable
	if _, err := briefs.SaveHere(ctx, "p1", BriefInput{Title: ptr("x")}); !errors.As(err, &pe) || pe.Feature != plan.ProjectBrief {
		t.Fatalf("refused: %v", err)
	}
	if err := briefs.DeleteHere(ctx, "p1"); !errors.As(err, &pe) {
		t.Fatalf("refused delete: %v", err)
	}
	st.plan = now.Add(-25 * time.Hour).UTC().Format(time.RFC3339) // tried again after a day
	if _, err := briefs.SaveHere(ctx, "p1", BriefInput{Title: ptr("x")}); err != nil {
		t.Fatalf("after a day: %v", err)
	}
	st.edits = nil
	st.overview = readmodel.ProjectOverview{GID: "p1", BriefState: readmodel.BriefUnavailable} // a 403 for p1
	if _, err := briefs.SaveHere(ctx, "p1", BriefInput{Title: ptr("x")}); !errors.Is(err, ErrBriefForbidden) || errors.As(err, &pe) {
		t.Fatalf("unavailable: %v", err)
	}
	st.overview = readmodel.ProjectOverview{GID: "p1", BriefState: readmodel.BriefNone}
	if _, err := briefs.Delete(ctx, "新居"); !errors.Is(err, ErrNoBrief) {
		t.Fatalf("delete none: %v", err)
	}
	if len(st.edits) != 0 {
		t.Fatalf("written: %+v", st.edits)
	}
}
