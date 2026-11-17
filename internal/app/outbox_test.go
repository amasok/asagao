package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// outboxStore holds one entry and records which writes Resolve makes.
type outboxStore struct {
	op     *outbox.Op
	getErr error
	err    error // returned by the writes
	calls  []string
}

func (f *outboxStore) GetOp(_ context.Context, id int64) (*outbox.Op, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	op := *f.op
	return &op, nil
}

func (f *outboxStore) ResolveKeepLocal(_ context.Context, id int64) error {
	f.calls = append(f.calls, fmt.Sprint("ResolveKeepLocal ", id))
	return f.err
}

func (f *outboxStore) RetryOp(_ context.Context, id int64) error {
	f.calls = append(f.calls, fmt.Sprint("RetryOp ", id))
	return f.err
}

func (f *outboxStore) DiscardOp(_ context.Context, id int64) error {
	f.calls = append(f.calls, fmt.Sprint("DiscardOp ", id))
	return f.err
}

func TestOutboxResolve(t *testing.T) {
	const (
		pending  = outbox.StatePending
		conflict = outbox.StateConflict
		failed   = outbox.StateFailed
		sending  = outbox.StateSending
	)
	for _, tc := range []struct {
		action  ResolveAction
		state   string
		call    string // the store write, "" for none
		push    bool
		refresh string
		wantErr error
	}{
		{KeepLocal, conflict, "ResolveKeepLocal 7", true, "", nil},
		{KeepLocal, pending, "", false, "", ErrWrongState},
		{KeepLocal, failed, "", false, "", ErrWrongState},
		{KeepLocal, sending, "", false, "", readmodel.ErrOpSending},
		{Retry, failed, "RetryOp 7", true, "", nil},
		{Retry, pending, "", false, "", ErrWrongState},
		{Retry, conflict, "", false, "", ErrWrongState},
		{Retry, sending, "", false, "", readmodel.ErrOpSending},
		{KeepRemote, conflict, "DiscardOp 7", false, "1200", nil},
		{KeepRemote, pending, "", false, "", ErrWrongState},
		{KeepRemote, failed, "", false, "", ErrWrongState},
		{KeepRemote, sending, "", false, "", readmodel.ErrOpSending},
		{Discard, pending, "DiscardOp 7", false, "1200", nil},
		{Discard, conflict, "DiscardOp 7", false, "1200", nil},
		{Discard, failed, "DiscardOp 7", false, "1200", nil},
		{Discard, sending, "", false, "", readmodel.ErrOpSending},
		{"explode", pending, "", false, "", nil}, // an error, but not one of these
		{"", conflict, "", false, "", nil},
	} {
		t.Run(string(tc.action)+"/"+tc.state, func(t *testing.T) {
			op := outbox.Op{ID: 7, Kind: outbox.KindUpdateTask, TaskGID: "1200", State: tc.state}
			st := &outboxStore{op: &op}
			r, err := (&Outbox{Store: st}).Resolve(context.Background(), 7, tc.action)
			if tc.call != "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == ErrWrongState && errors.Is(err, readmodel.ErrOpSending) ||
				tc.wantErr == readmodel.ErrOpSending && errors.Is(err, ErrWrongState) {
				t.Fatalf("err = %v matches the other error too", err)
			}
			if want := slices.DeleteFunc([]string{tc.call}, func(s string) bool { return s == "" }); !slices.Equal(st.calls, want) {
				t.Fatalf("store calls = %q, want %q", st.calls, want)
			}
			if r.Op.ID != 7 || r.Op.State != tc.state || r.Action != tc.action || r.Push != tc.push || r.RefreshGID != tc.refresh {
				t.Fatalf("resolution = %+v", r)
			}
		})
	}
}

func TestOutboxResolveErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	if _, err := (&Outbox{Store: &outboxStore{getErr: boom}}).Resolve(ctx, 7, Discard); !errors.Is(err, boom) {
		t.Fatalf("GetOp's error: %v", err)
	}
	for _, tc := range []struct {
		action ResolveAction
		state  string
	}{
		{KeepLocal, outbox.StateConflict},
		{Retry, outbox.StateFailed},
		{KeepRemote, outbox.StateConflict},
		{Discard, outbox.StatePending},
	} {
		st := &outboxStore{op: &outbox.Op{ID: 7, TaskGID: "1200", State: tc.state}, err: boom}
		r, err := (&Outbox{Store: st}).Resolve(ctx, 7, tc.action)
		if !errors.Is(err, boom) {
			t.Errorf("%s: the store's error: %v", tc.action, err)
		}
		if r.RefreshGID != "" {
			t.Errorf("%s: nothing to refresh when the write failed: %+v", tc.action, r)
		}
	}
}

func TestOutboxRefreshTarget(t *testing.T) {
	for _, tc := range []struct {
		kind, task, want string
	}{
		{outbox.KindUpdateTask, "1200", "1200"},
		{outbox.KindMoveTask, "1200", "1200"},
		{outbox.KindAddComment, "1200", "1200"},
		{outbox.KindAddTag, "1200", "1200"},
		{outbox.KindSetCustomField, "1200", "1200"},
		{outbox.KindAddDependency, "1200", "1200"},
		{outbox.KindUpdateTask, "local:ab", ""},   // the task never reached Asana
		{outbox.KindAddComment, "local:ab", ""},   // likewise
		{outbox.KindCreateTask, "1200", ""},       // the local task is gone
		{outbox.KindDuplicateTask, "1200", ""},    // the local copy is gone
		{outbox.KindRenameSection, "1300", ""},    // task_gid is a section
		{outbox.KindCreateStatus, "1400", ""},     // task_gid is a project
		{outbox.KindMarkNotification, "1500", ""}, // task_gid is a notification
	} {
		st := &outboxStore{op: &outbox.Op{ID: 1, Kind: tc.kind, TaskGID: tc.task, State: outbox.StatePending}}
		r, err := (&Outbox{Store: st}).Resolve(context.Background(), 1, Discard)
		if err != nil || r.RefreshGID != tc.want {
			t.Errorf("%s %s: RefreshGID = %q, %v; want %q", tc.kind, tc.task, r.RefreshGID, err, tc.want)
		}
	}
}

func TestStateErrorMessage(t *testing.T) {
	for _, tc := range []struct {
		err  StateError
		want []string
	}{
		{StateError{3, outbox.StatePending, KeepLocal}, []string{"#3 は競合している変更ではありません", "状態: 未送信", "keep_local は競合している変更にだけ使えます"}},
		{StateError{4, outbox.StateFailed, KeepRemote}, []string{"状態: 失敗", "keep_remote は競合している変更"}},
		{StateError{5, outbox.StateConflict, Retry}, []string{"#5 は送信に失敗した変更ではありません", "状態: 競合", "retry は送信に失敗した変更にだけ使えます"}},
	} {
		msg := tc.err.Error()
		for _, w := range tc.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%q does not say %q", msg, w)
			}
		}
		if !errors.Is(&tc.err, ErrWrongState) {
			t.Errorf("%v is not ErrWrongState", &tc.err)
		}
	}
}
