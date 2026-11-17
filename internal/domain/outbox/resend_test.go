package outbox

import (
	"errors"
	"testing"
)

func TestSafeToResend(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name    string
		op      Op
		created bool  // what tagCreated answers
		err     error // and its error
		want    bool
		wantErr error
		asked   bool // tagCreated is called
	}{
		{name: "create task", op: Op{Kind: KindCreateTask}, want: false},
		{name: "duplicate task", op: Op{Kind: KindDuplicateTask}, want: false},
		{name: "add comment", op: Op{Kind: KindAddComment}, want: false},
		{name: "upload attachment", op: Op{Kind: KindUploadAttachment}, want: false},
		{name: "create status", op: Op{Kind: KindCreateStatus}, want: false},
		{name: "update task", op: Op{Kind: KindUpdateTask}, want: true},
		{name: "move task", op: Op{Kind: KindMoveTask}, want: true},
		{name: "remove tag", op: Op{Kind: KindRemoveTag, Payload: map[string]any{"tag": "local:t"}}, want: true},
		{name: "set custom field", op: Op{Kind: KindSetCustomField}, want: true},
		{name: "mark notification", op: Op{Kind: KindMarkNotification}, want: true},
		{name: "unknown kind", op: Op{Kind: "something_new"}, want: true},
		{name: "add existing tag", op: Op{Kind: KindAddTag, Payload: map[string]any{"tag": "123"}}, want: true},
		{name: "add tag without payload", op: Op{Kind: KindAddTag}, want: true},
		{name: "add tag, tag not a string", op: Op{Kind: KindAddTag, Payload: map[string]any{"tag": 1}}, want: true},
		{name: "add local tag not created", op: Op{Kind: KindAddTag, Payload: map[string]any{"tag": "local:t"}}, created: false, want: false, asked: true},
		{name: "add local tag created", op: Op{Kind: KindAddTag, Payload: map[string]any{"tag": "local:t"}}, created: true, want: true, asked: true},
		{name: "add local tag, lookup fails", op: Op{Kind: KindAddTag, Payload: map[string]any{"tag": "local:t"}}, err: boom, want: false, wantErr: boom, asked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asked string
			got, err := SafeToResend(tc.op, func(tag string) (bool, error) {
				asked = tag
				return tc.created, tc.err
			})
			if got != tc.want || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("SafeToResend = %v, %v; want %v, %v", got, err, tc.want, tc.wantErr)
			}
			if tc.asked != (asked != "") {
				t.Fatalf("tagCreated asked for %q, want asked = %v", asked, tc.asked)
			}
			if tc.asked && asked != "local:t" {
				t.Fatalf("tagCreated asked for %q, want the local tag", asked)
			}
		})
	}
}
