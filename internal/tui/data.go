package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/amasok/asana-tui/internal/store"
)

// queryRows loads the tasks for the list pane and groups them:
//   - search: one group with all hits (across every project)
//   - My Tasks: by due date (期限切れ / 今日 / 7日以内 / それ以降 / 期日なし)
//   - project: by section, in Asana's section order
func queryRows(ctx context.Context, st *store.Store, sc scope, query string, filter filterMode, now time.Time) ([]row, error) {
	f := store.TaskFilter{Query: query}
	today := now.Format(time.DateOnly)
	f.Completed = boolPtr(false)
	switch filter {
	case filterDueWeek:
		f.DueBefore = now.AddDate(0, 0, 7).Format(time.DateOnly)
	case filterOverdue:
		f.DueBefore = now.AddDate(0, 0, -1).Format(time.DateOnly)
	case filterCompleted:
		f.Completed = boolPtr(true)
	case filterAll:
		f.Completed = nil
	}

	if query != "" {
		if filter == filterOpen {
			f.Completed = nil // search includes completed tasks unless a filter says otherwise
		}
		tasks, err := st.ListTasks(ctx, f)
		if err != nil {
			return nil, err
		}
		return group(tasks, []string{fmt.Sprintf("検索結果 (%d)", len(tasks))}, func(store.TaskSummary) int { return 0 }), nil
	}

	switch sc.kind {
	case scopeProject:
		f.ProjectGID = sc.project
		tasks, err := st.ListTasks(ctx, f)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(sc.sections)+1)
		index := map[string]int{}
		for i, s := range sc.sections {
			names = append(names, s.Name)
			index[s.GID] = i
		}
		other := len(names)
		names = append(names, "その他")
		return group(tasks, names, func(t store.TaskSummary) int {
			for _, m := range t.Memberships {
				if m.ProjectGID == sc.project {
					if i, ok := index[m.SectionGID]; ok {
						return i
					}
				}
			}
			return other
		}), nil

	default: // My Tasks
		me, err := st.MeGID(ctx)
		if err != nil {
			return nil, err
		}
		f.AssigneeGID = me
		tasks, err := st.ListTasks(ctx, f)
		if err != nil {
			return nil, err
		}
		if filter == filterCompleted {
			return group(tasks, []string{"完了済み"}, func(store.TaskSummary) int { return 0 }), nil
		}
		week := now.AddDate(0, 0, 7).Format(time.DateOnly)
		return group(tasks, []string{"期限切れ", "今日", "7日以内", "それ以降", "期日なし"}, func(t store.TaskSummary) int {
			switch {
			case t.DueOn == "":
				return 4
			case t.DueOn < today && !t.Completed:
				return 0
			case t.DueOn == today:
				return 1
			case t.DueOn <= week:
				return 2
			}
			return 3
		}), nil
	}
}

// group buckets tasks (keeping their order) under headers, dropping empty groups.
func group(tasks []store.TaskSummary, headers []string, bucket func(store.TaskSummary) int) []row {
	buckets := make([][]int, len(headers))
	for i, t := range tasks {
		b := bucket(t)
		buckets[b] = append(buckets[b], i)
	}
	var rows []row
	for b, idx := range buckets {
		if len(idx) == 0 {
			continue
		}
		rows = append(rows, row{header: headers[b]})
		for _, i := range idx {
			rows = append(rows, row{task: &tasks[i]})
		}
	}
	return rows
}

func boolPtr(b bool) *bool { return &b }
