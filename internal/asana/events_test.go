package asana

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEvents412GivesToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resource") != "p1" {
			t.Errorf("query %v", r.URL.Query())
		}
		switch r.URL.Query().Get("sync") {
		case "":
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, `{"errors":[{"message":"Sync token invalid or too old."}],"sync":"new"}`)
		case "new":
			fmt.Fprint(w, `{"data":[{"action":"changed","resource":{"gid":"t1","resource_type":"task"},"change":{"field":"name","action":"changed"}}],"sync":"next","has_more":true}`)
		}
	})
	ctx := context.Background()
	_, err := c.Events(ctx, "p1", "")
	tok, ok := SyncToken(err)
	if !ok || tok != "new" {
		t.Fatalf("412: token %q ok %v err %v", tok, ok, err)
	}
	p, err := c.Events(ctx, "p1", "new")
	if err != nil || p.Sync != "next" || !p.HasMore || len(p.Events) != 1 ||
		p.Events[0].Resource.GID != "t1" || p.Events[0].Resource.ResourceType != "task" || p.Events[0].Change.Field != "name" {
		t.Fatalf("page = %+v %v", p, err)
	}
	if _, ok := SyncToken(&Error{Status: 412}); ok {
		t.Fatal("412 without a token is not a sync token")
	}
	if _, ok := SyncToken(&Error{Status: 500, Sync: "x"}); ok {
		t.Fatal("only 412 carries a sync token")
	}
}

func TestTasksByGIDBatch(t *testing.T) {
	var batches, singles int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/batch":
			batches++
			b, _ := io.ReadAll(r.Body)
			var res []string
			for _, gid := range []string{"a", "b", "c", "d"} {
				if !strings.Contains(string(b), `"/tasks/`+gid+`"`) {
					continue
				}
				switch gid {
				case "b":
					res = append(res, `{"status_code":404,"body":{"errors":[{"message":"gone"}]}}`)
				case "c":
					res = append(res, `{"status_code":400,"body":{"errors":[{"message":"bad field"}]}}`)
				default:
					res = append(res, fmt.Sprintf(`{"status_code":200,"body":{"data":{"gid":%q,"name":"N"}}}`, gid))
				}
			}
			fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(res, ","))
		case r.URL.Path == "/tasks/c":
			singles++
			fmt.Fprint(w, `{"data":{"gid":"c","name":"C"}}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	tasks, gone, err := c.TasksByGID(context.Background(), []string{"a", "b", "c", "d"})
	if err != nil || len(tasks) != 3 || len(gone) != 1 || gone[0] != "b" || batches != 1 || singles != 1 {
		t.Fatalf("tasks %+v gone %v err %v batches %d singles %d", tasks, gone, err, batches, singles)
	}
}

// A 5xx to the batch (which only reads) is retried; one refused as a whole falls back
// to reading the tasks one by one; a 429 inside the batch is an error.
func TestTasksByGIDBatchFailures(t *testing.T) {
	t.Run("5xx retried", func(t *testing.T) {
		n := 0
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if n++; n == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			fmt.Fprint(w, `{"data":[{"status_code":200,"body":{"data":{"gid":"a"}}}]}`)
		})
		if tasks, _, err := c.TasksByGID(context.Background(), []string{"a"}); err != nil || len(tasks) != 1 {
			t.Fatalf("%v %v", tasks, err)
		}
	})
	t.Run("batch refused", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/batch" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if r.URL.Path == "/tasks/x" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprint(w, `{"data":{"gid":"a"}}`)
		})
		tasks, gone, err := c.TasksByGID(context.Background(), []string{"a", "x"})
		if err != nil || len(tasks) != 1 || len(gone) != 1 {
			t.Fatalf("%v %v %v", tasks, gone, err)
		}
	})
	t.Run("429 inside", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"data":[{"status_code":429,"body":{"errors":[{"message":"slow down"}]}}]}`)
		})
		if _, _, err := c.TasksByGID(context.Background(), []string{"a"}); err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("err = %v", err)
		}
	})
}
