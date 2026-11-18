package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// task show shows a custom task type (#59) and its status by name.
func TestShowCustomType(t *testing.T) {
	path := writeCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(context.Background(), []asana.Task{{GID: "t1", Name: "ソファを買う", ResourceSubtype: "custom",
		Workspace: &asana.Ref{GID: "w1"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}},
		CustomType: &asana.Ref{GID: "ct1", Name: "バグ"}, CustomTypeStatusOption: &asana.Ref{GID: "cs1", Name: "対応中"}}}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	out, err := runCLI(t, "task", "show", "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"バグ", "対応中"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "カスタムタイプ") {
		t.Errorf("shows the subtype instead of the type's name:\n%s", out)
	}
}
