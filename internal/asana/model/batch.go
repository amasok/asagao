package model

// MaxBatchActions is how many requests Asana accepts in one POST /batch.
const MaxBatchActions = 10

// BatchWriteKind identifies an operation sent in a POST /batch write.
type BatchWriteKind string

const (
	BatchKindUpdateTask       BatchWriteKind = "update_task"
	BatchKindMoveTask         BatchWriteKind = "move_task"
	BatchKindAddTag           BatchWriteKind = "add_tag"
	BatchKindRemoveTag        BatchWriteKind = "remove_tag"
	BatchKindAddFollower      BatchWriteKind = "add_follower"
	BatchKindRemoveFollower   BatchWriteKind = "remove_follower"
	BatchKindAddProject       BatchWriteKind = "add_project"
	BatchKindRemoveProject    BatchWriteKind = "remove_project"
	BatchKindAddDependency    BatchWriteKind = "add_dependency"
	BatchKindRemoveDependency BatchWriteKind = "remove_dependency"
)

// BatchWrite is an intent-based write sent with others in a POST /batch (asana.Client.Batch).
// Construct one with the Batch* helpers; internal/asana translates it into the REST action.
type BatchWrite struct {
	Kind       BatchWriteKind
	TaskGID    string
	Fields     map[string]any
	SectionGID string
	TagGID     string
	UserGID    string
	ProjectGID string
	DepGID     string
}

// BatchUpdateTask builds a batch write that updates taskGID with fields.
func BatchUpdateTask(taskGID string, fields map[string]any) BatchWrite {
	return BatchWrite{Kind: BatchKindUpdateTask, TaskGID: taskGID, Fields: fields}
}

// BatchMoveTaskToSection builds a batch write that moves taskGID into sectionGID.
func BatchMoveTaskToSection(sectionGID, taskGID string) BatchWrite {
	return BatchWrite{Kind: BatchKindMoveTask, TaskGID: taskGID, SectionGID: sectionGID}
}

// BatchAddTag builds a batch write that adds tagGID to taskGID.
func BatchAddTag(taskGID, tagGID string) BatchWrite {
	return BatchWrite{Kind: BatchKindAddTag, TaskGID: taskGID, TagGID: tagGID}
}

// BatchRemoveTag builds a batch write that removes tagGID from taskGID.
func BatchRemoveTag(taskGID, tagGID string) BatchWrite {
	return BatchWrite{Kind: BatchKindRemoveTag, TaskGID: taskGID, TagGID: tagGID}
}

// BatchAddFollower builds a batch write that adds userGID as a follower of taskGID.
func BatchAddFollower(taskGID, userGID string) BatchWrite {
	return BatchWrite{Kind: BatchKindAddFollower, TaskGID: taskGID, UserGID: userGID}
}

// BatchRemoveFollower builds a batch write that removes userGID from taskGID's followers.
func BatchRemoveFollower(taskGID, userGID string) BatchWrite {
	return BatchWrite{Kind: BatchKindRemoveFollower, TaskGID: taskGID, UserGID: userGID}
}

// BatchAddProject builds a batch write that adds taskGID to projectGID (and optionally sectionGID).
func BatchAddProject(taskGID, projectGID, sectionGID string) BatchWrite {
	return BatchWrite{Kind: BatchKindAddProject, TaskGID: taskGID, ProjectGID: projectGID, SectionGID: sectionGID}
}

// BatchRemoveProject builds a batch write that removes taskGID from projectGID.
func BatchRemoveProject(taskGID, projectGID string) BatchWrite {
	return BatchWrite{Kind: BatchKindRemoveProject, TaskGID: taskGID, ProjectGID: projectGID}
}

// BatchAddDependency builds a batch write that makes taskGID wait for depGID.
func BatchAddDependency(taskGID, depGID string) BatchWrite {
	return BatchWrite{Kind: BatchKindAddDependency, TaskGID: taskGID, DepGID: depGID}
}

// BatchRemoveDependency builds a batch write that stops taskGID waiting for depGID.
func BatchRemoveDependency(taskGID, depGID string) BatchWrite {
	return BatchWrite{Kind: BatchKindRemoveDependency, TaskGID: taskGID, DepGID: depGID}
}

// BatchWriteResult is Asana's answer to one BatchWrite.
type BatchWriteResult struct {
	Task *Task // the task in the response (for BatchKindUpdateTask) when Err is nil
	Err  error // *Error when Asana answered this request with a status >= 400
}
