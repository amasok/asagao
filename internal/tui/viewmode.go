package tui

import (
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// viewMode is how the tasks are shown, one at a time: v cycles list -> board ->
// timeline (projects only) -> calendar -> list. A board or a timeline shows only in a
// project and a calendar not in the inbox (boardActive, timelineActive, calendarActive),
// so the mode is kept when moving to a scope that cannot show it.
type viewMode int

const (
	viewList viewMode = iota
	viewBoard
	viewTimeline
	viewCalendar
)

// boardState is the cursor of the board (board.go): sections are columns.
type boardState struct {
	col, row  int
	colOffset int // first column on screen
}

// calendarState is the state of the calendar (calendar.go).
type calendarState struct {
	day   time.Time // selected day
	month time.Time // month of tasks
	tasks map[string][]readmodel.TaskSummary
}

// timelineState is the state of the timeline (timeline.go).
type timelineState struct {
	start  time.Time // first day shown (zero: a week before today)
	dayW   int       // columns per day (0: the default)
	follow string    // the task the timeline last scrolled to
}
