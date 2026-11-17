package readmodel

import (
	"encoding/json"
	"testing"
	"time"
)

// The views are the JSON the CLI and the MCP tools return: their keys, what is left out
// when empty, and how nested views are written stay as they were in the store.
func TestViewsJSON(t *testing.T) {
	last := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	no := false
	user := &UserRef{GID: "u1", Name: "花子"}
	member := MembershipView{ProjectGID: "p1", ProjectName: "仕事", SectionGID: "s1", SectionName: "今週"}
	field := CustomFieldView{GID: "f1", Name: "優先度", Type: "enum", Value: "o1", Display: "高", Editable: true}
	summary := TaskSummary{
		GID: "t1", Name: "牛乳", ResourceSubtype: "default_task", Completed: true, CompletedAt: "2026-01-02T00:00:00Z",
		DueOn: "2026-01-03", DueAt: "2026-01-03T09:00:00Z", StartOn: "2026-01-01", Assignee: user,
		Memberships: []MembershipView{member}, Tags: []string{"急ぎ"}, ParentGID: "t0", ParentName: "買い物",
		NumSubtasks: 2, PermalinkURL: "https://app.asana.com/0/0/t1", ModifiedAt: "2026-01-02T01:00:00Z",
		Pending: true, Liked: true, NumLikes: 3, ApprovalStatus: "pending", OpenDependencies: 1,
		CustomFields: []CustomFieldView{field},
	}
	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{"membership empty", MembershipView{}, `{"project_gid":"","project_name":""}`},
		{"membership", member, `{"project_gid":"p1","project_name":"仕事","section_gid":"s1","section_name":"今週"}`},
		{"summary empty", TaskSummary{},
			`{"gid":"","name":"","resource_subtype":"","completed":false,"assignee":null,"memberships":null,"tags":null,"num_subtasks":0,"permalink_url":"","modified_at":"","pending":false,"liked":false,"num_likes":0}`},
		{"summary", summary,
			`{"gid":"t1","name":"牛乳","resource_subtype":"default_task","completed":true,"completed_at":"2026-01-02T00:00:00Z","due_on":"2026-01-03","due_at":"2026-01-03T09:00:00Z","start_on":"2026-01-01","assignee":{"gid":"u1","name":"花子"},"memberships":[{"project_gid":"p1","project_name":"仕事","section_gid":"s1","section_name":"今週"}],"tags":["急ぎ"],"parent_gid":"t0","parent_name":"買い物","num_subtasks":2,"permalink_url":"https://app.asana.com/0/0/t1","modified_at":"2026-01-02T01:00:00Z","pending":true,"liked":true,"num_likes":3,"approval_status":"pending","open_dependencies":1,"custom_fields":[{"gid":"f1","name":"優先度","type":"enum","value":"o1","display":"高","editable":true}]}`},
		{"story empty", StoryView{}, `{"gid":"","type":"","resource_subtype":"","text":"","created_by":null,"created_at":"","pending":false}`},
		{"story", StoryView{GID: "c1", Type: "comment", ResourceSubtype: "comment_added", Text: "了解", CreatedBy: user, CreatedAt: "2026-01-02T00:00:00Z", Pending: true, HTMLText: "<body>了解</body>"},
			`{"gid":"c1","type":"comment","resource_subtype":"comment_added","text":"了解","created_by":{"gid":"u1","name":"花子"},"created_at":"2026-01-02T00:00:00Z","pending":true,"html_text":"\u003cbody\u003e了解\u003c/body\u003e"}`},
		{"detail empty", TaskDetail{},
			`{"gid":"","name":"","resource_subtype":"","completed":false,"assignee":null,"memberships":null,"tags":null,"num_subtasks":0,"permalink_url":"","modified_at":"","pending":false,"liked":false,"num_likes":0,"notes":"","created_at":"","parent":null,"subtasks":null,"stories":null,"attachments":null,"followers":null,"dependencies":null,"dependents":null}`},
		{"detail", TaskDetail{
			TaskSummary: TaskSummary{GID: "t1", Name: "牛乳"}, Notes: "説明", HTMLNotes: "<body>説明</body>", CreatedAt: "2026-01-01T00:00:00Z",
			Parent: &TaskRef{GID: "t0", Name: "買い物"}, Subtasks: []TaskSummary{}, Stories: []StoryView{},
			Attachments: []AttachmentView{{GID: "a1", Name: "a.png", Host: "asana", Size: 10, IsImage: true}},
			Followers: []UserRef{*user}, Dependencies: []DependencyView{{GID: "t2", Name: "先"}}, Dependents: []DependencyView{},
			DetailsFetchedAt: "2026-01-02T00:00:00Z",
		},
			`{"gid":"t1","name":"牛乳","resource_subtype":"","completed":false,"assignee":null,"memberships":null,"tags":null,"num_subtasks":0,"permalink_url":"","modified_at":"","pending":false,"liked":false,"num_likes":0,"notes":"説明","html_notes":"\u003cbody\u003e説明\u003c/body\u003e","created_at":"2026-01-01T00:00:00Z","parent":{"gid":"t0","name":"買い物"},"subtasks":[],"stories":[],"attachments":[{"gid":"a1","name":"a.png","host":"asana","size":10,"created_at":"","is_image":true}],"followers":[{"gid":"u1","name":"花子"}],"dependencies":[{"gid":"t2","name":"先","completed":false}],"dependents":[],"details_fetched_at":"2026-01-02T00:00:00Z"}`},
		{"task ref", TaskRef{GID: "t1", Name: "牛乳"}, `{"gid":"t1","name":"牛乳"}`},
		{"tag count", TagCount{GID: "g1", Name: "急ぎ", Open: 2, Total: 5}, `{"gid":"g1","name":"急ぎ","open_tasks":2,"tasks":5}`},
		{"attachment empty", AttachmentView{}, `{"gid":"","name":"","host":"","size":0,"created_at":"","is_image":false}`},
		{"attachment", AttachmentView{GID: "a1", Name: "a.pdf", Host: "asana", ViewURL: "https://x/a", Size: 3, CreatedAt: "2026-01-01T00:00:00Z", LocalPath: "/cache/a.pdf"},
			`{"gid":"a1","name":"a.pdf","host":"asana","view_url":"https://x/a","size":3,"created_at":"2026-01-01T00:00:00Z","is_image":false,"local_path":"/cache/a.pdf"}`},
		{"dependency empty", DependencyView{}, `{"gid":"","name":"","completed":false}`},
		{"dependency", DependencyView{GID: "t2", Name: "先", Completed: true, ResourceSubtype: "milestone", Pending: true},
			`{"gid":"t2","name":"先","completed":true,"resource_subtype":"milestone","pending":true}`},
		{"field option empty", FieldOption{}, `{"field_gid":"","field_name":"","option":"","tasks":0}`},
		{"field option", FieldOption{FieldGID: "f1", FieldName: "優先度", Option: "高", Color: "red", Tasks: 4},
			`{"field_gid":"f1","field_name":"優先度","option":"高","color":"red","tasks":4}`},
		{"section", SectionView{GID: "s1", Name: "今週"}, `{"gid":"s1","name":"今週"}`},
		{"project empty", ProjectView{}, `{"gid":"","name":"","workspace_gid":"","is_member":false,"archived":false,"permalink_url":"","sections":null}`},
		{"project", ProjectView{GID: "p1", Name: "仕事", WorkspaceGID: "w1", TeamName: "開発", IsMember: true, Archived: true, PermalinkURL: "https://x/p1", Sections: []SectionView{{GID: "s1", Name: "今週"}}, Status: "on_track"},
			`{"gid":"p1","name":"仕事","workspace_gid":"w1","team_name":"開発","is_member":true,"archived":true,"permalink_url":"https://x/p1","sections":[{"gid":"s1","name":"今週"}],"status":"on_track"}`},
		{"status update empty", StatusUpdateView{}, `{"gid":"","status_type":"","title":"","text":"","author":"","created_at":"","pending":false}`},
		{"status update", StatusUpdateView{GID: "su1", StatusType: "on_track", Title: "週報", Text: "順調", HTMLText: "<body>順調</body>", Author: "花子", CreatedAt: "2026-01-02T00:00:00Z", Pending: true},
			`{"gid":"su1","status_type":"on_track","title":"週報","text":"順調","html_text":"\u003cbody\u003e順調\u003c/body\u003e","author":"花子","created_at":"2026-01-02T00:00:00Z","pending":true}`},
		{"stats empty", Stats{}, `{"workspaces":0,"projects":0,"sections":0,"tasks":0,"open_tasks":0}`},
		{"stats", Stats{Workspaces: 1, Projects: 2, Sections: 3, Tasks: 4, OpenTasks: 5, LastSync: &last},
			`{"workspaces":1,"projects":2,"sections":3,"tasks":4,"open_tasks":5,"last_sync":"2026-01-02T03:04:05Z"}`},
		// TaskFilter is not shown, but its fields are the query: none has a JSON name
		{"task filter", TaskFilter{WorkspaceGID: "w1", Completed: &no, Limit: 5},
			`{"WorkspaceGID":"w1","AssigneeGID":"","ProjectGID":"","SectionGID":"","ParentGID":"","Completed":false,"DueBefore":"","DueAfter":"","ModifiedAfter":"","SortByModified":false,"Tag":"","Field":"","FieldValue":"","Query":"","Limit":5}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}
