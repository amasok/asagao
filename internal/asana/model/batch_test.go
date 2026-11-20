package model

import (
	"reflect"
	"testing"
)

func TestBatchWriteBuilders(t *testing.T) {
	fields := map[string]any{"completed": true, "name": "Renamed"}
	cases := []struct {
		name string
		got  BatchWrite
		want BatchWrite
	}{
		{
			name: "BatchUpdateTask",
			got:  BatchUpdateTask("t1", fields),
			want: BatchWrite{Kind: BatchKindUpdateTask, TaskGID: "t1", Fields: fields},
		},
		{
			name: "BatchMoveTaskToSection",
			got:  BatchMoveTaskToSection("s1", "t1"),
			want: BatchWrite{Kind: BatchKindMoveTask, TaskGID: "t1", SectionGID: "s1"},
		},
		{
			name: "BatchAddTag",
			got:  BatchAddTag("t1", "tag1"),
			want: BatchWrite{Kind: BatchKindAddTag, TaskGID: "t1", TagGID: "tag1"},
		},
		{
			name: "BatchRemoveTag",
			got:  BatchRemoveTag("t1", "tag1"),
			want: BatchWrite{Kind: BatchKindRemoveTag, TaskGID: "t1", TagGID: "tag1"},
		},
		{
			name: "BatchAddFollower",
			got:  BatchAddFollower("t1", "u1"),
			want: BatchWrite{Kind: BatchKindAddFollower, TaskGID: "t1", UserGID: "u1"},
		},
		{
			name: "BatchRemoveFollower",
			got:  BatchRemoveFollower("t1", "u1"),
			want: BatchWrite{Kind: BatchKindRemoveFollower, TaskGID: "t1", UserGID: "u1"},
		},
		{
			name: "BatchAddProject without section",
			got:  BatchAddProject("t1", "p1", ""),
			want: BatchWrite{Kind: BatchKindAddProject, TaskGID: "t1", ProjectGID: "p1"},
		},
		{
			name: "BatchAddProject with section",
			got:  BatchAddProject("t1", "p1", "s1"),
			want: BatchWrite{Kind: BatchKindAddProject, TaskGID: "t1", ProjectGID: "p1", SectionGID: "s1"},
		},
		{
			name: "BatchRemoveProject",
			got:  BatchRemoveProject("t1", "p1"),
			want: BatchWrite{Kind: BatchKindRemoveProject, TaskGID: "t1", ProjectGID: "p1"},
		},
		{
			name: "BatchAddDependency",
			got:  BatchAddDependency("t1", "d1"),
			want: BatchWrite{Kind: BatchKindAddDependency, TaskGID: "t1", DepGID: "d1"},
		},
		{
			name: "BatchRemoveDependency",
			got:  BatchRemoveDependency("t1", "d1"),
			want: BatchWrite{Kind: BatchKindRemoveDependency, TaskGID: "t1", DepGID: "d1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("got %#v, want %#v", tc.got, tc.want)
			}
		})
	}
}
