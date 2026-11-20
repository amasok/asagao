package tui

import "github.com/amasok/asagao/internal/readmodel"

// listState is the state of the task list: its rows, the cursor, what narrows it and
// the tasks marked in it. The board and the timeline show the same rows.
type listState struct {
	rows   []row // the rows shown: all minus collapsed groups
	all    []row // every row
	cursor int   // index into rows; always on a task row when any exist
	offset int
	seq    int // drops stale task loads
	// scope is the scope whose tasks are in all (to tell a reload from a switch).
	scope string

	// collapsible groups (collapse.go): the keys of the groups collapsed, and the scope
	// they were loaded for
	collapsed      map[string]bool
	collapsedScope string
	expanded       map[string]bool // tasks whose subtasks are shown under them (subtasks.go)
	marked         map[string]bool // gids of the tasks marked (bulk.go)
	selectAfter    string          // gid to select after the next load (e.g. a created task)

	// what narrows the list
	filter      filterMode
	tagFilter   string
	fieldFilter fieldFilter
	saved       *readmodel.SavedSearch // the saved search shown (searches.go)
}
