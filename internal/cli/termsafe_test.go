package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/config"
)

// OSC 52 (clipboard), OSC 8 (link), APC (kitty graphics) and a C1 CSI.
const evil = "\x1b]52;c;ZXZpbA==\x07\x1b]8;;https://evil.example\x1b\\\x1b_Ga=T;AAAA\x1b\\\u009b2J"

func assertNoControls(t *testing.T, out string) {
	t.Helper()
	for _, r := range out {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("control character %U in output:\n%q", r, out)
		}
	}
}

// Whatever a command prints goes through termsafe, even text that never was in the store.
func TestCLIOutputIsTerminalSafe(t *testing.T) {
	root := newRoot(&app{baseURL: unreachableAsana(t)})
	root.AddCommand(&cobra.Command{Use: "echo-evil", RunE: func(cmd *cobra.Command, _ []string) error {
		fmt.Fprintln(cmd.OutOrStdout(), "out"+evil)
		fmt.Fprintln(cmd.ErrOrStderr(), "err"+evil)
		return nil
	}})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"echo-evil"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertNoControls(t, out.String())
	if !strings.Contains(out.String(), "out]52;c;ZXZpbA==") || !strings.Contains(out.String(), "err]52") {
		t.Fatalf("text around the escapes should stay: %q", out.String())
	}
}

func TestCLITaskOutputIsTerminalSafe(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st := createStoreAt(t, filepath.Join(config.DataDir(), "store.db"))
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "e1", Name: "悪い" + evil, Notes: "本文" + evil,
		Assignee:    &asana.Ref{GID: "me", Name: "Me"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	for _, args := range [][]string{{"task", "list"}, {"task", "show", "e1"}, {"search", "悪い"}} {
		out, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out, "悪い") {
			t.Fatalf("%v: the task should be listed: %q", args, out)
		}
		assertNoControls(t, out)
	}
}
