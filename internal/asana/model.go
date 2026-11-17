package asana

import "github.com/amasok/asagao/internal/asana/model"

// The types of Asana's resources live in internal/asana/model, so that the code that only
// needs the types (the store, the sync engine) does not need the client. These aliases
// keep the asana names while the callers move (migration-plan.md, d1).
type (
	Ref                = model.Ref
	User               = model.User
	Workspace          = model.Workspace
	Project            = model.Project
	Section            = model.Section
	Membership         = model.Membership
	Task               = model.Task
	TaskQuery          = model.TaskQuery
	Story              = model.Story
	Attachment         = model.Attachment
	StatusUpdate       = model.StatusUpdate
	EnumOption         = model.EnumOption
	DateValue          = model.DateValue
	CustomField        = model.CustomField
	CustomFieldSetting = model.CustomFieldSetting
	Event              = model.Event
	EventPage          = model.EventPage
	Notification       = model.Notification
	WorkRef            = model.WorkRef
	TaskRef            = model.TaskRef
	Error              = model.Error
)
