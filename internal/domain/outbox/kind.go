package outbox

import "slices"

// Kind is what an outbox entry does (the outbox kind column). It is stored as its string,
// so the values of the constants must never change.
type Kind string

// Subject is what the outbox task_gid column of an entry holds.
type Subject int

const (
	// SubjectTask: a task the entry changes (it may be local, its create queued before it).
	SubjectTask Subject = iota + 1
	// SubjectNewTask: the local task the entry itself creates in Asana (create, duplicate).
	SubjectNewTask
	// SubjectSection: a section ("local:..." while its create_section is queued).
	SubjectSection
	// SubjectTag: a tag.
	SubjectTag
	// SubjectProject: a project.
	SubjectProject
	// SubjectNotification: an inbox notification.
	SubjectNotification
	// SubjectEnumOption: an enum option ("local:..." until Asana created it).
	SubjectEnumOption
)

// Spec is what the rules of the outbox need to know about a kind.
type Spec struct {
	// Subject is what the task_gid column holds.
	Subject Subject
	// Creates is whether sending the entry creates something in Asana, so that sending it
	// twice would create it twice (see SafeToResend).
	Creates bool
}

// specs holds every kind. A kind missing here is a bug: TestEveryKindHasASpec and the
// registries of the sync engine and the store check each other against AllKinds.
var specs = map[Kind]Spec{
	KindUpdateTask:         {Subject: SubjectTask},
	KindCreateTask:         {Subject: SubjectNewTask, Creates: true},
	KindDuplicateTask:      {Subject: SubjectNewTask, Creates: true},
	KindAddComment:         {Subject: SubjectTask, Creates: true},
	KindMoveTask:           {Subject: SubjectTask},
	KindRenameSection:      {Subject: SubjectSection},
	KindCreateSection:      {Subject: SubjectSection, Creates: true},
	KindDeleteSection:      {Subject: SubjectSection},
	KindInsertSection:      {Subject: SubjectSection},
	KindUpdateTag:          {Subject: SubjectTag},
	KindDeleteTag:          {Subject: SubjectTag},
	KindCreateStatus:       {Subject: SubjectProject, Creates: true},
	KindAddTag:             {Subject: SubjectTask},
	KindRemoveTag:          {Subject: SubjectTask},
	KindAddFollower:        {Subject: SubjectTask},
	KindRemoveFollower:     {Subject: SubjectTask},
	KindAddProject:         {Subject: SubjectTask},
	KindRemoveProject:      {Subject: SubjectTask},
	KindUploadAttachment:   {Subject: SubjectTask, Creates: true},
	KindDeleteTask:         {Subject: SubjectTask},
	KindSetParent:          {Subject: SubjectTask},
	KindAddDependency:      {Subject: SubjectTask},
	KindRemoveDependency:   {Subject: SubjectTask},
	KindSetCustomField:     {Subject: SubjectTask},
	KindMarkNotification:   {Subject: SubjectNotification},
	KindUpdateStory:        {Subject: SubjectTask},
	KindDeleteStory:        {Subject: SubjectTask},
	KindDeleteAttachment:   {Subject: SubjectTask},
	KindMoveMySection:      {Subject: SubjectTask},
	KindUpdateProject:      {Subject: SubjectProject},
	KindUpdateProjectBrief: {Subject: SubjectProject},
	KindAddTimeEntry:       {Subject: SubjectTask, Creates: true},
	KindUpdateTimeEntry:    {Subject: SubjectTask},
	KindDeleteTimeEntry:    {Subject: SubjectTask},
	KindCreateEnumOption:   {Subject: SubjectEnumOption, Creates: true},
	KindAddFieldSetting:    {Subject: SubjectProject},
	KindRemoveFieldSetting: {Subject: SubjectProject},
}

// AllKinds returns every kind, sorted.
func AllKinds() []Kind {
	ks := make([]Kind, 0, len(specs))
	for k := range specs {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// SpecOf returns the spec of kind k; ok is false for a kind this version does not know
// (an entry written by a newer one).
func SpecOf(k Kind) (s Spec, ok bool) {
	s, ok = specs[k]
	return s, ok
}

// SubjectOf returns what the task_gid column of an entry of kind k holds (0 for an
// unknown kind).
func SubjectOf(k Kind) Subject { return specs[k].Subject }

// Known reports whether k is a kind this version knows.
func (k Kind) Known() bool {
	_, ok := specs[k]
	return ok
}

// OnTask reports whether an entry of kind k changes an existing task (task_gid holds it),
// rather than creating one or changing something else (a section, a tag, a project, ...).
func (k Kind) OnTask() bool { return specs[k].Subject == SubjectTask }

// TaskDeps returns the tasks op needs in Asana before it can be sent: the task it changes,
// a new parent, or the parent a task is created under. A task created offline among them
// makes the entry wait for that create.
func TaskDeps(op Op) []string {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	switch SubjectOf(op.Kind) {
	case SubjectTask:
		if op.Kind == KindSetParent && str("parent") != "" {
			return []string{str("parent"), op.TaskGID}
		}
		return []string{op.TaskGID}
	case SubjectNewTask:
		if op.Kind == KindCreateTask && str("parent") != "" {
			return []string{str("parent")}
		}
	}
	return nil
}
