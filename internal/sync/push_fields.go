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

func init() {
	registerOf(outbox.KindSetCustomField, handlerOf[outbox.SetCustomField]{send: (*Pusher).pushCustomField, batch: batchCustomField,
		fieldValues: func(p outbox.SetCustomField) map[string]any { return map[string]any{p.Field: p.Value} }})
	registerOf(outbox.KindCreateEnumOption, handlerOf[outbox.CreateEnumOption]{send: (*Pusher).pushCreateEnumOption, forbidden: enumOptionForbidden,
		find: func(s *Pusher, ctx context.Context, op outbox.Op, p outbox.CreateEnumOption, _ finder) (outcome, match, error) {
			m, err := s.findEnumOption(ctx, op, p)
			return sent, m, err
		},
		planWorkspace: func(_ *Pusher, _ context.Context, _ outbox.Op, p outbox.CreateEnumOption) string { return p.Workspace }})
	settingWorkspace := func(_ *Pusher, _ context.Context, _ outbox.Op, p outbox.FieldSetting) string { return p.Workspace }
	registerOf(outbox.KindAddFieldSetting, handlerOf[outbox.FieldSetting]{send: sends((*Pusher).pushFieldSetting), forbidden: fieldSettingForbidden, planWorkspace: settingWorkspace})
	registerOf(outbox.KindRemoveFieldSetting, handlerOf[outbox.FieldSetting]{send: sends((*Pusher).pushFieldSetting), forbidden: fieldSettingForbidden, planWorkspace: settingWorkspace})
}

// waitsForField reports whether a value of a custom field (set_custom_field, or a create
// with values) uses an option created offline whose creation is still queued, or a field
// whose adding to a project of the task is still queued. It waits, and goes out once that
// is sent (the option's gid is then replaced in it) or is discarded with it.
func (s *Pusher) waitsForField(ctx context.Context, op outbox.Op) (bool, error) {
	valuesOf := handlers[op.Kind].fieldValues
	if valuesOf == nil {
		return false, nil
	}
	for field, v := range valuesOf(op) {
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
func (s *Pusher) pushCreateEnumOption(ctx context.Context, op outbox.Op, p outbox.CreateEnumOption) (outcome, error) {
	if m, err := s.findEnumOption(ctx, op, p); err != nil || m == matched {
		return sent, err
	}
	created, err := s.API.CreateEnumOption(ctx, p.Field, p.Name)
	if isRefusedAsIs(err) {
		// e.g. "An enum option already exists with the name" (someone added it meanwhile):
		// take that one.
		if m, ferr := s.findEnumOption(ctx, op, p); ferr == nil && m == matched {
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
func (s *Pusher) findEnumOption(ctx context.Context, op outbox.Op, p outbox.CreateEnumOption) (match, error) {
	name := p.Name
	f, err := s.API.CustomField(ctx, p.Field)
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
func (s *Pusher) pushFieldSetting(ctx context.Context, op outbox.Op, p outbox.FieldSetting) error {
	project, add := op.TaskGID, op.Kind == outbox.KindAddFieldSetting
	field := p.Field
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

// enumOptionForbidden records that Asana refused (403) to add an option to a field, so
// that adding one is not offered again for that field, and says why.
func enumOptionForbidden(s *Pusher, ctx context.Context, _ outbox.Op, p outbox.CreateEnumOption, cause error) error {
	_ = s.Store.SetFieldOptionsLocked(ctx, p.Field, s.clock())
	return fmt.Errorf("Asana がこのフィールドへの選択肢の追加を許可しませんでした (選択肢の編集が制限されているか、権限がありません): %w", cause)
}

// fieldSettingForbidden records that Asana refused (403) to add a custom field to a
// project or remove one from it, so that it is not offered again for that project, and
// says why.
func fieldSettingForbidden(s *Pusher, ctx context.Context, op outbox.Op, _ outbox.FieldSetting, cause error) error {
	_ = s.Store.SetProjectFieldsLocked(ctx, op.TaskGID, s.clock())
	return fmt.Errorf("Asana がこのプロジェクトのカスタムフィールドの変更を許可しませんでした (権限がありません): %w", cause)
}

// syncWorkspaceCustomFields fetches the custom fields of a workspace (to add one to a
// project) with the poll, or if they were never fetched. Without custom fields (402: the
// plan) or the right to list them (403) there are none to offer; any other failure is
// retried on the next sync.
func (s *Puller) syncWorkspaceCustomFields(ctx context.Context, workspaceGID string, poll bool) error {
	if !poll {
		if last, err := s.Store.WorkspaceCustomFieldsFetchedAt(ctx, workspaceGID); err != nil || last != "" {
			return err
		}
	}
	if hidden, err := planHidden(ctx, s.Store, s.clock(), plan.CustomFields, workspaceGID); err != nil || hidden {
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
		if _, err := recordPlanRefusal(ctx, s.Store, s.clock(), plan.CustomFields, workspaceGID, err); err != nil {
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

// pushCustomField sets one custom field, after checking that nobody changed it in Asana
// since it was edited here. A field the task no longer has is treated like a 404 (the
// change is dropped and the user told).
func (s *Pusher) pushCustomField(ctx context.Context, op outbox.Op, p outbox.SetCustomField) (outcome, error) {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	if localgid.IsLocal(task) {
		return sent, &model.Error{Status: 404} // its create failed
	}
	server, err := s.API.Task(ctx, task)
	if err != nil {
		return sent, err
	}
	if conflicts, err := customFieldConflict(op, p, server); err != nil {
		return sent, err
	} else if conflicts != nil {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, conflicts)
	}
	t, err := s.API.UpdateTask(ctx, task, customFieldUpdate(p))
	var apiErr *model.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
		// Shown with the failed change (not dropped): the value may be in a form Asana does not take.
		return sent, fmt.Errorf("Asana が「%v」の値を受け付けませんでした（値の形式が合っていない可能性があります）: %w", p.FieldName, err)
	}
	if err != nil {
		return sent, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sent, sentNotSaved(err)
	}
	s.refresh(ctx, *t)
	return sent, nil
}

// customFieldConflict compares a custom field change with the task in Asana: the server's
// value if it was changed there too (nil if not), or a 404 if the task no longer has the
// field.
func customFieldConflict(op outbox.Op, p outbox.SetCustomField, server *model.Task) (map[string]any, error) {
	if server.Partial { // Asana gave no custom fields to compare with: just send
		return nil, nil
	}
	sv, display, found := model.ServerCustomField(server, p.Field)
	if !found {
		return nil, &model.Error{Status: http.StatusNotFound} // removed from the task's projects
	}
	if !same(sv, normalize(op.Base["value"])) && !same(sv, normalize(p.Value)) {
		return map[string]any{"value": sv, "display": display}, nil
	}
	return nil, nil
}

// customFieldUpdate is the task update that sets a custom field change.
func customFieldUpdate(p outbox.SetCustomField) map[string]any {
	return map[string]any{"custom_fields": map[string]any{p.Field: model.CustomFieldAPIValue(p.Type, p.Value)}}
}

// normalize turns a JSON-decoded list ([]any) into []string, so it prints like a stored one.
func normalize(v any) any {
	if l, ok := v.([]any); ok {
		out := make([]string, 0, len(l))
		for _, e := range l {
			out = append(out, fmt.Sprint(e))
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return v
}

// batchCustomField is the batch request for a set_custom_field, checked against the task in Asana first.
func batchCustomField(s *Pusher, ctx context.Context, op outbox.Op, p outbox.SetCustomField, e *batchEntry) (bool, error) {
	task := e.task
	if p.Field == "" {
		return false, nil
	}
	if wait, err := s.waitsForField(ctx, op); err != nil || wait {
		return false, err // an option or a field of the project is created first
	}
	e.write = model.BatchUpdateTask(task, customFieldUpdate(p))
	e.check = func(op outbox.Op, server *model.Task) (map[string]any, error) {
		return customFieldConflict(op, p, server)
	}
	return true, nil
}
