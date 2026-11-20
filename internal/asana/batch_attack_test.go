package asana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
)

func TestBatchAttackPathEscapeAndInjection(t *testing.T) {
	trickyTask := "../admin/t?x=1&y=2#frag %20タスク\x00\r\n\x1b[31m"
	trickySection := "s/1?a=b#c %セクション\x00\x1b[0m"
	trickyOther := "x/1?k=v#h %その他\n"

	writes := []struct {
		name         string
		write        BatchWrite
		wantMethod   string
		wantPath     string
		wantSegments int
		checkBody    func(t *testing.T, data map[string]any)
	}{
		{
			name:         "update task",
			write:        model.BatchUpdateTask(trickyTask, map[string]any{"name": "ok"}),
			wantMethod:   "put",
			wantPath:     "/tasks/" + url.PathEscape(trickyTask),
			wantSegments: 2,
		},
		{
			name:         "move task",
			write:        model.BatchMoveTaskToSection(trickySection, trickyTask),
			wantMethod:   "post",
			wantPath:     "/sections/" + url.PathEscape(trickySection) + "/addTask",
			wantSegments: 3,
			checkBody: func(t *testing.T, data map[string]any) {
				t.Helper()
				if data["task"] != trickyTask {
					t.Fatalf("body task = %q, want %q", data["task"], trickyTask)
				}
			},
		},
		{
			name:         "add tag",
			write:        model.BatchAddTag(trickyTask, trickyOther),
			wantMethod:   "post",
			wantPath:     "/tasks/" + url.PathEscape(trickyTask) + "/addTag",
			wantSegments: 3,
			checkBody: func(t *testing.T, data map[string]any) {
				t.Helper()
				if data["tag"] != trickyOther {
					t.Fatalf("body tag = %q, want %q", data["tag"], trickyOther)
				}
			},
		},
		{
			name:         "remove tag",
			write:        model.BatchRemoveTag(trickyTask, trickyOther),
			wantMethod:   "post",
			wantPath:     "/tasks/" + url.PathEscape(trickyTask) + "/removeTag",
			wantSegments: 3,
		},
		{
			name:         "add follower",
			write:        model.BatchAddFollower(trickyTask, trickyOther),
			wantMethod:   "post",
			wantPath:     "/tasks/" + url.PathEscape(trickyTask) + "/addFollowers",
			wantSegments: 3,
		},
		{
			name:         "remove follower",
			write:        model.BatchRemoveFollower(trickyTask, trickyOther),
			wantMethod:   "post",
			wantPath:     "/tasks/" + url.PathEscape(trickyTask) + "/removeFollowers",
			wantSegments: 3,
		},
		{
			name:         "add project",
			write:        model.BatchAddProject(trickyTask, trickyOther, trickySection),
			wantMethod:   "post",
			wantPath:     "/tasks/" + url.PathEscape(trickyTask) + "/addProject",
			wantSegments: 3,
			checkBody: func(t *testing.T, data map[string]any) {
				t.Helper()
				if data["project"] != trickyOther || data["section"] != trickySection {
					t.Fatalf("body project/section = %v", data)
				}
			},
		},
		{
			name:         "remove project",
			write:        model.BatchRemoveProject(trickyTask, trickyOther),
			wantMethod:   "post",
			wantPath:     "/tasks/" + url.PathEscape(trickyTask) + "/removeProject",
			wantSegments: 3,
		},
		{
			name:         "add dependency",
			write:        model.BatchAddDependency(trickyTask, trickyOther),
			wantMethod:   "post",
			wantPath:     "/tasks/" + url.PathEscape(trickyTask) + "/addDependencies",
			wantSegments: 3,
		},
		{
			name:         "remove dependency",
			write:        model.BatchRemoveDependency(trickyTask, trickyOther),
			wantMethod:   "post",
			wantPath:     "/tasks/" + url.PathEscape(trickyTask) + "/removeDependencies",
			wantSegments: 3,
		},
	}

	// 1) Direct batchActionFor verification.
	batchWrites := make([]BatchWrite, 0, len(writes))
	for _, tc := range writes {
		batchWrites = append(batchWrites, tc.write)
		t.Run("action/"+tc.name, func(t *testing.T) {
			act, _, err := batchActionFor(tc.write, []string{"name"})
			if err != nil {
				t.Fatalf("batchActionFor error: %v", err)
			}
			if act.Method != tc.wantMethod {
				t.Fatalf("method = %q, want %q", act.Method, tc.wantMethod)
			}
			if act.RelativePath != tc.wantPath {
				t.Fatalf("relative_path = %q, want %q", act.RelativePath, tc.wantPath)
			}
			u, err := url.Parse(act.RelativePath)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", act.RelativePath, err)
			}
			if u.RawQuery != "" || u.Fragment != "" {
				t.Fatalf("query/fragment leaked into relative_path: query=%q frag=%q", u.RawQuery, u.Fragment)
			}
			segs := strings.Split(strings.TrimPrefix(act.RelativePath, "/"), "/")
			if len(segs) != tc.wantSegments {
				t.Fatalf("segments = %q (len %d), want len %d", segs, len(segs), tc.wantSegments)
			}
			unescaped, err := url.PathUnescape(segs[1])
			if err != nil {
				t.Fatalf("PathUnescape(%q): %v", segs[1], err)
			}
			wantRaw := trickyTask
			if tc.write.Kind == model.BatchKindMoveTask {
				wantRaw = trickySection
			}
			if unescaped != wantRaw {
				t.Fatalf("unescaped segment = %q, want %q", unescaped, wantRaw)
			}
			if tc.checkBody != nil {
				tc.checkBody(t, act.Data)
			}
		})
	}

	// 2) End-to-end Client.Batch verification over in-memory RoundTripper (10 actions == MaxBatchActions).
	var sentActions []struct {
		Method       string         `json:"method"`
		RelativePath string         `json:"relative_path"`
		Data         map[string]any `json:"data"`
	}
	c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Data struct {
				Actions []struct {
					Method       string         `json:"method"`
					RelativePath string         `json:"relative_path"`
					Data         map[string]any `json:"data"`
				} `json:"actions"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		sentActions = body.Data.Actions

		respItems := make([]string, len(sentActions))
		for i := range sentActions {
			if i == 0 {
				respItems[i] = `{"status_code":200,"body":{"data":{"gid":"t0","name":"ok"}}}`
			} else {
				respItems[i] = `{"status_code":200,"body":{"data":{}}}`
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":[` + strings.Join(respItems, ",") + `]}`)),
			Header:     make(http.Header),
		}, nil
	})

	res, err := c.Batch(context.Background(), batchWrites)
	if err != nil {
		t.Fatalf("Client.Batch error: %v", err)
	}
	if len(res) != len(writes) || len(sentActions) != len(writes) {
		t.Fatalf("len(res)=%d, len(sentActions)=%d, want %d", len(res), len(sentActions), len(writes))
	}
	for i, tc := range writes {
		if sentActions[i].RelativePath != tc.wantPath {
			t.Fatalf("action[%d] (%s) relative_path = %q, want %q", i, tc.name, sentActions[i].RelativePath, tc.wantPath)
		}
		if sentActions[i].Method != tc.wantMethod {
			t.Fatalf("action[%d] (%s) method = %q, want %q", i, tc.name, sentActions[i].Method, tc.wantMethod)
		}
		if tc.checkBody != nil {
			tc.checkBody(t, sentActions[i].Data)
		}
	}
}

func TestBatchAttackValidationZeroHTTPCalls(t *testing.T) {
	invalidWrites := []struct {
		name  string
		write BatchWrite
	}{
		{"zero value", BatchWrite{}},
		{"unknown kind", BatchWrite{Kind: "unknown", TaskGID: "t1"}},
		{"unknown kind control chars", BatchWrite{Kind: "\x1b[31mbad", TaskGID: "t1"}},
		{"update task missing task gid", model.BatchUpdateTask("", map[string]any{"completed": true})},
		{"move task missing section gid", model.BatchMoveTaskToSection("", "t1")},
		{"move task missing task gid", model.BatchMoveTaskToSection("s1", "")},
		{"move task missing both gids", model.BatchMoveTaskToSection("", "")},
		{"add tag missing task gid", model.BatchAddTag("", "tag1")},
		{"add tag missing tag gid", model.BatchAddTag("t1", "")},
		{"remove tag missing task gid", model.BatchRemoveTag("", "tag1")},
		{"remove tag missing tag gid", model.BatchRemoveTag("t1", "")},
		{"add follower missing task gid", model.BatchAddFollower("", "u1")},
		{"add follower missing user gid", model.BatchAddFollower("t1", "")},
		{"remove follower missing task gid", model.BatchRemoveFollower("", "u1")},
		{"remove follower missing user gid", model.BatchRemoveFollower("t1", "")},
		{"add project missing task gid", model.BatchAddProject("", "p1", "")},
		{"add project missing project gid without section", model.BatchAddProject("t1", "", "")},
		{"add project missing project gid with section", model.BatchAddProject("t1", "", "s1")},
		{"remove project missing task gid", model.BatchRemoveProject("", "p1")},
		{"remove project missing project gid", model.BatchRemoveProject("t1", "")},
		{"add dependency missing task gid", model.BatchAddDependency("", "d1")},
		{"add dependency missing dep gid", model.BatchAddDependency("t1", "")},
		{"remove dependency missing task gid", model.BatchRemoveDependency("", "d1")},
		{"remove dependency missing dep gid", model.BatchRemoveDependency("t1", "")},
	}

	assertZeroHTTPCalls := func(t *testing.T, writes []BatchWrite) {
		t.Helper()
		var transportCalls atomic.Int64
		c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
			transportCalls.Add(1)
			return nil, fmt.Errorf("HTTP transport must not be called")
		})
		res, err := c.Batch(context.Background(), writes)
		if err == nil {
			t.Fatalf("expected error, got nil (res=%+v)", res)
		}
		if transportCalls.Load() != 0 || c.Requests() != 0 {
			t.Fatalf("HTTP called: transportCalls=%d clientRequests=%d", transportCalls.Load(), c.Requests())
		}
	}

	for _, tc := range invalidWrites {
		t.Run("alone/"+tc.name, func(t *testing.T) {
			assertZeroHTTPCalls(t, []BatchWrite{tc.write})
		})
		t.Run("second/"+tc.name, func(t *testing.T) {
			assertZeroHTTPCalls(t, []BatchWrite{
				model.BatchUpdateTask("t0", map[string]any{"completed": true}),
				tc.write,
			})
		})
		t.Run("last_of_10/"+tc.name, func(t *testing.T) {
			writes := make([]BatchWrite, MaxBatchActions)
			for i := 0; i < MaxBatchActions-1; i++ {
				writes[i] = model.BatchAddTag(fmt.Sprintf("t%d", i), "g1")
			}
			writes[MaxBatchActions-1] = tc.write
			assertZeroHTTPCalls(t, writes)
		})
	}

	t.Run("more than MaxBatchActions", func(t *testing.T) {
		writes := make([]BatchWrite, MaxBatchActions+1)
		for i := range writes {
			writes[i] = model.BatchAddTag(fmt.Sprintf("t%d", i), "g1")
		}
		assertZeroHTTPCalls(t, writes)
	})

	t.Run("nil and empty slice", func(t *testing.T) {
		var transportCalls atomic.Int64
		c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
			transportCalls.Add(1)
			return nil, fmt.Errorf("HTTP transport must not be called")
		})
		for _, empty := range [][]BatchWrite{nil, {}} {
			res, err := c.Batch(context.Background(), empty)
			if err != nil || res != nil {
				t.Fatalf("empty batch got (%+v, %v), want (nil, nil)", res, err)
			}
		}
		if transportCalls.Load() != 0 || c.Requests() != 0 {
			t.Fatalf("HTTP called on empty batch: %d / %d", transportCalls.Load(), c.Requests())
		}
	})
}

func TestBatchAttackWantTaskDecodeAndIndexAlignment(t *testing.T) {
	t.Run("non-update actions ignore odd 2xx bodies", func(t *testing.T) {
		nonUpdates := []BatchWrite{
			model.BatchMoveTaskToSection("s1", "t1"),
			model.BatchAddTag("t2", "tag1"),
			model.BatchRemoveTag("t3", "tag1"),
			model.BatchAddFollower("t4", "u1"),
			model.BatchRemoveFollower("t5", "u1"),
			model.BatchAddProject("t6", "p1", ""),
			model.BatchRemoveProject("t7", "p1"),
			model.BatchAddDependency("t8", "d1"),
			model.BatchRemoveDependency("t9", "d1"),
		}
		oddBodies := []string{
			`{"data":{}}`,
			`null`,
			`{}`,
			`"plain-string"`,
			`[1,2,3]`,
			`{"data":{"gid":"not-a-task","completed":"not-a-bool"}}`,
			`{"data":"scalar"}`,
			`12345`,
			`true`,
		}
		c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
			items := make([]string, len(oddBodies))
			for i, b := range oddBodies {
				items[i] = fmt.Sprintf(`{"status_code":200,"body":%s}`, b)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"data":[` + strings.Join(items, ",") + `]}`)),
				Header:     make(http.Header),
			}, nil
		})
		res, err := c.Batch(context.Background(), nonUpdates)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res) != len(nonUpdates) {
			t.Fatalf("len(res) = %d, want %d", len(res), len(nonUpdates))
		}
		for i, r := range res {
			if r.Err != nil || r.Task != nil {
				t.Fatalf("res[%d] = %+v, want {Task:nil, Err:nil}", i, r)
			}
		}
	})

	t.Run("update task rejects broken task JSON", func(t *testing.T) {
		badBodies := []string{
			`{"data":{"completed":"not-a-bool"}}`,
			`{"data":"not-an-object"}`,
			`"plain-string"`,
			`[]`,
		}
		for _, body := range badBodies {
			c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
				resp := fmt.Sprintf(`{"data":[{"status_code":200,"body":{"data":{}}},{"status_code":200,"body":%s}]}`, body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(resp)),
					Header:     make(http.Header),
				}, nil
			})
			_, err := c.Batch(context.Background(), []BatchWrite{
				model.BatchAddTag("t1", "tag1"),
				model.BatchUpdateTask("t2", map[string]any{"completed": true}),
			})
			if err == nil {
				t.Fatalf("expected decode error for body %s, got nil", body)
			}
		}
	})

	t.Run("MaxBatchActions mixed batch index alignment", func(t *testing.T) {
		writes := []BatchWrite{
			model.BatchUpdateTask("t0", map[string]any{"name": "zero"}),
			model.BatchMoveTaskToSection("s1", "t1"),
			model.BatchAddTag("t2", "tag2"),
			model.BatchUpdateTask("t3", map[string]any{"completed": true}),
			model.BatchAddFollower("t4", "u4"),
			model.BatchRemoveFollower("t5", "u5"),
			model.BatchUpdateTask("t6", map[string]any{"name": "six"}),
			model.BatchAddProject("t7", "p7", "s7"),
			model.BatchRemoveDependency("t8", "d8"),
			model.BatchUpdateTask("t9", map[string]any{"name": "nine"}),
		}
		if len(writes) != MaxBatchActions {
			t.Fatalf("len(writes) = %d, want %d", len(writes), MaxBatchActions)
		}

		c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
			resp := `{"data":[
				{"status_code":200,"body":{"data":{"gid":"t0","name":"zero"}}},
				{"status_code":200,"body":{"data":{}}},
				{"status_code":200,"body":{"data":{"completed":"not-a-bool"}}},
				{"status_code":200,"body":{"data":{"gid":"t3","name":"three","completed":true}}},
				{"status_code":201,"body":null},
				{"status_code":302,"body":{}},
				{"status_code":404,"body":{"errors":[{"message":"task missing"}]}},
				{"status_code":400,"body":{"errors":[{"message":"bad\u001b[31m\u0007 input"}]}},
				{"status_code":500,"body":null},
				{"status_code":200,"body":{"data":{"gid":"t9","name":"nine"}}}
			]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(resp)),
				Header:     make(http.Header),
			}, nil
		})

		res, err := c.Batch(context.Background(), writes)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res) != MaxBatchActions {
			t.Fatalf("len(res) = %d, want %d", len(res), MaxBatchActions)
		}
		if res[0].Err != nil || res[0].Task == nil || res[0].Task.GID != "t0" || res[0].Task.Name != "zero" {
			t.Fatalf("res[0] = %+v", res[0])
		}
		if res[1].Err != nil || res[1].Task != nil {
			t.Fatalf("res[1] = %+v", res[1])
		}
		if res[2].Err != nil || res[2].Task != nil {
			t.Fatalf("res[2] = %+v", res[2])
		}
		if res[3].Err != nil || res[3].Task == nil || res[3].Task.GID != "t3" || !res[3].Task.Completed {
			t.Fatalf("res[3] = %+v", res[3])
		}
		if res[4].Err != nil || res[4].Task != nil {
			t.Fatalf("res[4] = %+v", res[4])
		}
		if e, ok := res[5].Err.(*Error); !ok || e.Status != http.StatusBadGateway || res[5].Task != nil {
			t.Fatalf("res[5] = %+v", res[5])
		}
		if e, ok := res[6].Err.(*Error); !ok || e.Status != http.StatusNotFound || res[6].Task != nil {
			t.Fatalf("res[6] = %+v", res[6])
		}
		if e, ok := res[7].Err.(*Error); !ok || e.Status != http.StatusBadRequest || res[7].Task != nil {
			t.Fatalf("res[7] = %+v", res[7])
		} else if strings.ContainsAny(e.Error(), "\x1b\x07") {
			t.Fatalf("error message leaked control chars: %q", e.Error())
		}
		if e, ok := res[8].Err.(*Error); !ok || e.Status != http.StatusInternalServerError || res[8].Task != nil {
			t.Fatalf("res[8] = %+v", res[8])
		}
		if res[9].Err != nil || res[9].Task == nil || res[9].Task.GID != "t9" || res[9].Task.Name != "nine" {
			t.Fatalf("res[9] = %+v", res[9])
		}
	})

	t.Run("result count mismatch fails batch", func(t *testing.T) {
		writes := []BatchWrite{model.BatchAddTag("t1", "g1"), model.BatchAddTag("t2", "g2")}
		for _, resp := range []string{`{"data":[]}`, `{"data":[{"status_code":200,"body":{}}]}`, `{"data":[{"status_code":200,"body":{}},{"status_code":200,"body":{}},{"status_code":200,"body":{}}]}`} {
			c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(resp)),
					Header:     make(http.Header),
				}, nil
			})
			if _, err := c.Batch(context.Background(), writes); err == nil {
				t.Fatalf("expected error for mismatched result count in %s", resp)
			}
		}
	})

	t.Run("field tier marks updated task", func(t *testing.T) {
		c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
			var body struct {
				Data struct {
					Actions []struct {
						Options struct {
							Fields []string `json:"fields"`
						} `json:"options"`
					} `json:"actions"`
				} `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			for _, f := range body.Data.Actions[0].Options.Fields {
				if isCustomTypeField(f) {
					t.Fatalf("unexpected custom type field %q when noCustomTypes is set", f)
				}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"data":[{"status_code":200,"body":{"data":{"gid":"t1"}}}]}`)),
				Header:     make(http.Header),
			}, nil
		})
		c.noCustomTypes.Store(true)
		res, err := c.Batch(context.Background(), []BatchWrite{model.BatchUpdateTask("t1", map[string]any{"name": "x"})})
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Task == nil || !res[0].Task.NoCustomType || res[0].Task.Partial {
			t.Fatalf("tier flags = %+v", res[0].Task)
		}
	})

	t.Run("concurrent Batch calls pass race detector", func(t *testing.T) {
		c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{"data":[
					{"status_code":200,"body":{"data":{"gid":"t1","completed":true}}},
					{"status_code":200,"body":{"data":{}}}
				]}`)),
				Header: http.Header{"Date": []string{http.TimeFormat}},
			}, nil
		})
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := c.Batch(context.Background(), []BatchWrite{
					model.BatchUpdateTask("t1", map[string]any{"completed": true}),
					model.BatchAddTag("t2", "g1"),
				})
				if err != nil {
					errs <- err
					return
				}
				if len(res) != 2 || res[0].Task == nil || res[0].Task.GID != "t1" || res[1].Task != nil {
					errs <- fmt.Errorf("unexpected results: %+v", res)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		if c.Requests() != 8 {
			t.Fatalf("Requests = %d, want 8", c.Requests())
		}
	})
}

func TestBatchAttackAddProjectOptionalSection(t *testing.T) {
	var rawActions []struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Data struct {
				Actions []struct {
					Data map[string]json.RawMessage `json:"data"`
				} `json:"actions"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		rawActions = body.Data.Actions
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"status_code":200,"body":{"data":{}}},{"status_code":200,"body":{"data":{}}}]}`)),
			Header:     make(http.Header),
		}, nil
	})

	_, err := c.Batch(context.Background(), []BatchWrite{
		model.BatchAddProject("t1", "p1", ""),
		model.BatchAddProject("t2", "p2", "s2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rawActions) != 2 {
		t.Fatalf("len(rawActions) = %d, want 2", len(rawActions))
	}
	if _, hasSection := rawActions[0].Data["section"]; hasSection {
		t.Fatalf("section key must be omitted when SectionGID is empty: %v", rawActions[0].Data)
	}
	if !reflect.DeepEqual(rawActions[0].Data, map[string]json.RawMessage{"project": json.RawMessage(`"p1"`)}) {
		t.Fatalf("without section data = %v", rawActions[0].Data)
	}
	if !reflect.DeepEqual(rawActions[1].Data, map[string]json.RawMessage{
		"project": json.RawMessage(`"p2"`),
		"section": json.RawMessage(`"s2"`),
	}) {
		t.Fatalf("with section data = %v", rawActions[1].Data)
	}
}

func TestBatchAttackRetryOn5xx(t *testing.T) {
	var attempts atomic.Int64
	c := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader(`{"errors":[{"message":"busy"}]}`)),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"status_code":200,"body":{"data":{"gid":"t1"}}}]}`)),
			Header:     make(http.Header),
		}, nil
	})
	c.MaxRetries = 1
	res, err := c.Batch(context.Background(), []BatchWrite{model.BatchUpdateTask("t1", map[string]any{"completed": true})})
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if attempts.Load() != 2 || len(res) != 1 || res[0].Task == nil || res[0].Task.GID != "t1" {
		t.Fatalf("attempts=%d res=%+v", attempts.Load(), res)
	}

	var err400 *Error
	c2 := newInMemoryClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"errors":[{"message":"bad batch"}]}`)),
			Header:     make(http.Header),
		}, nil
	})
	c2.MaxRetries = 2
	if _, err := c2.Batch(context.Background(), []BatchWrite{model.BatchAddTag("t1", "g1")}); !errors.As(err, &err400) || err400.Status != 400 || c2.Requests() != 1 {
		t.Fatalf("400 should not retry: err=%v requests=%d", err, c2.Requests())
	}
}
