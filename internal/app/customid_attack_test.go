package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

type stateMap map[string]string

func (s stateMap) CustomIDTaskGID(_ context.Context, id string) (string, error) { return s[id], nil }

type stateErr struct{}

func (stateErr) CustomIDTaskGID(context.Context, string) (string, error) {
	return "", errors.New("db")
}

type scriptedFinder struct {
	answers map[string]error // nil error: found as "g-"+id
	asked   []string
}

func (f *scriptedFinder) TaskByCustomID(_ context.Context, id string) (string, error) {
	f.asked = append(f.asked, id)
	if err := f.answers[id]; err != nil {
		return "", err
	}
	return "g-" + id, nil
}

// Mixed answers for several custom IDs (#36): an answer from Asana (500, 404, 402 as a
// model error) is about its ID only; not reaching Asana stops the asking.
func TestResolveCustomIDsMixed(t *testing.T) {
	notFound := fmt.Errorf("x: %w", readmodel.ErrNotFound)
	e500 := &model.Error{Status: 500}
	e402 := &model.Error{Status: 402}
	remembered := stateMap{"A": "old-A", "B": "old-B", "C": "old-C", "D": "old-D"}
	ctx := context.Background()

	t.Run("500, 404, 402 then ok: all asked", func(t *testing.T) {
		f := &scriptedFinder{answers: map[string]error{"A": e500, "B": notFound, "C": e402}}
		got, err := ResolveCustomIDs(ctx, remembered, f, []string{"A", "B", "C", "D"})
		if len(f.asked) != 4 {
			t.Fatalf("asked %v", f.asked)
		}
		want := map[string]string{"A": "old-A", "C": "old-C", "D": "g-D"} // B: Asana says there is none: no stale gid
		if len(got) != len(want) {
			t.Fatalf("got %v err %v", got, err)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
		if err == nil {
			t.Error("no warning for the failures")
		}
	})
	t.Run("unreachable stops the asking, the rest is remembered", func(t *testing.T) {
		f := &scriptedFinder{answers: map[string]error{"B": context.DeadlineExceeded}}
		got, err := ResolveCustomIDs(ctx, remembered, f, []string{"A", "B", "C", "D"})
		if fmt.Sprint(f.asked) != "[A B]" {
			t.Fatalf("asked %v", f.asked)
		}
		if got["A"] != "g-A" || got["B"] != "old-B" || got["C"] != "old-C" || got["D"] != "old-D" || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v err %v", got, err)
		}
	})
	t.Run("a plain error (not from Asana) also stops the asking", func(t *testing.T) {
		f := &scriptedFinder{answers: map[string]error{"A": errors.New("plan lacks custom IDs")}}
		_, _ = ResolveCustomIDs(ctx, remembered, f, []string{"A", "B"})
		if len(f.asked) != 1 {
			t.Fatalf("asked %v", f.asked)
		}
	})
	t.Run("nil finder uses the remembered ones and never panics", func(t *testing.T) {
		got, err := ResolveCustomIDs(ctx, remembered, nil, []string{"A", "Z"})
		if err != nil || got["A"] != "old-A" || len(got) != 1 {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("no ids", func(t *testing.T) {
		got, err := ResolveCustomIDs(ctx, remembered, &scriptedFinder{}, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("a cache failure is returned", func(t *testing.T) {
		if _, err := ResolveCustomIDs(ctx, stateErr{}, nil, []string{"A"}); err == nil {
			t.Fatal("swallowed")
		}
	})
	t.Run("a cancelled ctx still reads what was remembered", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		f := &scriptedFinder{answers: map[string]error{"A": context.Canceled}}
		got, _ := ResolveCustomIDs(cctx, remembered, f, []string{"A"})
		if got["A"] != "old-A" {
			t.Fatalf("got %v", got)
		}
	})
}

func TestCustomIDsOncePerID(t *testing.T) {
	ids := CustomIDs(nil)
	if len(ids) != 0 {
		t.Fatal(ids)
	}
}
