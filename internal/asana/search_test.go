package asana

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestSearchTasksQuery(t *testing.T) {
	done := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspaces/w1/tasks/search" {
			t.Errorf("path %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("text") != "牛乳 買う" || q.Get("completed") != "false" || q.Get("limit") != "100" || q.Get("sort_by") != "modified_at" {
			t.Errorf("query %v", q)
		}
		done = true
		fmt.Fprint(w, `{"data":[{"gid":"t1","name":"牛乳を買う"},{"gid":"t2","name":"x"}]}`)
	})
	open := false
	refs, err := c.SearchTasks(context.Background(), "w1", "牛乳 買う", &open, 500)
	if err != nil || !done || len(refs) != 2 || refs[0].GID != "t1" {
		t.Fatalf("refs %+v err %v", refs, err)
	}
}

func TestSearchTasksWithoutCompletedSendsNoFilter(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["completed"]; ok {
			t.Errorf("completed sent: %v", r.URL.Query())
		}
		if r.URL.Query().Get("limit") != "1" {
			t.Errorf("limit %q", r.URL.Query().Get("limit"))
		}
		fmt.Fprint(w, `{"data":[]}`)
	})
	if _, err := c.SearchTasks(context.Background(), "w1", "x", nil, 0); err != nil {
		t.Fatal(err)
	}
}

func TestSearchTasks402(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"errors":[{"message":"Search is only available to premium users."}]}`)
	})
	_, err := c.SearchTasks(context.Background(), "w1", "x", nil, 10)
	if !IsPaymentRequired(err) {
		t.Fatalf("err %v", err)
	}
	if calls != 1 { // a 402 is not retried
		t.Fatalf("calls %d", calls)
	}
}

func TestTypeahead(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspaces/w1/typeahead" {
			t.Errorf("path %s", r.URL.Path)
		}
		q := r.URL.Query()
		switch q.Get("resource_type") {
		case "task":
			if q.Get("query") != "牛乳" || q.Get("count") != "20" {
				t.Errorf("task query %v", q)
			}
			fmt.Fprint(w, `{"data":[{"gid":"t1","name":"牛乳を買う","resource_type":"task"}]}`)
		case "user":
			if q.Get("opt_fields") != "name,email" {
				t.Errorf("user fields %v", q)
			}
			fmt.Fprint(w, `{"data":[{"gid":"u1","name":"Hanako","email":"h@example.com"}]}`)
		case "tag":
			fmt.Fprint(w, `{"data":[{"gid":"g1","name":"買い物","color":"dark-red"}]}`)
		default:
			t.Errorf("resource_type %v", q)
		}
	})
	ctx := context.Background()
	ts, err := c.TypeaheadTasks(ctx, "w1", "牛乳", 20)
	if err != nil || len(ts) != 1 || ts[0].GID != "t1" {
		t.Fatalf("tasks %+v %v", ts, err)
	}
	us, err := c.TypeaheadUsers(ctx, "w1", "han", 5)
	if err != nil || len(us) != 1 || us[0].Email != "h@example.com" {
		t.Fatalf("users %+v %v", us, err)
	}
	tags, err := c.TypeaheadTags(ctx, "w1", "買", 5)
	if err != nil || len(tags) != 1 || tags[0].Color != "dark-red" {
		t.Fatalf("tags %+v %v", tags, err)
	}
}

func TestTaskByCustomID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/workspaces/w1/tasks/custom_id/ENG-123":
			if r.URL.Query().Get("opt_fields") == "" {
				t.Error("no opt_fields")
			}
			fmt.Fprint(w, `{"data":{"gid":"t1","name":"直す"}}`)
		case "/workspaces/w1/tasks/custom_id/A%2FB":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
		default:
			t.Errorf("path %s", r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	task, err := c.TaskByCustomID(ctx, "w1", "ENG-123")
	if err != nil || task.GID != "t1" || task.Name != "直す" {
		t.Fatalf("task %+v %v", task, err)
	}
	var apiErr *Error
	if _, err := c.TaskByCustomID(ctx, "w1", "A/B"); err == nil || !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("slash: %v", err)
	}
	for _, bad := range []string{"", ".", ".."} {
		if _, err := c.TaskByCustomID(ctx, "w1", bad); err != errBadCustomID {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
