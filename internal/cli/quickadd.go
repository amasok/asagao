package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	usecase "github.com/amasok/asagao/internal/app"
)

// quickAddJSON is what `task create --quick --json` says it read from the name.
type quickAddJSON struct {
	Name         string   `json:"name"`
	Due          string   `json:"due_on,omitempty"`
	Assignee     string   `json:"assignee_gid,omitempty"`
	Tags         []string `json:"tag_gids,omitempty"`
	Unread       string   `json:"unread,omitempty"`
	UnreadReason string   `json:"unread_reason,omitempty"`
}

func newQuickAddJSON(q usecase.QuickAddResult) *quickAddJSON {
	j := &quickAddJSON{Name: q.Name, Due: q.Due, Unread: q.Unread, UnreadReason: q.UnreadReason}
	if q.Assignee != nil {
		j.Assignee = q.Assignee.GID
	}
	for _, t := range q.Tags {
		j.Tags = append(j.Tags, t.GID)
	}
	return j
}

// readQuickAdd reads the quick-add tokens at the end of o.name (app.ParseQuickAdd) into o:
// the name loses them, and the assignee, due date and tags are set. A token that names
// what a flag also gives (--due, --assignee) is an error, before anything is written.
func readQuickAdd(ctx context.Context, st Store, o *createOpts, now time.Time) (*usecase.QuickAddResult, error) {
	q, err := usecase.ReadQuickAdd(ctx, st, o.name, now)
	if err != nil {
		return nil, err
	}
	if q.Due != "" && o.due != "" {
		return nil, errors.New("--quick: 期日が --due と名前の両方で指定されています")
	}
	if q.Assignee != nil && o.assignee != "" {
		return nil, errors.New("--quick: 担当が --assignee と名前の両方で指定されています")
	}
	o.name = q.Name
	if q.Due != "" {
		o.due = q.Due
	}
	switch {
	case q.AssigneeIsMe:
		o.assignee = "me" // the store's MeGID, even when I am not in the users table
	case q.Assignee != nil:
		o.assignee = q.Assignee.GID
	}
	for _, t := range q.Tags {
		o.tags = append(o.tags, t.GID)
	}
	return &q, nil
}

// printQuickAdd says what --quick read, and which token it did not read and why.
func printQuickAdd(w io.Writer, q usecase.QuickAddResult) {
	if s := q.Read(); s != "" {
		fmt.Fprintf(w, "読み取り: %s\n", s)
	}
	if s := q.Problem(); s != "" {
		fmt.Fprintln(w, s)
	}
}
