package sync

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
)

// Against the real Asana (#59), custom_type.name alone comes without the gid: a custom task
// was stored without its type, its status could not be set ("not a custom type task"), and
// a PUT with a type looked refused. The fake answers like Asana (askedRef).

// The fake gives no gid for a type asked for by its name only, and gives it when asked.
func TestAskedRefLikeAsana(t *testing.T) {
	for q, want := range map[string]string{
		"custom_type.name":                 `{"name":"バグ"}`,
		"custom_type.gid,custom_type.name": `{"gid":"ct1","name":"バグ"}`,
		"custom_type":                      `{"gid":"ct1","name":"バグ"}`,
		"name":                             "null",
	} {
		r := httptest.NewRequest("GET", "/tasks/t1?opt_fields="+q, nil)
		if got := askedRef(r, "custom_type", "ct1", "バグ"); got != want {
			t.Errorf("%s: %s, want %s", q, got, want)
		}
	}
}

func fetchT1(t *testing.T, s *Syncer) {
	t.Helper()
	if err := s.FetchDetails(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
}

// A custom task read from Asana keeps its type and status; a status set later goes with
// the type and is not taken for refused; a status or type changed in Asana is read again.
func TestCustomTypeReadWithGid(t *testing.T) {
	f, s, st, _ := setupTypes(t)
	ctx := context.Background()
	if err := s.syncCustomTypes(ctx, "p1", "w1", false); err != nil {
		t.Fatal(err)
	}
	f.subtype, f.typ, f.status = "custom", "ct1", "cs-doing"
	fetchT1(t, s)
	d, err := st.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if d.CustomType == nil || d.CustomType.GID != "ct1" || d.CustomType.Name != "バグ" ||
		d.CustomTypeStatus == nil || d.CustomTypeStatus.GID != "cs-doing" || d.CustomTypeStatus.Name != "対応中" {
		t.Fatalf("read without its type: %+v %+v", d.CustomType, d.CustomTypeStatus)
	}

	// The status alone: sent with the type, applied, not dropped.
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"custom_type_status_option": "cs-done"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || len(res.Discarded) != 0 || len(f.puts) != 1 || f.puts[0]["custom_type"] != "ct1" || f.puts[0]["custom_type_status_option"] != "cs-done" {
		t.Fatalf("push %+v, PUT %v", res, f.puts)
	}
	// A Complete status completes the task (locally at once, and in Asana: see
	// typesServer), and the answer to the PUT does not reopen it.
	if d, _ := st.GetTask(ctx, "t1"); d.CustomTypeStatus == nil || d.CustomTypeStatus.GID != "cs-done" || !d.Completed || !f.completed {
		t.Fatalf("after push %+v completed=%v (Asana %v)", d.CustomTypeStatus, d.Completed, f.completed)
	}

	// Only the status changed in Asana.
	f.status = "cs-doing"
	fetchT1(t, s)
	if d, _ := st.GetTask(ctx, "t1"); d.CustomType == nil || d.CustomType.GID != "ct1" || d.CustomTypeStatus == nil || d.CustomTypeStatus.GID != "cs-doing" {
		t.Fatalf("status changed in Asana: %+v %+v", d.CustomType, d.CustomTypeStatus)
	}

	// The type taken off in Asana.
	f.subtype = "default_task"
	fetchT1(t, s)
	if d, _ := st.GetTask(ctx, "t1"); d.CustomType != nil || d.CustomTypeStatus != nil {
		t.Fatalf("type gone in Asana, still %+v %+v", d.CustomType, d.CustomTypeStatus)
	}
}

// An answer whose type or status has no gid cannot be compared with what was sent: the
// change is not taken for refused (which would drop it).
func TestCustomTypeAppliedWithoutGid(t *testing.T) {
	op := outbox.Op{Kind: outbox.KindUpdateTask, TaskGID: "t1",
		Payload: map[string]any{"custom_type_status_option": "cs-done"}, Base: map[string]any{"custom_type": "ct1"}}
	for _, task := range []*model.Task{
		{ResourceSubtype: "custom", CustomType: &model.Ref{Name: "バグ"}, CustomTypeStatusOption: &model.Ref{GID: "cs-done"}},
		{ResourceSubtype: "custom", CustomType: &model.Ref{GID: "ct1"}, CustomTypeStatusOption: &model.Ref{Name: "完了"}},
	} {
		if err := customTypeApplied(op, task); err != nil {
			t.Errorf("%+v: %v", task, err)
		}
	}
	// With the gids, a status Asana did not apply is still caught.
	ignored := &model.Task{ResourceSubtype: "custom", CustomType: &model.Ref{GID: "ct1"}, CustomTypeStatusOption: &model.Ref{GID: "cs-doing"}}
	if customTypeApplied(op, ignored) == nil {
		t.Error("an ignored status is not caught")
	}
}
