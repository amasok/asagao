package asana

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// Tasks are read with their custom type and its status (#59), as optional fields: a
// workspace that refuses them still syncs its tasks.
func TestTaskFieldsHaveCustomType(t *testing.T) {
	for _, f := range []string{"custom_type.name", "custom_type_status_option.name"} {
		if !slices.Contains(TaskFields, f) {
			t.Errorf("TaskFields lack %s", f)
		}
		if !isOptionalTaskField(f) {
			t.Errorf("%s is not optional", f)
		}
	}
}

// A plan that refuses only the custom type fields (#59) leaves out those, not the other
// optional fields: custom fields and dependencies are still read, and later requests do
// not ask for the custom type.
func TestCustomTypeFieldsRefusedAlone(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := r.URL.Query().Get("opt_fields")
		queries = append(queries, f)
		if strings.Contains(f, "custom_type") {
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(w, `{"errors":[{"message":"payment required"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":{"gid":"t1","name":"A","custom_fields":[],"dependencies":[{"gid":"t2"}]}}`)
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	c.BaseURL = srv.URL
	c.MaxRetries = 0
	for i := 0; i < 2; i++ {
		task, err := c.Task(context.Background(), "t1")
		if err != nil {
			t.Fatal(err)
		}
		if task.Partial || !task.NoCustomType || task.CustomFields == nil || len(task.Dependencies) != 1 {
			t.Fatalf("read %d: partial=%v noCustomType=%v fields=%v deps=%v", i, task.Partial, task.NoCustomType, task.CustomFields, task.Dependencies)
		}
	}
	if len(queries) != 3 || !strings.Contains(queries[1], "custom_fields") || strings.Contains(queries[2], "custom_type") {
		t.Fatalf("queries %q", queries)
	}
}

// A plan that refuses the other optional fields too still gets the plain fields.
func TestOptionalFieldsRefusedAfterCustomType(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		f := r.URL.Query().Get("opt_fields")
		if strings.Contains(f, "custom_fields") || strings.Contains(f, "custom_type") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"errors":[{"message":"no"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":{"gid":"t1","name":"A"}}`)
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	c.BaseURL = srv.URL
	c.MaxRetries = 0
	task, err := c.Task(context.Background(), "t1")
	if err != nil || !task.Partial || !task.NoCustomType || n != 3 {
		t.Fatalf("task %+v, %v, %d requests", task, err, n)
	}
}
