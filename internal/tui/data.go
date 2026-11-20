package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// queryRows loads the tasks for the list pane and groups them:
//   - search: one group with all hits (across every project)
//   - My Tasks: by due date (期限切れ / 今日 / 7日以内 / それ以降 / 期日なし)
//   - project: by section, in Asana's section order
//
// found are the tasks Asana found for query (remotesearch.go): they are listed after the
// cache's matches, when they pass the filter.
func queryRows(ctx context.Context, st *app.UseCases, sc scope, query string, filter filterMode, tag string, field fieldFilter, now time.Time, found []string) ([]row, error) {
	f := readmodel.TaskFilter{Query: query, Tag: tag, Field: field.field, FieldValue: field.value}
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
		tasks, err := st.Query().ListTasks(ctx, f)
		if err != nil {
			return nil, err
		}
		label := fmt.Sprintf("検索結果 (%d)", len(tasks))
		if found != nil {
			if tasks, err = app.WithFound(ctx, st.Query(), f, tasks, found); err != nil {
				return nil, err
			}
			label = fmt.Sprintf("検索結果 (%d・Asana も)", len(tasks))
		}
		return group(tasks, []string{label}, func(readmodel.TaskSummary) int { return 0 }), nil
	}

	switch sc.kind {
	case scopeInbox:
		return inboxRows(ctx, st, now, sc.notice)
	case scopeRecent:
		f.ModifiedAfter = app.RecentSince(now)
		f.SortByModified = true
		if filter == filterOpen {
			f.Completed = nil // recent changes include completions
		}
		tasks, err := st.Query().ListTasks(ctx, f)
		if err != nil {
			return nil, err
		}
		var labels []string
		index := map[string]int{}
		for _, t := range tasks {
			l := modifiedDay(t.ModifiedAt, now)
			if _, ok := index[l]; !ok {
				index[l] = len(labels)
				labels = append(labels, l)
			}
		}
		rows := groupKeyed(tasks, labels, labels, func(t readmodel.TaskSummary) int { return index[modifiedDay(t.ModifiedAt, now)] })
		if sc.notice != "" { // why this is shown instead of the inbox
			rows = append([]row{{header: sc.notice, note: true}}, rows...)
		}
		return rows, nil
	case scopeProject:
		f.ProjectGID = sc.project
		tasks, err := st.Query().ListTasks(ctx, f)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(sc.sections)+1)
		keys := make([]string, 0, len(sc.sections)+1)
		index := map[string]int{}
		for i, s := range sc.sections {
			names = append(names, s.Name)
			keys = append(keys, s.GID)
			index[s.GID] = i
		}
		other := len(names)
		names = append(names, "その他")
		keys = append(keys, "other")
		// Empty sections are shown too (as in Asana), so they can be renamed, moved or deleted.
		rows := groupKeyedKeep(tasks, names, keys, func(t readmodel.TaskSummary) int {
			for _, m := range t.Memberships {
				if m.ProjectGID == sc.project {
					if i, ok := index[m.SectionGID]; ok {
						return i
					}
				}
			}
			return other
		}, func(b int) bool { return b != other })
		if !sc.fetched { // only the tasks cached otherwise (e.g. mine) are listed
			rows = append([]row{{header: unfetchedNote, note: true}}, rows...)
		}
		return rows, nil

	default: // My Tasks
		me, err := st.Query().MeGID(ctx)
		if err != nil {
			return nil, err
		}
		f.AssigneeGID = me
		tasks, err := st.Query().ListTasks(ctx, f)
		if err != nil {
			return nil, err
		}
		if filter == filterCompleted {
			return group(tasks, []string{"完了済み"}, func(readmodel.TaskSummary) int { return 0 }), nil
		}
		// By the sections of My Tasks when they are known (as in Asana), else by due date.
		if secs, err := st.Sections(func() time.Time { return now }).MySections(ctx, ""); err == nil && len(secs) > 0 {
			return groupByMySection(tasks, secs), nil
		}
		week := now.AddDate(0, 0, 7).Format(time.DateOnly)
		return group(tasks, []string{"期限切れ", "今日", "7日以内", "それ以降", "期日なし"}, func(t readmodel.TaskSummary) int {
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
func group(tasks []readmodel.TaskSummary, headers []string, bucket func(readmodel.TaskSummary) int) []row {
	return groupKeyed(tasks, headers, headers, bucket)
}

// groupKeyed is group with a stable key per header (section gid), used to remember
// which groups are collapsed.
func groupKeyed(tasks []readmodel.TaskSummary, headers, keys []string, bucket func(readmodel.TaskSummary) int) []row {
	return groupKeyedKeep(tasks, headers, keys, bucket, func(int) bool { return false })
}

// groupKeyedKeep is groupKeyed that keeps the empty groups keep says to keep.
func groupKeyedKeep(tasks []readmodel.TaskSummary, headers, keys []string, bucket func(readmodel.TaskSummary) int, keep func(b int) bool) []row {
	buckets := make([][]int, len(headers))
	for i, t := range tasks {
		b := bucket(t)
		buckets[b] = append(buckets[b], i)
	}
	var rows []row
	for b, idx := range buckets {
		if len(idx) == 0 && !keep(b) {
			continue
		}
		rows = append(rows, row{header: headers[b], key: keys[b], count: len(idx), empty: len(idx) == 0})
		for _, i := range idx {
			rows = append(rows, row{task: &tasks[i]})
		}
	}
	return rows
}

func boolPtr(b bool) *bool { return &b }

// modifiedDay labels a modification time: 今日 / 昨日 / 11/10(火).
func modifiedDay(modifiedAt string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, modifiedAt)
	if err != nil {
		return "日時不明"
	}
	t = t.In(now.Location())
	switch d := t.Format(time.DateOnly); d {
	case now.Format(time.DateOnly):
		return "今日"
	case now.AddDate(0, 0, -1).Format(time.DateOnly):
		return "昨日"
	}
	return t.Format("1/2") + "(" + weekdays[t.Weekday()] + ")"
}

// groupByMySection groups my tasks by their section of My Tasks, in the list's order;
// tasks whose section is not known go last.
func groupByMySection(tasks []readmodel.TaskSummary, secs []readmodel.SectionView) []row {
	names := make([]string, 0, len(secs)+1)
	keys := make([]string, 0, len(secs)+1)
	index := map[string]int{}
	for i, s := range secs {
		names = append(names, s.Name)
		keys = append(keys, "my:"+s.GID)
		index[s.GID] = i
	}
	other := len(names)
	names = append(names, "セクション不明")
	keys = append(keys, "my:other")
	return groupKeyed(tasks, names, keys, func(t readmodel.TaskSummary) int {
		if t.MySection != nil {
			if i, ok := index[t.MySection.GID]; ok {
				return i
			}
		}
		return other
	})
}
