// Package task holds the task types that the application layer and the store share.
// It has no dependencies, so the ports in internal/app can use these types without
// importing the store (docs/design/adr/0006-application-use-cases.md, decision 4).
package task

// NewTask is the input for creating a task locally.
type NewTask struct {
	ParentGID    string // create as a subtask of this task
	WorkspaceGID string
	ProjectGID   string
	SectionGID   string
	Name         string
	Notes        string
	HTMLNotes    string // Asana rich text; sent instead of Notes when set
	DueOn        string
	StartOn      string // needs DueOn, not after it (task.CheckDates)
	AssigneeGID  string
}

// TagRef is a tag to put on a task: an existing tag (GID) or a new one to create (Name).
type TagRef struct {
	GID  string
	Name string
}

// TagEdit is a resolved set of tag changes for one task.
type TagEdit struct {
	Add    []TagRef
	Remove []string // tag gids
}

// Empty reports whether the edit changes nothing.
func (e TagEdit) Empty() bool { return len(e.Add) == 0 && len(e.Remove) == 0 }
