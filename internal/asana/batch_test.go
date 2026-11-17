package asana

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Batch sends the writes as the actions of one POST /batch and returns each answer.
func TestBatchWrites(t *testing.T) {
	var got []map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/batch" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Data struct {
				Actions []map[string]any `json:"actions"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		got = body.Data.Actions
		fmt.Fprint(w, `{"data":[
			{"status_code":200,"body":{"data":{"gid":"1","name":"done","completed":true}}},
			{"status_code":200,"body":{"data":{}}},
			{"status_code":404,"body":{"errors":[{"message":"not found"}]}},
			{"status_code":500,"body":null}]}`)
	})
	res, err := c.Batch(context.Background(), []BatchWrite{
		{Method: http.MethodPut, Path: "/tasks/1", Data: map[string]any{"completed": true}, Task: true},
		{Method: http.MethodPost, Path: "/tasks/2/addTag", Data: map[string]any{"tag": "g"}},
		{Method: http.MethodPost, Path: "/tasks/3/addTag", Data: map[string]any{"tag": "g"}},
		{Method: http.MethodPost, Path: "/tasks/4/addTag", Data: map[string]any{"tag": "g"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0]["method"] != "put" || got[0]["relative_path"] != "/tasks/1" ||
		fmt.Sprint(got[0]["data"]) != "map[completed:true]" || got[1]["method"] != "post" {
		t.Fatalf("actions = %v", got)
	}
	if f, _ := got[0]["options"].(map[string]any)["fields"].([]any); len(f) == 0 || got[1]["options"] != nil {
		t.Fatalf("only a task write asks for fields: %v", got)
	}
	if len(res) != 4 || res[0].Err != nil || res[0].Task == nil || !res[0].Task.Completed || res[1].Err != nil || res[1].Task != nil {
		t.Fatalf("results = %+v", res)
	}
	if e, ok := res[2].Err.(*Error); !ok || e.Status != 404 || !strings.Contains(e.Error(), "not found") {
		t.Fatalf("404 = %v", res[2].Err)
	}
	if e, ok := res[3].Err.(*Error); !ok || e.Status != 500 {
		t.Fatalf("500 = %v", res[3].Err)
	}
}

func TestBatchFails(t *testing.T) {
	answer := ""
	status := http.StatusOK
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, answer)
	})
	c.MaxRetries = 0
	one := []BatchWrite{{Method: http.MethodPost, Path: "/tasks/1/addTag"}}

	answer = `{"data":[]}` // fewer answers than requests
	if _, err := c.Batch(context.Background(), one); err == nil {
		t.Error("a missing answer should fail the batch")
	}
	status, answer = http.StatusServiceUnavailable, `{"errors":[{"message":"down"}]}`
	if _, err := c.Batch(context.Background(), one); err == nil || err.(*Error).Status != 503 {
		t.Errorf("503 = %v", err)
	}
	if _, err := c.Batch(context.Background(), make([]BatchWrite, MaxBatchActions+1)); err == nil {
		t.Error("more than MaxBatchActions should be refused")
	}
	if res, err := c.Batch(context.Background(), nil); err != nil || res != nil {
		t.Errorf("empty = %v, %v", res, err)
	}
}
