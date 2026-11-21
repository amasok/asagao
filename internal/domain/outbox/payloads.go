package outbox

import "reflect"

// The payload structs of the kinds (see payload.go). A field is written back as it was
// read: a key always written is a plain field; one written only when it holds something
// omits its zero value; one whose "" (or false) says something else than its absence (a
// field that is cleared, not left as it is) is a pointer, nil when absent.

func init() {
	payloadType[UpdateTask](KindUpdateTask)
	payloadType[CreateTask](KindCreateTask)
	payloadType[DuplicateTask](KindDuplicateTask)
	payloadType[AddComment](KindAddComment)
	payloadType[MoveTask](KindMoveTask)
	payloadType[DeleteTask](KindDeleteTask)
	payloadType[SetParent](KindSetParent)
	payloadType[RenameSection](KindRenameSection)
	payloadType[CreateSection](KindCreateSection)
	payloadType[DeleteSection](KindDeleteSection)
	payloadType[InsertSection](KindInsertSection)
	payloadType[UpdateTag](KindUpdateTag)
	payloadType[DeleteTag](KindDeleteTag)
	payloadType[AddTag](KindAddTag)
	payloadType[RemoveTag](KindRemoveTag)
	payloadType[Follower](KindAddFollower, KindRemoveFollower)
	payloadType[ProjectMembership](KindAddProject, KindRemoveProject)
	payloadType[Dependency](KindAddDependency, KindRemoveDependency)
	payloadType[CreateStatus](KindCreateStatus)
	payloadType[UploadAttachment](KindUploadAttachment)
	payloadType[SetCustomField](KindSetCustomField)
	payloadType[MarkNotification](KindMarkNotification)
	payloadType[UpdateStory](KindUpdateStory)
	payloadType[DeleteStory](KindDeleteStory)
	payloadType[DeleteAttachment](KindDeleteAttachment)
	payloadType[MoveMySection](KindMoveMySection)
	payloadType[UpdateProject](KindUpdateProject)
	payloadType[UpdateProjectBrief](KindUpdateProjectBrief)
	payloadType[TimeEntry](KindAddTimeEntry, KindUpdateTimeEntry, KindDeleteTimeEntry)
	payloadType[CreateEnumOption](KindCreateEnumOption)
	payloadType[FieldSetting](KindAddFieldSetting, KindRemoveFieldSetting)
}

// Str returns the string p points to ("" for nil).
func Str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// UpdateTask is the payload of update_task: the fields of the task the edit changes
// (domain/task's updatable), nil when it leaves one as it is. "" clears due_on, start_on
// and assignee; notes comes with html_notes (the plain text of the rich one).
type UpdateTask struct {
	Completed              *bool   `json:"completed,omitempty"`
	Liked                  *bool   `json:"liked,omitempty"`
	Name                   *string `json:"name,omitempty"`
	Notes                  *string `json:"notes,omitempty"`
	HTMLNotes              *string `json:"html_notes,omitempty"`
	DueOn                  *string `json:"due_on,omitempty"`
	StartOn                *string `json:"start_on,omitempty"`
	Assignee               *string `json:"assignee,omitempty"`
	ResourceSubtype        *string `json:"resource_subtype,omitempty"`
	ApprovalStatus         *string `json:"approval_status,omitempty"`
	CustomType             *string `json:"custom_type,omitempty"`
	CustomTypeStatusOption *string `json:"custom_type_status_option,omitempty"`
}

// CreateTask is the payload of create_task. The edits of the task made before the create
// is sent are folded in (store: MergeFields of the changes), so it may hold any field of an
// update too. parent, project and section say where the task is created; custom_fields
// holds the custom field values in Asana's form (field gid → value).
type CreateTask struct {
	Workspace              string         `json:"workspace"` // "" for a subtask
	Name                   string         `json:"name"`
	Parent                 string         `json:"parent,omitempty"`
	Project                string         `json:"project,omitempty"`
	Section                *string        `json:"section,omitempty"`
	Notes                  *string        `json:"notes,omitempty"`
	HTMLNotes              *string        `json:"html_notes,omitempty"`
	DueOn                  *string        `json:"due_on,omitempty"`
	StartOn                *string        `json:"start_on,omitempty"`
	Assignee               *string        `json:"assignee,omitempty"`
	Completed              *bool          `json:"completed,omitempty"`
	Liked                  *bool          `json:"liked,omitempty"`
	ResourceSubtype        *string        `json:"resource_subtype,omitempty"`
	ApprovalStatus         *string        `json:"approval_status,omitempty"`
	CustomType             *string        `json:"custom_type,omitempty"`
	CustomTypeStatusOption *string        `json:"custom_type_status_option,omitempty"`
	CustomFields           map[string]any `json:"custom_fields,omitempty"`
}

// DuplicateTask is the payload of duplicate_task: the task copied, and the copy's name.
type DuplicateTask struct {
	Source string `json:"source"`
	Name   string `json:"name"`
}

// AddComment is the payload of add_comment. StoryGID is the comment's local gid.
type AddComment struct {
	Text     string `json:"text"`
	HTMLText string `json:"html_text,omitempty"`
	StoryGID string `json:"story_gid,omitempty"`
}

// MoveTask is the payload of move_task: the section of the project the task goes to, and
// where in it (a task of the section, optional).
type MoveTask struct {
	Project      string `json:"project"`
	Section      string `json:"section"`
	SectionName  string `json:"section_name"`
	InsertBefore string `json:"insert_before,omitempty"`
	InsertAfter  string `json:"insert_after,omitempty"`
}

// DeleteTask is the payload of delete_task: the task's name (for the outbox list).
type DeleteTask struct {
	Name string `json:"name"`
}

// SetParent is the payload of set_parent: the new parent ("" for a top-level task) and
// where among its subtasks (a sibling, optional).
type SetParent struct {
	Parent       string `json:"parent"`
	ParentName   string `json:"parent_name"`
	InsertBefore string `json:"insert_before,omitempty"`
	InsertAfter  string `json:"insert_after,omitempty"`
}

// RenameSection is the payload of rename_section.
type RenameSection struct {
	Name string `json:"name"`
}

// CreateSection is the payload of create_section: before / after is a section (absent:
// at the end). Existing is set before it is first sent: the sections of the project that
// already had its name (empty: none), so that one of them is not taken for it.
type CreateSection struct {
	Project  string   `json:"project"`
	Name     string   `json:"name"`
	Before   string   `json:"before,omitempty"`
	After    string   `json:"after,omitempty"`
	Existing []string `json:"existing,omitzero"`
}

// DeleteSection is the payload of delete_section (Position: where it was, to restore).
type DeleteSection struct {
	Project  string `json:"project"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

// InsertSection is the payload of insert_section: exactly one of before / after.
type InsertSection struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
}

// UpdateTag is the payload of update_tag: the new name and / or color ("" for none), nil
// when unchanged.
type UpdateTag struct {
	Name      *string `json:"name,omitempty"`
	Color     *string `json:"color,omitempty"`
	Workspace string  `json:"workspace,omitempty"`
}

// DeleteTag is the payload of delete_tag: the tag and the tasks it was on, to restore.
// Workspace is "" when it was not known (and absent before #68).
type DeleteTag struct {
	Name      string   `json:"name"`
	Color     string   `json:"color"`
	Workspace string   `json:"workspace"`
	Tasks     []string `json:"tasks"`
}

// AddTag is the payload of add_tag: Tag is "local:..." for a tag created offline (created
// in Workspace with TagName first).
type AddTag struct {
	Tag       string `json:"tag"`
	TagName   string `json:"tag_name"`
	Workspace string `json:"workspace"`
}

// RemoveTag is the payload of remove_tag.
type RemoveTag struct {
	Tag     string `json:"tag"`
	TagName string `json:"tag_name"`
}

// Follower is the payload of add_follower and remove_follower.
type Follower struct {
	User     string `json:"user"`
	UserName string `json:"user_name"`
}

// ProjectMembership is the payload of add_project and remove_project: Section is the
// section the task goes to (or was in, to restore); "" or absent for none.
type ProjectMembership struct {
	Project     string  `json:"project"`
	ProjectName string  `json:"project_name"`
	Section     *string `json:"section,omitempty"`
}

// Dependency is the payload of add_dependency and remove_dependency.
type Dependency struct {
	Dependency     string `json:"dependency"`
	DependencyName string `json:"dependency_name"`
}

// CreateStatus is the payload of create_status. Status is the update's local gid.
type CreateStatus struct {
	Status     string `json:"status"`
	StatusType string `json:"status_type"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	HTMLText   string `json:"html_text"`
}

// UploadAttachment is the payload of upload_attachment: the attachment's local gid, its
// name and the local file sent.
type UploadAttachment struct {
	Attachment string `json:"attachment"`
	Name       string `json:"name"`
	Path       string `json:"path"`
}

// SetCustomField is the payload of set_custom_field. Value is nil (clear), a string
// (text, a date, an enum option), a number, or a list of gids (people, multi_enum).
type SetCustomField struct {
	Field     string `json:"field"`
	FieldName string `json:"field_name"`
	Type      string `json:"type"`
	Value     any    `json:"value"`
	Display   string `json:"display"`
}

// MarkNotification is the payload of mark_notification.
type MarkNotification struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
	Read      bool   `json:"read"`
}

// UpdateStory is the payload of update_story ("" HTMLText: plain text).
type UpdateStory struct {
	Story    string `json:"story"`
	Text     string `json:"text"`
	HTMLText string `json:"html_text"`
}

// DeleteStory is the payload of delete_story.
type DeleteStory struct {
	Story string `json:"story"`
	Text  string `json:"text"`
}

// DeleteAttachment is the payload of delete_attachment. Path is set for a local
// attachment whose upload was being sent (its file is removed once it is deleted).
type DeleteAttachment struct {
	Attachment string `json:"attachment"`
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
}

// MoveMySection is the payload of move_my_section.
type MoveMySection struct {
	Section     string `json:"section"`
	SectionName string `json:"section_name"`
	Workspace   string `json:"workspace"`
}

// UpdateProject is the payload of update_project: the fields of the project the edit
// changes (domain/project.Fields), nil when it leaves one as it is. "" clears owner,
// start_on and due_on.
type UpdateProject struct {
	Notes     *string `json:"notes,omitempty"`
	HTMLNotes *string `json:"html_notes,omitempty"`
	Owner     *string `json:"owner,omitempty"`
	StartOn   *string `json:"start_on,omitempty"`
	DueOn     *string `json:"due_on,omitempty"`
	Color     *string `json:"color,omitempty"`
}

// UpdateProjectBrief is the payload of update_project_brief: Delete, or the title and / or
// the body (html_text with its plain text) to set, nil when unchanged.
type UpdateProjectBrief struct {
	Delete   bool    `json:"delete,omitempty"`
	Title    *string `json:"title,omitempty"`
	HTMLText *string `json:"html_text,omitempty"`
	Text     *string `json:"text,omitempty"`
}

// TimeEntry is the payload of add_time_entry, update_time_entry and delete_time_entry.
// Entry is "local:..." for an entry whose add is not sent yet.
type TimeEntry struct {
	Entry           string `json:"entry"`
	DurationMinutes int    `json:"duration_minutes"`
	EnteredOn       string `json:"entered_on"`
}

// CreateEnumOption is the payload of create_enum_option. Workspace is "" when it was not
// known (and absent before it was recorded).
type CreateEnumOption struct {
	Field     string `json:"field"`
	FieldName string `json:"field_name"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
}

// FieldSetting is the payload of add_custom_field_setting and remove_custom_field_setting.
type FieldSetting struct {
	Field     string `json:"field"`
	FieldName string `json:"field_name"`
	Workspace string `json:"workspace"`
}

// sectionRefs are the payloads that name sections (under SectionRefKeys).
type sectionRefs interface{ sectionRefs() []string }

func (p CreateTask) sectionRefs() []string        { return []string{Str(p.Section)} }
func (p MoveTask) sectionRefs() []string          { return []string{p.Section} }
func (p ProjectMembership) sectionRefs() []string { return []string{Str(p.Section)} }
func (p CreateSection) sectionRefs() []string     { return []string{p.Before, p.After} }
func (p InsertSection) sectionRefs() []string     { return []string{p.Before, p.After} }
func (p MoveMySection) sectionRefs() []string     { return []string{p.Section} }

// SectionRefs returns the sections the payload of op names (under SectionRefKeys: the
// section a task goes to, the section another one is put next to), which may have been
// created offline. An unreadable payload names none.
func SectionRefs(op Op) []string {
	p, err := DecodePayload(op)
	if err != nil {
		return nil
	}
	return refsOf(p)
}

// SectionRefsAsFarAsTheyRead returns the sections the payload of op names as SectionRefs,
// but read as far as it reads (BestEffortPayload): an entry held because it cannot be read
// entirely still names the sections its keys that read hold.
func SectionRefsAsFarAsTheyRead(op Op) []string {
	t, ok := payloadTypes[op.Kind]
	if !ok {
		return nil
	}
	p := reflect.New(t)
	readAsFarAsItReads(op.Payload, p.Interface())
	return refsOf(p.Elem().Interface())
}

// refsOf returns the sections payload p names ("" left out).
func refsOf(p any) []string {
	r, ok := p.(sectionRefs)
	if !ok {
		return nil
	}
	var out []string
	for _, g := range r.sectionRefs() {
		if g != "" {
			out = append(out, g)
		}
	}
	return out
}
