package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	gosync "sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

// With every kind of change queued, no tool of a read-only server (called with any
// arguments, incremental or full sync, twice) sends anything but GET to Asana.
func TestReadOnlyServerNeverWrites(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	me := store.UserRef{GID: "me", Name: "Me"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true, "name": "変更"}, testNow))
	_, err := st.CreateTaskLocal(ctx, store.NewTask{WorkspaceGID: "w1", Name: "新しいタスク"}, testNow)
	must(err)
	_, err = st.AddCommentLocal(ctx, "t2", "コメント", me, testNow)
	must(err)
	must(st.MoveTaskLocal(ctx, "t2", "p1", "s1", testNow))
	_, err = st.AddTagLocal(ctx, "t3", "", "新しいタグ", testNow)
	must(err)
	_, err = st.DuplicateTaskLocal(ctx, "t1", "複製", testNow)
	must(err)
	must(st.RenameSectionLocal(ctx, "s2", "改名", testNow))
	_, err = st.CreateStatusLocal(ctx, "p1", "on_track", "状況", "本文", "", me, testNow)
	must(err)

	var mu gosync.Mutex
	var writes []string
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			writes = append(writes, r.Method+" "+r.URL.Path)
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		gets++
		switch r.URL.Path {
		case "/users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me","workspaces":[{"gid":"w1","name":"WS"}]}}`)
		case "/workspaces":
			fmt.Fprint(w, `{"data":[{"gid":"w1","name":"WS"}]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	cs := connect(t, &Server{Store: st, ReadOnly: true, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL = srv.URL
		return &sync.Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}}, nil
	}})
	before, _, _, _ := st.OutboxCounts(ctx)

	args := map[string][]map[string]any{
		"sync":     {{}, {"full": true}, {"full": false}},
		"get_task": {{"gid": "t1"}, {"gid": "t1", "offline": false}},
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		for _, tool := range tools.Tools {
			for _, a := range append(args[tool.Name], map[string]any{"gid": "t1", "project": "p1"}) {
				if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: a}); err != nil {
					t.Logf("%s %v: %v", tool.Name, a, err)
				}
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(writes) > 0 {
		t.Errorf("read-only server sent writes to Asana: %v", writes)
	}
	if gets == 0 {
		t.Error("sync should have pulled")
	}
	if after, _, _, _ := st.OutboxCounts(ctx); after != before {
		t.Errorf("queued changes went from %d to %d (dropped or sent)", before, after)
	}
}
