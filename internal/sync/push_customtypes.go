package sync

import (
	"context"
	"errors"
	"net/http"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
	domaintask "github.com/amasok/asagao/internal/domain/task"
)

// Setting a custom task type (#59). Whether Asana's PUT /tasks takes custom_type and
// custom_type_status_option is not settled (the API announcement says it does; reports
// say it does not): a change Asana refuses (4xx) or answers without applying stops as
// failed, with the reason, and is not sent again. The user can discard it.

// taskUpdateFields is the body of PUT /tasks/{gid} for an update_task entry: apiFields,
// and with a custom task type or its status, resource_subtype "custom" and the type too
// (Asana takes them only on a custom task; the type is the base's when only the status
// changes).
func taskUpdateFields(op outbox.Op) map[string]any {
	out := apiFields(op.Payload)
	_, typ := out["custom_type"]
	_, status := out["custom_type_status_option"]
	if !typ && !status {
		return out
	}
	if _, ok := out["resource_subtype"]; !ok {
		out["resource_subtype"] = domaintask.SubtypeCustom
	}
	if base, _ := op.Base["custom_type"].(string); !typ && base != "" {
		out["custom_type"] = base
	}
	return out
}

// errCustomTypeUnsupported says that Asana did not take a custom type over the API.
const errCustomTypeUnsupported = "Asana がカスタムタイプの設定を受け付けませんでした（API が対応していない可能性があります。Asana の画面で設定してください）"

// customTypeWithdrawn says that Asana did not take an entry's custom type: refused it
// (4xx; a 402 is the plan's) or answered without applying it (applied: the rest of the
// entry was applied). The custom type is taken out of the entry and the rest, if any, sent
// again (handleOutcome, withdrawCustomType), so that it does not hold the rest up.
type customTypeWithdrawn struct {
	reason  string
	applied bool
	err     error // Asana's answer (nil when it answered 200)
}

func (e *customTypeWithdrawn) Error() string {
	if e.err == nil {
		return e.reason
	}
	return e.reason + ": " + e.err.Error()
}
func (e *customTypeWithdrawn) Unwrap() error { return e.err }

// customTypeRefused turns Asana's refusal (400 / 403 / 422, or 402 for the plan, which
// is recorded for the workspace) of a change with a custom type into customTypeWithdrawn.
func (s *Syncer) customTypeRefused(ctx context.Context, op outbox.Op, err error) error {
	var apiErr *model.Error
	if op.Kind != outbox.KindUpdateTask || !domaintask.TouchesCustomType(op.Payload) || !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Status {
	case http.StatusPaymentRequired:
		if ws, wsErr := s.Store.TaskWorkspace(ctx, op.TaskGID); wsErr == nil {
			_, _ = s.recordPlanRefusal(ctx, plan.CustomTypes, ws, err) // per workspace, for a while (plan.go)
		}
		return &customTypeWithdrawn{reason: plan.UnavailableMessage(plan.CustomTypes), err: err}
	case http.StatusBadRequest, http.StatusForbidden, http.StatusUnprocessableEntity:
		return &customTypeWithdrawn{reason: errCustomTypeUnsupported, err: err}
	}
	return err
}

// customTypeApplied checks that Asana's answer to a change with a custom type has it: an
// answer without it (the field ignored) is customTypeWithdrawn, applied, instead of a
// change completed silently without its type. A task read without its custom type is not
// checked, nor one whose type or status came without its gid.
func customTypeApplied(op outbox.Op, t *model.Task) error {
	if !domaintask.TouchesCustomType(op.Payload) || t == nil || t.Partial || t.NoCustomType {
		return nil
	}
	// A type or status without its gid (read by name only) cannot be compared: the change
	// is not taken for refused because of it.
	for _, r := range []*model.Ref{t.CustomType, t.CustomTypeStatusOption} {
		if r != nil && r.GID == "" {
			return nil
		}
	}
	sent := taskUpdateFields(op)
	got := map[string]string{"resource_subtype": t.ResourceSubtype}
	if t.CustomType != nil {
		got["custom_type"] = t.CustomType.GID
	}
	if t.CustomTypeStatusOption != nil {
		got["custom_type_status_option"] = t.CustomTypeStatusOption.GID
	}
	for _, k := range []string{"resource_subtype", "custom_type", "custom_type_status_option"} {
		if v, ok := sent[k].(string); ok && got[k] != v {
			return &customTypeWithdrawn{reason: errCustomTypeUnsupported, applied: true}
		}
	}
	return nil
}

// withdrawCustomType handles a custom type Asana did not take without making the user
// decide: it is dropped (and reported in res.Discarded, with the reason), and the rest of
// the entry, if Asana did not apply it, is sent again right away. Asana applied nothing
// of a refused request, so sending the rest again repeats nothing.
func (s *Syncer) withdrawCustomType(ctx context.Context, op outbox.Op, w *customTypeWithdrawn, res *PushResult) (again bool, err error) {
	rest, d, err := s.Store.WithdrawCustomType(ctx, op.ID, w.reason, w.applied, s.clock())
	if err != nil {
		return false, err
	}
	res.Discarded = append(res.Discarded, d)
	if w.applied {
		res.Sent++
	}
	return rest != nil, nil
}
