package store

import (
	"fmt"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// The store's views are readmodel's (aliases): a value of one is a value of the other, so
// the store's methods and their callers keep their types.
func TestViewsAreReadmodelTypes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		store, rm any
	}{
		{"MembershipView", MembershipView{}, readmodel.MembershipView{}},
		{"TaskSummary", TaskSummary{}, readmodel.TaskSummary{}},
		{"StoryView", StoryView{}, readmodel.StoryView{}},
		{"TaskDetail", TaskDetail{}, readmodel.TaskDetail{}},
		{"TaskRef", TaskRef{}, readmodel.TaskRef{}},
		{"SectionView", SectionView{}, readmodel.SectionView{}},
		{"ProjectView", ProjectView{}, readmodel.ProjectView{}},
		{"TaskFilter", TaskFilter{}, readmodel.TaskFilter{}},
		{"TagCount", TagCount{}, readmodel.TagCount{}},
		{"AttachmentView", AttachmentView{}, readmodel.AttachmentView{}},
		{"DependencyView", DependencyView{}, readmodel.DependencyView{}},
		{"StatusUpdateView", StatusUpdateView{}, readmodel.StatusUpdateView{}},
		{"FieldOption", FieldOption{}, readmodel.FieldOption{}},
		{"Stats", Stats{}, readmodel.Stats{}},
	} {
		if typeName(tc.store) != typeName(tc.rm) {
			t.Errorf("store.%s is %s, want %s", tc.name, typeName(tc.store), typeName(tc.rm))
		}
	}
}

func typeName(v any) string { return fmt.Sprintf("%T", v) }
