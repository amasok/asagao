package outbox

import "testing"

func TestPayloadName(t *testing.T) {
	for _, c := range []struct {
		kind    Kind
		payload map[string]any
		want    string
	}{
		{KindCreateTask, map[string]any{"workspace": "w", "name": "買い物"}, "買い物"},
		{KindDeleteTask, map[string]any{"name": "消した"}, "消した"},
		{KindUpdateTag, map[string]any{"name": "新しい名前"}, "新しい名前"},
		{KindUpdateTag, map[string]any{"color": "red"}, ""},
		{KindRenameSection, map[string]any{"name": "見出し", "from_future": 1}, "見出し"}, // held: as far as it reads
		{KindRenameSection, map[string]any{"name": 5}, ""},
		{KindAddFollower, map[string]any{"user": "u1", "user_name": "誰か"}, ""}, // no name
		{Kind("from_future"), map[string]any{"name": "新しい種類"}, "新しい種類"},        // the stored form's
		{Kind("from_future"), map[string]any{"name": 5}, ""},
	} {
		if got := PayloadName(Op{Kind: c.kind, Payload: c.payload}); got != c.want {
			t.Errorf("PayloadName(%s %v) = %q, want %q", c.kind, c.payload, got, c.want)
		}
	}
}
