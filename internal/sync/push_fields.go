package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// Sending changes to custom fields themselves (#37): an option added to a field, a field
// added to or removed from a project.

// waitsForField reports whether a value of a custom field (set_custom_field, or a create
// with values) uses an option created offline whose creation is still queued, or a field
// whose adding to a project of the task is still queued. It waits, and goes out once that
// is sent (the option's gid is then replaced in it) or is discarded with it.
func (s *Syncer) waitsForField(ctx context.Context, op outbox.Op) (bool, error) {
	values := map[string]any{}
	switch op.Kind {
	case outbox.KindSetCustomField:
		field, _ := op.Payload["field"].(string)
		values[field] = op.Payload["value"]
	case outbox.KindCreateTask:
		cfs, _ := op.Payload["custom_fields"].(map[string]any)
		for k, v := range cfs {
			values[k] = v
		}
	default:
		return false, nil
	}
	for field, v := range values {
		if wait, err := s.Store.FieldSettingQueued(ctx, op.TaskGID, field); err != nil || wait {
			return wait, err
		}
		for _, g := range optionGIDs(v) {
			if !localgid.IsLocal(g) {
				continue
			}
			if wait, err := s.Store.EnumOptionCreateQueued(ctx, g); err != nil || wait {
				return wait, err
			}
		}
	}
	return false, nil
}

// optionGIDs are the strings of an enum / multi_enum value (as stored or as sent).
func optionGIDs(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			if g, ok := e.(string); ok {
				out = append(out, g)
			}
		}
		return out
	}
	return nil
}

// pushCreateEnumOption adds an option to a field. An enabled option with the name that
// Asana already has (added by someone else meanwhile, or by an earlier request of ours
// whose answer was lost) is taken instead of creating a second one.
func (s *Syncer) pushCreateEnumOption(ctx context.Context, op outbox.Op) (outcome, error) {
	if m, err := s.findEnumOption(ctx, op); err != nil || m == matched {
		return sent, err
	}
	field, _ := op.Payload["field"].(string)
	name, _ := op.Payload["name"].(string)
	created, err := s.API.CreateEnumOption(ctx, field, name)
	if isRefusedAsIs(err) {
		// e.g. "An enum option already exists with the name" (someone added it meanwhile):
		// take that one.
		if m, ferr := s.findEnumOption(ctx, op); ferr == nil && m == matched {
			return sent, nil
		}
	}
	if err != nil {
		return sent, err
	}
	if err := s.Store.CompleteCreateEnumOption(ctx, op.ID, op.TaskGID, *created); err != nil {
		return sent, sentNotSaved(err)
	}
	return sent, nil
}

// findEnumOption looks in Asana for an enabled option of the field with the entry's name
// that no other local entry took (the first, if there are several: they are the same
// choice to the user). matched: it was recorded as the entry's result.
func (s *Syncer) findEnumOption(ctx context.Context, op outbox.Op) (match, error) {
	field, _ := op.Payload["field"].(string)
	name, _ := op.Payload["name"].(string)
	f, err := s.API.CustomField(ctx, field)
	if err != nil {
		return notFound, err
	}
	used, err := s.Store.RecordedResults(ctx, readmodel.ResultEnumOption)
	if err != nil {
		return notFound, err
	}
	for _, o := range f.EnumOptions {
		if o.Name == name && (o.Enabled == nil || *o.Enabled) && !used[o.GID] {
			return matched, s.Store.CompleteCreateEnumOption(ctx, op.ID, op.TaskGID, o)
		}
	}
	return notFound, nil
}

// pushFieldSetting adds a field to a project or removes it. If Asana refuses because the
// project already is as wanted (someone else did it, or an earlier request whose answer
// was lost), it is done. The project's fields are then fetched again.
func (s *Syncer) pushFieldSetting(ctx context.Context, op outbox.Op) error {
	project, add := op.TaskGID, op.Kind == outbox.KindAddFieldSetting
	field, _ := op.Payload["field"].(string)
	var err error
	if add {
		err = s.API.AddCustomFieldSetting(ctx, project, field)
	} else {
		err = s.API.RemoveCustomFieldSetting(ctx, project, field)
	}
	var settings []model.CustomFieldSetting
	if isRefusedAsIs(err) { // e.g. "Custom field already exists in parent object."
		var gerr error
		if settings, gerr = s.API.CustomFieldSettings(ctx, project); gerr == nil && hasField(settings, field) == add {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sentNotSaved(err)
	}
	if settings == nil {
		settings, err = s.API.CustomFieldSettings(ctx, project)
	}
	if err == nil { // else fetched on a later sync
		if err := s.Store.ReplaceProjectCustomFields(ctx, project, settings); err == nil {
			_ = s.Store.SetProjectCustomFieldsFetchedAt(ctx, project, s.clock())
		}
	}
	return nil
}

// isRefusedAsIs reports whether Asana refused a request because of what it asked for (a
// 4xx other than a missing login, plan, permission, object or a rate limit): it may be
// that Asana already is as asked.
func isRefusedAsIs(err error) bool {
	var apiErr *model.Error
	if !errors.As(err, &apiErr) || apiErr.Status < 400 || apiErr.Status >= 500 {
		return false
	}
	switch apiErr.Status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests:
		return false
	}
	return true
}

func hasField(settings []model.CustomFieldSetting, field string) bool {
	for _, st := range settings {
		if st.CustomField.GID == field {
			return true
		}
	}
	return false
}

// fieldForbidden records that Asana refused (403) a change to a custom field itself, so
// that it is not offered again for that field or project, and says why.
func (s *Syncer) fieldForbidden(ctx context.Context, op outbox.Op, cause error) error {
	if !isForbidden(cause) {
		return cause
	}
	now := s.clock()
	switch op.Kind {
	case outbox.KindCreateEnumOption:
		field, _ := op.Payload["field"].(string)
		_ = s.Store.SetFieldOptionsLocked(ctx, field, now)
		return fmt.Errorf("Asana がこのフィールドへの選択肢の追加を許可しませんでした (選択肢の編集が制限されているか、権限がありません): %w", cause)
	case outbox.KindAddFieldSetting, outbox.KindRemoveFieldSetting:
		_ = s.Store.SetProjectFieldsLocked(ctx, op.TaskGID, now)
		return fmt.Errorf("Asana がこのプロジェクトのカスタムフィールドの変更を許可しませんでした (権限がありません): %w", cause)
	}
	return cause
}

// syncWorkspaceCustomFields fetches the custom fields of a workspace (to add one to a
// project) with the poll, or if they were never fetched. Without custom fields (402: the
// plan) or the right to list them (403) there are none to offer; any other failure is
// retried on the next sync.
func (s *Syncer) syncWorkspaceCustomFields(ctx context.Context, workspaceGID string, poll bool) error {
	if !poll {
		if last, err := s.Store.WorkspaceCustomFieldsFetchedAt(ctx, workspaceGID); err != nil || last != "" {
			return err
		}
	}
	if hidden, err := s.planHidden(ctx, plan.CustomFields, workspaceGID); err != nil || hidden {
		return err // a recent 402: not asked for again yet
	}
	fields, err := s.API.WorkspaceCustomFields(ctx, workspaceGID)
	now := s.clock()
	switch {
	case err == nil:
		if err := s.Store.ReplaceWorkspaceCustomFields(ctx, workspaceGID, fields); err != nil {
			return err
		}
	case model.IsPaymentRequired(err):
		if err := s.Store.ReplaceWorkspaceCustomFields(ctx, workspaceGID, nil); err != nil {
			return err
		}
		if _, err := s.recordPlanRefusal(ctx, plan.CustomFields, workspaceGID, err); err != nil {
			return err
		}
	case isForbidden(err):
		if err := s.Store.ReplaceWorkspaceCustomFields(ctx, workspaceGID, nil); err != nil {
			return err
		}
	default:
		return nil
	}
	return s.Store.SetWorkspaceCustomFieldsFetchedAt(ctx, workspaceGID, now)
}
