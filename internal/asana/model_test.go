package asana

import "github.com/amasok/asagao/internal/asana/model"

// The asana names are the model types themselves (aliases), so either can be passed.
var (
	_ model.Task          = Task{}
	_ *model.Error        = (*Error)(nil)
	_ []model.CustomField = []CustomField(nil)
	_ model.TaskQuery     = TaskQuery{}
	_ model.EventPage     = EventPage{}
)
