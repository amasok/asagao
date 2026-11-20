package asana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newInMemoryClient(fn roundTripFunc) *Client {
	c := NewClient(&http.Client{Transport: fn})
	c.MaxRetries = 0
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

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
		model.BatchUpdateTask("1", map[string]any{"completed": true}),
		model.BatchAddTag("2", "g"),
		model.BatchAddTag("3", "g"),
		model.BatchAddTag("4", "g"),
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
	one := []BatchWrite{model.BatchAddTag("1", "g")}

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

func TestBatchActionForAllKinds(t *testing.T) {
	fields := []string{"name", "completed"}
	cases := []struct {
		name     string
		write    model.BatchWrite
		wantAct  batchAction
		wantTask bool
	}{
		{
			name:  "update task",
			write: model.BatchUpdateTask("t1", map[string]any{"completed": true}),
			wantAct: batchAction{
				Method:       "put",
				RelativePath: "/tasks/t1",
				Data:         map[string]any{"completed": true},
				Options:      map[string]any{"fields": fields},
			},
			wantTask: true,
		},
		{
			name:  "move task",
			write: model.BatchMoveTaskToSection("s1", "t1"),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/sections/s1/addTask",
				Data:         map[string]any{"task": "t1"},
			},
		},
		{
			name:  "add tag",
			write: model.BatchAddTag("t1", "tag1"),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/tasks/t1/addTag",
				Data:         map[string]any{"tag": "tag1"},
			},
		},
		{
			name:  "remove tag",
			write: model.BatchRemoveTag("t1", "tag1"),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/tasks/t1/removeTag",
				Data:         map[string]any{"tag": "tag1"},
			},
		},
		{
			name:  "add follower",
			write: model.BatchAddFollower("t1", "u1"),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/tasks/t1/addFollowers",
				Data:         map[string]any{"followers": []string{"u1"}},
			},
		},
		{
			name:  "remove follower",
			write: model.BatchRemoveFollower("t1", "u1"),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/tasks/t1/removeFollowers",
				Data:         map[string]any{"followers": []string{"u1"}},
			},
		},
		{
			name:  "add project without section",
			write: model.BatchAddProject("t1", "p1", ""),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/tasks/t1/addProject",
				Data:         map[string]any{"project": "p1"},
			},
		},
		{
			name:  "add project with section",
			write: model.BatchAddProject("t1", "p1", "s1"),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/tasks/t1/addProject",
				Data:         map[string]any{"project": "p1", "section": "s1"},
			},
		},
		{
			name:  "remove project",
			write: model.BatchRemoveProject("t1", "p1"),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/tasks/t1/removeProject",
				Data:         map[string]any{"project": "p1"},
			},
		},
		{
			name:  "add dependency",
			write: model.BatchAddDependency("t1", "d1"),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/tasks/t1/addDependencies",
				Data:         map[string]any{"dependencies": []string{"d1"}},
			},
		},
		{
			name:  "remove dependency",
			write: model.BatchRemoveDependency("t1", "d1"),
			wantAct: batchAction{
				Method:       "post",
				RelativePath: "/tasks/t1/removeDependencies",
				Data:         map[string]any{"dependencies": []string{"d1"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			act, wantTask, err := batchActionFor(tc.write, fields)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if wantTask != tc.wantTask {
				t.Fatalf("wantTask = %v, want %v", wantTask, tc.wantTask)
			}
			if !reflect.DeepEqual(act, tc.wantAct) {
				t.Fatalf("act = %#v, want %#v", act, tc.wantAct)
			}
		})
	}
}

func TestBatchActionForPathEscape(t *testing.T) {
	act, _, err := batchActionFor(model.BatchUpdateTask("t/1?a=1#b", map[string]any{"name": "x"}), []string{"name"})
	if err != nil {
		t.Fatal(err)
	}
	if act.RelativePath != "/tasks/t%2F1%3Fa=1%23b" {
		t.Fatalf("escaped task path = %q", act.RelativePath)
	}

	act, _, err = batchActionFor(model.BatchMoveTaskToSection("s/1 2?#", "t/raw"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if act.RelativePath != "/sections/s%2F1%202%3F%23/addTask" {
		t.Fatalf("escaped section path = %q", act.RelativePath)
	}
	if act.Data["task"] != "t/raw" {
		t.Fatalf("body task gid should stay unescaped: %v", act.Data["task"])
	}

	act, _, err = batchActionFor(model.BatchAddTag("t/1?x", "g/1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if act.RelativePath != "/tasks/t%2F1%3Fx/addTag" {
		t.Fatalf("escaped addTag path = %q", act.RelativePath)
	}
	if act.Data["tag"] != "g/1" {
		t.Fatalf("body tag gid should stay unescaped: %v", act.Data["tag"])
	}
}

func TestBatchActionForInvalidInputs(t *testing.T) {
	invalid := []struct {
		name  string
		write model.BatchWrite
	}{
		{"zero value", model.BatchWrite{}},
		{"unknown kind", model.BatchWrite{Kind: "delete_everything", TaskGID: "t1"}},
		{"unknown kind with control chars", model.BatchWrite{Kind: "\x1b[31mbad", TaskGID: "t1"}},
		{"update task missing task gid", model.BatchUpdateTask("", map[string]any{"completed": true})},
		{"update task nil fields", model.BatchUpdateTask("t1", nil)},
		{"update task empty fields", model.BatchUpdateTask("t1", map[string]any{})},
		{"move task missing section gid", model.BatchMoveTaskToSection("", "t1")},
		{"move task missing task gid", model.BatchMoveTaskToSection("s1", "")},
		{"add tag missing task gid", model.BatchAddTag("", "tag1")},
		{"add tag missing tag gid", model.BatchAddTag("t1", "")},
		{"remove tag missing task gid", model.BatchRemoveTag("", "tag1")},
		{"remove tag missing tag gid", model.BatchRemoveTag("t1", "")},
		{"add follower missing task gid", model.BatchAddFollower("", "u1")},
		{"add follower missing user gid", model.BatchAddFollower("t1", "")},
		{"remove follower missing task gid", model.BatchRemoveFollower("", "u1")},
		{"remove follower missing user gid", model.BatchRemoveFollower("t1", "")},
		{"add project missing task gid", model.BatchAddProject("", "p1", "")},
		{"add project missing project gid", model.BatchAddProject("t1", "", "s1")},
		{"remove project missing task gid", model.BatchRemoveProject("", "p1")},
		{"remove project missing project gid", model.BatchRemoveProject("t1", "")},
		{"add dependency missing task gid", model.BatchAddDependency("", "d1")},
		{"add dependency missing dep gid", model.BatchAddDependency("t1", "")},
		{"remove dependency missing task gid", model.BatchRemoveDependency("", "d1")},
		{"remove dependency missing dep gid", model.BatchRemoveDependency("t1", "")},
	}

	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := batchActionFor(tc.write, []string{"name"})
			if err == nil {
				t.Fatalf("expected error for %+v", tc.write)
			}
			if strings.ContainsRune(err.Error(), '\x1b') {
				t.Fatalf("error leaked raw control character: %q", err.Error())
			}
		})
	}
}

func TestBatchValidationBeforeHTTP(t *testing.T) {
	invalidCases := []struct {
		name   string
		writes []BatchWrite
	}{
		{"unknown kind", []BatchWrite{{Kind: "bogus", TaskGID: "t1"}}},
		{"empty task gid", []BatchWrite{model.BatchUpdateTask("", map[string]any{"name": "x"})}},
		{"nil update fields", []BatchWrite{model.BatchUpdateTask("t1", nil)}},
		{"empty update fields", []BatchWrite{model.BatchUpdateTask("t1", map[string]any{})}},
		{"empty tag gid after valid write", []BatchWrite{
			model.BatchUpdateTask("t1", map[string]any{"name": "x"}),
			model.BatchAddTag("t1", ""),
		}},
		{"empty section gid on move", []BatchWrite{model.BatchMoveTaskToSection("", "t1")}},
		{"empty user gid on follower", []BatchWrite{model.BatchRemoveFollower("t1", "")}},
		{"empty project gid on project", []BatchWrite{model.BatchAddProject("t1", "", "s1")}},
		{"empty dep gid on dependency", []BatchWrite{model.BatchAddDependency("t1", "")}},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
				called = true
				return nil, fmt.Errorf("HTTP must not be called")
			})
			if _, err := c.Batch(context.Background(), tc.writes); err == nil {
				t.Fatal("expected error, got nil")
			}
			if called || c.Requests() != 0 {
				t.Fatalf("HTTP was called (%v, requests=%d) on invalid input", called, c.Requests())
			}
		})
	}
}

func TestBatchRoundTripMatchesSingleEndpoints(t *testing.T) {
	type captured struct {
		method string
		path   string
		data   map[string]any
	}
	recordSingle := func(fn func(*Client) error) captured {
		var got captured
		c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(r.Body)
			var body struct {
				Data map[string]any `json:"data"`
			}
			if len(b) > 0 {
				_ = json.Unmarshal(b, &body)
			}
			got = captured{
				method: strings.ToLower(r.Method),
				path:   strings.TrimPrefix(r.URL.EscapedPath(), "/api/1.0"),
				data:   body.Data,
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"data":{"gid":"t/1"}}`)),
				Header:     make(http.Header),
			}, nil
		})
		if err := fn(c); err != nil {
			t.Fatal(err)
		}
		return got
	}

	ctx := context.Background()
	pairs := []struct {
		name   string
		write  BatchWrite
		single func(*Client) error
	}{
		{
			"UpdateTask",
			model.BatchUpdateTask("t/1", map[string]any{"completed": true}),
			func(c *Client) error { _, err := c.UpdateTask(ctx, "t/1", map[string]any{"completed": true}); return err },
		},
		{
			"AddTaskToSection",
			model.BatchMoveTaskToSection("s/1", "t/1"),
			func(c *Client) error { return c.AddTaskToSection(ctx, "s/1", "t/1") },
		},
		{
			"AddTag",
			model.BatchAddTag("t/1", "tag1"),
			func(c *Client) error { return c.AddTag(ctx, "t/1", "tag1") },
		},
		{
			"RemoveTag",
			model.BatchRemoveTag("t/1", "tag1"),
			func(c *Client) error { return c.RemoveTag(ctx, "t/1", "tag1") },
		},
		{
			"AddFollowers",
			model.BatchAddFollower("t/1", "u1"),
			func(c *Client) error { return c.AddFollowers(ctx, "t/1", []string{"u1"}) },
		},
		{
			"RemoveFollowers",
			model.BatchRemoveFollower("t/1", "u1"),
			func(c *Client) error { return c.RemoveFollowers(ctx, "t/1", []string{"u1"}) },
		},
		{
			"AddProject without section",
			model.BatchAddProject("t/1", "p1", ""),
			func(c *Client) error { return c.AddProject(ctx, "t/1", "p1", "") },
		},
		{
			"AddProject with section",
			model.BatchAddProject("t/1", "p1", "s1"),
			func(c *Client) error { return c.AddProject(ctx, "t/1", "p1", "s1") },
		},
		{
			"RemoveProject",
			model.BatchRemoveProject("t/1", "p1"),
			func(c *Client) error { return c.RemoveProject(ctx, "t/1", "p1") },
		},
		{
			"AddDependencies",
			model.BatchAddDependency("t/1", "d1"),
			func(c *Client) error { return c.AddDependencies(ctx, "t/1", []string{"d1"}) },
		},
		{
			"RemoveDependencies",
			model.BatchRemoveDependency("t/1", "d1"),
			func(c *Client) error { return c.RemoveDependencies(ctx, "t/1", []string{"d1"}) },
		},
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			single := recordSingle(p.single)
			act, _, err := batchActionFor(p.write, TaskFields)
			if err != nil {
				t.Fatal(err)
			}
			// Round-trip act.Data through JSON so []string vs []any matches decoded single.data.
			raw, err := json.Marshal(act.Data)
			if err != nil {
				t.Fatal(err)
			}
			var batchData map[string]any
			if err := json.Unmarshal(raw, &batchData); err != nil {
				t.Fatal(err)
			}
			if act.Method != single.method || act.RelativePath != single.path || !reflect.DeepEqual(batchData, single.data) {
				t.Fatalf("batch (%s %s %v) != single (%s %s %v)", act.Method, act.RelativePath, batchData, single.method, single.path, single.data)
			}
		})
	}
}

func TestBatchClientRoundTripDecodesOnlyWantTask(t *testing.T) {
	var sentActions []map[string]any
	c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/1.0/batch" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Data struct {
				Actions []map[string]any `json:"actions"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		sentActions = body.Data.Actions
		respBody := `{"data":[
			{"status_code":200,"body":{"data":{"gid":"t1","name":"updated","completed":true}}},
			{"status_code":200,"body":{"data":{"gid":"should-not-decode"}}},
			{"status_code":302,"body":{}}
		]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(respBody)),
			Header:     make(http.Header),
		}, nil
	})

	res, err := c.Batch(context.Background(), []BatchWrite{
		model.BatchUpdateTask("t1", map[string]any{"completed": true}),
		model.BatchAddProject("t2", "p1", "s1"),
		model.BatchRemoveDependency("t3", "d1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sentActions) != 3 {
		t.Fatalf("actions len = %d", len(sentActions))
	}
	if sentActions[0]["options"] == nil || sentActions[1]["options"] != nil || sentActions[2]["options"] != nil {
		t.Fatalf("unexpected options: %v", sentActions)
	}
	if res[0].Err != nil || res[0].Task == nil || res[0].Task.GID != "t1" || !res[0].Task.Completed {
		t.Fatalf("res[0] = %+v", res[0])
	}
	if res[1].Err != nil || res[1].Task != nil {
		t.Fatalf("res[1] should not decode Task: %+v", res[1])
	}
	if e, ok := res[2].Err.(*Error); !ok || e.Status != http.StatusBadGateway {
		t.Fatalf("res[2] 302 should map to 502, got %+v", res[2].Err)
	}
}
