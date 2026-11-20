package asana

import (
	"fmt"
	"net/url"

	"github.com/amasok/asagao/internal/asana/model"
)

type batchAction struct {
	Method       string         `json:"method"`
	RelativePath string         `json:"relative_path"`
	Data         map[string]any `json:"data,omitempty"`
	Options      map[string]any `json:"options,omitempty"`
}

func requireBatchGID(kind model.BatchWriteKind, name, val string) error {
	if val == "" {
		return fmt.Errorf("asana: batch %q: %s is required", kind, name)
	}
	return nil
}

// batchActionFor converts an intent-based BatchWrite into the POST /batch action body,
// matching the REST paths and payloads in resources.go and dependencies.go.
func batchActionFor(w model.BatchWrite, taskFields []string) (act batchAction, wantTask bool, err error) {
	taskAction := func(suffix string, data map[string]any) (batchAction, bool, error) {
		return batchAction{
			Method:       "post",
			RelativePath: "/tasks/" + url.PathEscape(w.TaskGID) + suffix,
			Data:         data,
		}, false, nil
	}

	switch w.Kind {
	case model.BatchKindUpdateTask:
		if err := requireBatchGID(w.Kind, "task gid", w.TaskGID); err != nil {
			return batchAction{}, false, err
		}
		return batchAction{
			Method:       "put",
			RelativePath: "/tasks/" + url.PathEscape(w.TaskGID),
			Data:         w.Fields,
			Options:      map[string]any{"fields": taskFields},
		}, true, nil
	case model.BatchKindMoveTask:
		if err := requireBatchGID(w.Kind, "section gid", w.SectionGID); err != nil {
			return batchAction{}, false, err
		}
		if err := requireBatchGID(w.Kind, "task gid", w.TaskGID); err != nil {
			return batchAction{}, false, err
		}
		return batchAction{
			Method:       "post",
			RelativePath: "/sections/" + url.PathEscape(w.SectionGID) + "/addTask",
			Data:         map[string]any{"task": w.TaskGID},
		}, false, nil
	case model.BatchKindAddTag, model.BatchKindRemoveTag:
		if err := requireBatchGID(w.Kind, "task gid", w.TaskGID); err != nil {
			return batchAction{}, false, err
		}
		if err := requireBatchGID(w.Kind, "tag gid", w.TagGID); err != nil {
			return batchAction{}, false, err
		}
		suffix := "/addTag"
		if w.Kind == model.BatchKindRemoveTag {
			suffix = "/removeTag"
		}
		return taskAction(suffix, map[string]any{"tag": w.TagGID})
	case model.BatchKindAddFollower, model.BatchKindRemoveFollower:
		if err := requireBatchGID(w.Kind, "task gid", w.TaskGID); err != nil {
			return batchAction{}, false, err
		}
		if err := requireBatchGID(w.Kind, "user gid", w.UserGID); err != nil {
			return batchAction{}, false, err
		}
		suffix := "/addFollowers"
		if w.Kind == model.BatchKindRemoveFollower {
			suffix = "/removeFollowers"
		}
		return taskAction(suffix, map[string]any{"followers": []string{w.UserGID}})
	case model.BatchKindAddProject:
		if err := requireBatchGID(w.Kind, "task gid", w.TaskGID); err != nil {
			return batchAction{}, false, err
		}
		if err := requireBatchGID(w.Kind, "project gid", w.ProjectGID); err != nil {
			return batchAction{}, false, err
		}
		data := map[string]any{"project": w.ProjectGID}
		if w.SectionGID != "" {
			data["section"] = w.SectionGID
		}
		return taskAction("/addProject", data)
	case model.BatchKindRemoveProject:
		if err := requireBatchGID(w.Kind, "task gid", w.TaskGID); err != nil {
			return batchAction{}, false, err
		}
		if err := requireBatchGID(w.Kind, "project gid", w.ProjectGID); err != nil {
			return batchAction{}, false, err
		}
		return taskAction("/removeProject", map[string]any{"project": w.ProjectGID})
	case model.BatchKindAddDependency, model.BatchKindRemoveDependency:
		if err := requireBatchGID(w.Kind, "task gid", w.TaskGID); err != nil {
			return batchAction{}, false, err
		}
		if err := requireBatchGID(w.Kind, "dependency gid", w.DepGID); err != nil {
			return batchAction{}, false, err
		}
		suffix := "/addDependencies"
		if w.Kind == model.BatchKindRemoveDependency {
			suffix = "/removeDependencies"
		}
		return taskAction(suffix, map[string]any{"dependencies": []string{w.DepGID}})
	default:
		return batchAction{}, false, fmt.Errorf("asana: batch: unknown write kind %q", w.Kind)
	}
}
