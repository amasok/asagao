package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// fakeQueryStore records the method called and its arguments, and returns what ret holds
// for it.
type fakeQueryStore struct {
	called string
	args   []any
	err    error
}

func (f *fakeQueryStore) rec(name string, args ...any) { f.called, f.args = name, args }

var (
	qTasks   = []readmodel.TaskSummary{{GID: "t1"}}
	qFilter  = readmodel.TaskFilter{ProjectGID: "p1"}
	qDetail  = &readmodel.TaskDetail{Notes: "n"}
	qByGID   = map[string]readmodel.TaskSummary{"t1": {GID: "t1"}}
	qUsers   = []readmodel.UserRef{{GID: "u1"}}
	qAtt     = &readmodel.AttachmentView{GID: "a1"}
	qRefs    = []model.Ref{{GID: "g1", Name: "急ぎ"}}
	qTags    = []readmodel.TagCount{{GID: "g1"}}
	qOptions = []readmodel.FieldOption{{FieldGID: "f1"}}
	qField   = readmodel.CustomFieldView{GID: "f1"}
	qProjs   = []readmodel.ProjectView{{GID: "p1"}}
	qStatus  = []readmodel.StatusUpdateView{{GID: "s1"}}
	qInbox   = readmodel.InboxState{Status: readmodel.InboxOK}
	qNotes   = []readmodel.Notification{{GID: "n1"}}
	qStats   = readmodel.Stats{Tasks: 3}
	qOps     = []outbox.Op{{ID: 1}}
	qDiscard = []readmodel.Discard{{ID: 2}}
)

func (f *fakeQueryStore) ListTasks(_ context.Context, fl readmodel.TaskFilter) ([]readmodel.TaskSummary, error) {
	f.rec("ListTasks", fl)
	return qTasks, f.err
}
func (f *fakeQueryStore) BuildFilter(_ context.Context, spec readmodel.FilterSpec, now time.Time) (readmodel.TaskFilter, error) {
	f.rec("BuildFilter", spec, now)
	return qFilter, f.err
}
func (f *fakeQueryStore) GetTask(_ context.Context, gid string) (*readmodel.TaskDetail, error) {
	f.rec("GetTask", gid)
	return qDetail, f.err
}
func (f *fakeQueryStore) TaskSummaries(_ context.Context, gids []string) (map[string]readmodel.TaskSummary, error) {
	f.rec("TaskSummaries", gids)
	return qByGID, f.err
}
func (f *fakeQueryStore) TaskFollowers(_ context.Context, gid string) ([]readmodel.UserRef, error) {
	f.rec("TaskFollowers", gid)
	return qUsers, f.err
}
func (f *fakeQueryStore) TaskTags(_ context.Context, gid string) ([]model.Ref, error) {
	f.rec("TaskTags", gid)
	return qRefs, f.err
}
func (f *fakeQueryStore) GetAttachment(_ context.Context, gid string) (*readmodel.AttachmentView, error) {
	f.rec("GetAttachment", gid)
	return qAtt, f.err
}
func (f *fakeQueryStore) RealGID(_ context.Context, gid string) (string, error) {
	f.rec("RealGID", gid)
	return "real", f.err
}
func (f *fakeQueryStore) ListTags(context.Context) ([]readmodel.TagCount, error) {
	f.rec("ListTags")
	return qTags, f.err
}
func (f *fakeQueryStore) ListFieldOptions(context.Context) ([]readmodel.FieldOption, error) {
	f.rec("ListFieldOptions")
	return qOptions, f.err
}
func (f *fakeQueryStore) ResolveCustomField(_ context.Context, taskGID, arg string) (readmodel.CustomFieldView, error) {
	f.rec("ResolveCustomField", taskGID, arg)
	return qField, f.err
}
func (f *fakeQueryStore) ListUsers(context.Context) ([]readmodel.UserRef, error) {
	f.rec("ListUsers")
	return qUsers, f.err
}
func (f *fakeQueryStore) MeGID(context.Context) (string, error) {
	f.rec("MeGID")
	return "me", f.err
}
func (f *fakeQueryStore) ResolveUser(_ context.Context, arg string) (string, error) {
	f.rec("ResolveUser", arg)
	return "u1", f.err
}
func (f *fakeQueryStore) ResolveUsers(_ context.Context, args []string) ([]string, error) {
	f.rec("ResolveUsers", args)
	return []string{"u1", "u2"}, f.err
}
func (f *fakeQueryStore) ListProjects(_ context.Context, memberOnly bool) ([]readmodel.ProjectView, error) {
	f.rec("ListProjects", memberOnly)
	return qProjs, f.err
}
func (f *fakeQueryStore) ResolveProject(_ context.Context, arg string) (string, error) {
	f.rec("ResolveProject", arg)
	return "p1", f.err
}
func (f *fakeQueryStore) ProjectMembers(_ context.Context, gid string) ([]readmodel.UserRef, error) {
	f.rec("ProjectMembers", gid)
	return qUsers, f.err
}
func (f *fakeQueryStore) StatusUpdates(_ context.Context, gid string) ([]readmodel.StatusUpdateView, error) {
	f.rec("StatusUpdates", gid)
	return qStatus, f.err
}
func (f *fakeQueryStore) InboxState(context.Context) (readmodel.InboxState, error) {
	f.rec("InboxState")
	return qInbox, f.err
}
func (f *fakeQueryStore) ListNotifications(_ context.Context, fl readmodel.NotificationFilter) ([]readmodel.Notification, error) {
	f.rec("ListNotifications", fl)
	return qNotes, f.err
}
func (f *fakeQueryStore) UnreadNotifications(context.Context) (int, error) {
	f.rec("UnreadNotifications")
	return 4, f.err
}
func (f *fakeQueryStore) Stats(context.Context) (readmodel.Stats, error) {
	f.rec("Stats")
	return qStats, f.err
}
func (f *fakeQueryStore) OutboxCounts(context.Context) (int, int, int, error) {
	f.rec("OutboxCounts")
	return 1, 2, 3, f.err
}
func (f *fakeQueryStore) ListOps(context.Context) ([]outbox.Op, error) {
	f.rec("ListOps")
	return qOps, f.err
}
func (f *fakeQueryStore) UnsentCount(context.Context) (int, error) {
	f.rec("UnsentCount")
	return 5, f.err
}
func (f *fakeQueryStore) UnsentState(context.Context) (int, int64, error) {
	f.rec("UnsentState")
	return 6, 7, f.err
}
func (f *fakeQueryStore) PendingDiscards(context.Context) ([]readmodel.Discard, error) {
	f.rec("PendingDiscards")
	return qDiscard, f.err
}
func (f *fakeQueryStore) RecentDiscards(_ context.Context, limit int) ([]readmodel.Discard, error) {
	f.rec("RecentDiscards", limit)
	return qDiscard, f.err
}

// Each method of Query calls the store's of the same name, with the same arguments, and
// returns what it returned, the error included.
func TestQueryCallsTheStore(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	spec := readmodel.FilterSpec{Project: "仕事"}
	nf := readmodel.NotificationFilter{UnreadOnly: true, Limit: 3}
	for _, tc := range []struct {
		name string
		call func(q Query) (out []any, err error)
		args []any
		want []any
	}{
		{"ListTasks", func(q Query) ([]any, error) { v, err := q.ListTasks(ctx, qFilter); return []any{v}, err }, []any{qFilter}, []any{qTasks}},
		{"BuildFilter", func(q Query) ([]any, error) { v, err := q.BuildFilter(ctx, spec, now); return []any{v}, err }, []any{spec, now}, []any{qFilter}},
		{"GetTask", func(q Query) ([]any, error) { v, err := q.GetTask(ctx, "t1"); return []any{v}, err }, []any{"t1"}, []any{qDetail}},
		{"TaskSummaries", func(q Query) ([]any, error) { v, err := q.TaskSummaries(ctx, []string{"t1"}); return []any{v}, err }, []any{[]string{"t1"}}, []any{qByGID}},
		{"TaskFollowers", func(q Query) ([]any, error) { v, err := q.TaskFollowers(ctx, "t1"); return []any{v}, err }, []any{"t1"}, []any{qUsers}},
		{"TaskTags", func(q Query) ([]any, error) { v, err := q.TaskTags(ctx, "t1"); return []any{v}, err }, []any{"t1"}, []any{qRefs}},
		{"GetAttachment", func(q Query) ([]any, error) { v, err := q.GetAttachment(ctx, "a1"); return []any{v}, err }, []any{"a1"}, []any{qAtt}},
		{"RealGID", func(q Query) ([]any, error) { v, err := q.RealGID(ctx, "local:1"); return []any{v}, err }, []any{"local:1"}, []any{"real"}},
		{"ListTags", func(q Query) ([]any, error) { v, err := q.ListTags(ctx); return []any{v}, err }, nil, []any{qTags}},
		{"ListFieldOptions", func(q Query) ([]any, error) { v, err := q.ListFieldOptions(ctx); return []any{v}, err }, nil, []any{qOptions}},
		{"ResolveCustomField", func(q Query) ([]any, error) {
			v, err := q.ResolveCustomField(ctx, "t1", "優先度")
			return []any{v}, err
		}, []any{"t1", "優先度"}, []any{qField}},
		{"ListUsers", func(q Query) ([]any, error) { v, err := q.ListUsers(ctx); return []any{v}, err }, nil, []any{qUsers}},
		{"MeGID", func(q Query) ([]any, error) { v, err := q.MeGID(ctx); return []any{v}, err }, nil, []any{"me"}},
		{"ResolveUser", func(q Query) ([]any, error) { v, err := q.ResolveUser(ctx, "花子"); return []any{v}, err }, []any{"花子"}, []any{"u1"}},
		{"ResolveUsers", func(q Query) ([]any, error) { v, err := q.ResolveUsers(ctx, []string{"a", "b"}); return []any{v}, err }, []any{[]string{"a", "b"}}, []any{[]string{"u1", "u2"}}},
		{"ListProjects", func(q Query) ([]any, error) { v, err := q.ListProjects(ctx, true); return []any{v}, err }, []any{true}, []any{qProjs}},
		{"ResolveProject", func(q Query) ([]any, error) { v, err := q.ResolveProject(ctx, "仕事"); return []any{v}, err }, []any{"仕事"}, []any{"p1"}},
		{"ProjectMembers", func(q Query) ([]any, error) { v, err := q.ProjectMembers(ctx, "p1"); return []any{v}, err }, []any{"p1"}, []any{qUsers}},
		{"StatusUpdates", func(q Query) ([]any, error) { v, err := q.StatusUpdates(ctx, "p1"); return []any{v}, err }, []any{"p1"}, []any{qStatus}},
		{"InboxState", func(q Query) ([]any, error) { v, err := q.InboxState(ctx); return []any{v}, err }, nil, []any{qInbox}},
		{"ListNotifications", func(q Query) ([]any, error) { v, err := q.ListNotifications(ctx, nf); return []any{v}, err }, []any{nf}, []any{qNotes}},
		{"UnreadNotifications", func(q Query) ([]any, error) { v, err := q.UnreadNotifications(ctx); return []any{v}, err }, nil, []any{4}},
		{"Stats", func(q Query) ([]any, error) { v, err := q.Stats(ctx); return []any{v}, err }, nil, []any{qStats}},
		{"OutboxCounts", func(q Query) ([]any, error) { a, b, c, err := q.OutboxCounts(ctx); return []any{a, b, c}, err }, nil, []any{1, 2, 3}},
		{"ListOps", func(q Query) ([]any, error) { v, err := q.ListOps(ctx); return []any{v}, err }, nil, []any{qOps}},
		{"UnsentCount", func(q Query) ([]any, error) { v, err := q.UnsentCount(ctx); return []any{v}, err }, nil, []any{5}},
		{"UnsentState", func(q Query) ([]any, error) { n, id, err := q.UnsentState(ctx); return []any{n, id}, err }, nil, []any{6, int64(7)}},
		{"PendingDiscards", func(q Query) ([]any, error) { v, err := q.PendingDiscards(ctx); return []any{v}, err }, nil, []any{qDiscard}},
		{"RecentDiscards", func(q Query) ([]any, error) { v, err := q.RecentDiscards(ctx, 20); return []any{v}, err }, []any{20}, []any{qDiscard}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boom := errors.New("boom")
			f := &fakeQueryStore{err: boom}
			out, err := tc.call(Query{Store: f})
			if f.called != tc.name {
				t.Errorf("called %q, want %q", f.called, tc.name)
			}
			if !reflect.DeepEqual(f.args, tc.args) {
				t.Errorf("args %v, want %v", f.args, tc.args)
			}
			if !reflect.DeepEqual(out, tc.want) {
				t.Errorf("returned %v, want %v", out, tc.want)
			}
			if err != boom {
				t.Errorf("err = %v, want the store's", err)
			}
		})
	}
}
