package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
)

// Every outbox kind can be sent, and nothing else is registered.
func TestEveryKindHasAHandler(t *testing.T) {
	kinds := outbox.AllKinds()
	if len(kinds) != 37 {
		t.Errorf("%d kinds, want 37", len(kinds))
	}
	for _, k := range kinds {
		h, ok := handlers[k]
		if !ok {
			t.Errorf("no handler for %s", k)
			continue
		}
		if h.send == nil {
			t.Errorf("%s cannot be sent", k)
		}
	}
	if len(handlers) != len(kinds) {
		t.Errorf("%d handlers for %d kinds", len(handlers), len(kinds))
	}
}

// What the handlers say agrees with the rules of domain/outbox and plan.
func TestHandlersAgreeWithSpecs(t *testing.T) {
	for k, h := range handlers {
		spec, _ := outbox.SpecOf(k)
		// An entry that creates something may have been applied without an answer: its
		// result must be looked for, or it would be created twice.
		if spec.Creates && h.find == nil {
			t.Errorf("%s creates something but has no find", k)
		}
		// A comment, attachment or time entry edited before it was created waits for it.
		if h.local != nil {
			if _, result, ok := h.local(outbox.Op{Kind: k}); !ok || result == "" || spec.Subject != outbox.SubjectTask {
				t.Errorf("%s: local %q, %v", k, result, ok)
			}
		}
		// Only entries of a feature recorded per workspace need its workspace.
		perWorkspace := func(f string) bool {
			return f == plan.Sections || f == plan.Tags || f == plan.MySection || f == plan.CustomFields
		}
		if f := plan.FeatureOf(k); h.planWorkspace != nil && !perWorkspace(f) {
			t.Errorf("%s has a plan workspace but its feature (%q) is not recorded so", k, f)
		}
		if f := plan.FeatureOf(k); h.planWorkspace == nil && perWorkspace(f) {
			t.Errorf("%s (%s) has no plan workspace", k, f)
		}
		// Batched entries change an existing task (batchEntryFor needs its gid).
		if h.batch != nil && spec.Subject != outbox.SubjectTask {
			t.Errorf("%s is batched but does not change a task", k)
		}
	}
	if h := handlers[outbox.KindDuplicateTask]; h.settle <= settleDefault {
		t.Errorf("a duplicate settles in %v", h.settle)
	}
	if got := settleTime(outbox.KindCreateTask); got != settleDefault {
		t.Errorf("settleTime(create_task) = %v", got)
	}
	if got := settleTime("unknown"); got != settleDefault {
		t.Errorf("settleTime(unknown) = %v", got)
	}
}

func TestRegisterRefusesMistakes(t *testing.T) {
	noop := func(*Pusher, context.Context, outbox.Op, outbox.UpdateTask) (outcome, error) { return sent, nil }
	other := func(*Pusher, context.Context, outbox.Op, outbox.Follower) (outcome, error) { return sent, nil }
	saved := handlers[outbox.KindAddTag]
	for _, tc := range []struct {
		name     string
		register func()
	}{
		{"twice", func() { registerOf(outbox.KindUpdateTask, handlerOf[outbox.UpdateTask]{send: noop}) }},
		{"unknown kind", func() { registerOf("made_up", handlerOf[outbox.UpdateTask]{send: noop}) }},
		{"empty kind", func() { registerOf("", handlerOf[outbox.UpdateTask]{send: noop}) }},
		{"without send", func() { registerOf(outbox.KindUpdateTask, handlerOf[outbox.UpdateTask]{}) }},
		{"another kind's payload", func() { registerOf(outbox.KindAddTag, handlerOf[outbox.Follower]{send: other}) }},
		{"another kind's payload, kind not registered", func() {
			delete(handlers, outbox.KindAddTag)
			defer func() { handlers[outbox.KindAddTag] = saved }()
			registerOf(outbox.KindAddTag, handlerOf[outbox.Follower]{send: other})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(handlers)
			defer func() {
				if recover() == nil {
					t.Error("registered")
				}
				if len(handlers) != before {
					t.Errorf("handlers changed: %d → %d", before, len(handlers))
				}
			}()
			tc.register()
		})
	}
}

// An entry of a kind this version does not know (written by a newer one) is not sent
// (held, see unreadable.go), without a request, and has nothing to look for.
func TestUnknownKindIsNotSent(t *testing.T) {
	s := &Syncer{}
	op := outbox.Op{Kind: "from_the_future", TaskGID: "t1"}
	out, err := s.Pusher().sendOp(context.Background(), op)
	var u *unreadableError
	if out != sent || !errors.As(err, &u) || !errors.Is(err, outbox.ErrUnknownKind) {
		t.Fatalf("sendOp = %v, %v", out, err)
	}
	out, m, err := s.Pusher().find(context.Background(), op, s.clock())
	if out != sent || m != notFound || err != nil {
		t.Fatalf("find = %v, %v, %v", out, m, err)
	}
}
