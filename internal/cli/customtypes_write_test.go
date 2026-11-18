package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// Custom task types (#59) in the CLI: task type takes a custom type's name, task status
// one of its statuses, and task show shows them.
func TestCustomTypeCommands(t *testing.T) {
	path := writeCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceProjectCustomTypes(context.Background(), "p1", []asana.CustomType{{GID: "ct1", Name: "バグ", StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cs1", Name: "対応中", CompletionState: "Incomplete"}, {GID: "cs2", Name: "修正済み", CompletionState: "Complete"}}}}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if out, err := runCLI(t, "task", "show", "t1"); err != nil || !strings.Contains(out, "バグ（task type で変える）") {
		t.Fatalf("show = %q, %v", out, err)
	}
	if _, err := runCLI(t, "task", "status", "t1", "対応中"); err == nil || !strings.Contains(err.Error(), "カスタムタイプのタスクではない") {
		t.Fatalf("status on a plain task: %v", err)
	}
	if _, err := runCLI(t, "task", "type", "t1", "機能"); err == nil || !strings.Contains(err.Error(), "カスタムタイプ: バグ") {
		t.Fatalf("unknown type: %v", err)
	}
	if out, err := runCLI(t, "task", "type", "t1", "バグ"); err != nil || !strings.Contains(out, "種類を「バグ」にしました") {
		t.Fatalf("type = %q, %v", out, err)
	}
	if out, err := runCLI(t, "task", "status", "t1", "修正済み"); err != nil || !strings.Contains(out, "ステータスを「修正済み」にしました") {
		t.Fatalf("status = %q, %v", out, err)
	}
	out, err := runCLI(t, "task", "show", "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"バグ", "修正済み", "対応中 / 修正済み（task status で変える）"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if _, err := runCLI(t, "task", "status", "t1", "未着手"); err == nil || !strings.Contains(err.Error(), "対応中 / 修正済み") {
		t.Fatalf("unknown status: %v", err)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].Payload["custom_type"] != "ct1" || ops[0].Payload["custom_type_status_option"] != "cs2" {
		t.Fatalf("queued %+v", ops)
	}
}
