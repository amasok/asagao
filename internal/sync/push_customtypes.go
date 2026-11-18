package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	domaintask "github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
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
const errCustomTypeUnsupported = "Asana がカスタムタイプの設定を受け付けませんでした（API が対応していない可能性があります。Asana の画面で設定し、この変更は取り消してください）"

// customTypeRefused words a 400 / 403 / 422 to a change with a custom type (a 402 is the
// plan's: send records it). The status is kept, so the entry is marked failed.
func customTypeRefused(op outbox.Op, err error) error {
	var apiErr *model.Error
	if op.Kind != outbox.KindUpdateTask || !domaintask.TouchesCustomType(op.Payload) || !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusUnprocessableEntity:
		return fmt.Errorf("%s: %w", errCustomTypeUnsupported, err)
	}
	return err
}

// customTypeApplied checks that Asana's answer to a change with a custom type has it: an
// answer without it (the field ignored) fails the entry instead of completing it, which
// would lose the change silently. A task read without the optional fields is not checked.
func customTypeApplied(op outbox.Op, t *model.Task) error {
	if !domaintask.TouchesCustomType(op.Payload) || t == nil || t.Partial {
		return nil
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
			return fmt.Errorf("%s: %w", errCustomTypeUnsupported,
				&model.Error{Status: http.StatusUnprocessableEntity}) // 4xx: failed, not sent again
		}
	}
	return nil
}

// customTypePlanRefusal records a 402 to a change with a custom type for the task's
// workspace, for a while (plan.go): custom types are not offered there meanwhile.
func (s *Syncer) customTypePlanRefusal(ctx context.Context, op outbox.Op, err error) error {
	if ws, wsErr := s.Store.TaskWorkspace(ctx, op.TaskGID); wsErr == nil {
		_, _ = s.recordPlanRefusal(ctx, readmodel.PlanCustomTypes, ws, err)
	}
	return fmt.Errorf("%s: %w", readmodel.PlanUnavailableMessage(readmodel.PlanCustomTypes), err)
}
