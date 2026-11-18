package asana

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

// A custom ID lookup retries a 429 only: Asana answers 500 for some unknown IDs (#36).
func TestTaskByCustomIDRetries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		statuses  []int // answers in order; the last repeats
		wantCalls int32
		wantErr   bool
	}{
		{"500 is not retried", []int{500}, 1, true},
		{"502 is not retried", []int{502}, 1, true},
		{"503 is not retried", []int{503}, 1, true},
		{"404 once", []int{404}, 1, true},
		{"402: once more without the optional fields", []int{402}, 2, true},
		{"403 once", []int{403}, 1, true},
		{"429 then ok", []int{429, 429, 200}, 3, false},
		{"429 forever stops at MaxRetries", []int{429}, 3, true}, // MaxRetries = 2
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				st := tc.statuses[min(n-1, len(tc.statuses)-1)]
				if st != 200 {
					w.WriteHeader(st)
					fmt.Fprint(w, `{"errors":[{"message":"no"}]}`)
					return
				}
				fmt.Fprint(w, `{"data":{"gid":"t3","name":"x"}}`)
			})
			c.MaxRetries = 2
			task, err := c.TaskByCustomID(context.Background(), "w1", "ENG-1")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err %v task %+v", err, task)
			}
			if calls.Load() != tc.wantCalls {
				t.Fatalf("%d requests, want %d", calls.Load(), tc.wantCalls)
			}
			if !tc.wantErr && task.GID != "t3" {
				t.Fatalf("task %+v", task)
			}
		})
	}
}

// A 402 is told apart from the others, and a cancelled context stops at once.
func TestTaskByCustomIDErrorsAndCancel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		fmt.Fprint(w, `{"errors":[{"message":"upgrade"}]}`)
	})
	if _, err := c.TaskByCustomID(context.Background(), "w1", "ENG-1"); err == nil || !IsPaymentRequired(err) {
		t.Fatalf("402 not told apart: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.TaskByCustomID(ctx, "w1", "ENG-1"); err == nil {
		t.Fatal("cancelled context succeeded")
	}
}
