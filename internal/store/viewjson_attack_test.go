package store

import (
	"encoding/json"
	"testing"
)

// The JSON the CLI and MCP print for the views, as it was on main before the views moved to
// readmodel (zero values: the keys, their order and null versus []).
func TestViewJSONUnchanged(t *testing.T) {
	for name, tc := range map[string]struct {
		v    any
		want string
	}{
		"Stats":          {Stats{}, `{"workspaces":0,"projects":0,"sections":0,"tasks":0,"open_tasks":0}`},
		"TaskSummary":    {TaskSummary{}, `{"gid":"","name":"","resource_subtype":"","completed":false,"assignee":null,"memberships":null,"tags":null,"num_subtasks":0,"permalink_url":"","modified_at":"","pending":false,"liked":false,"num_likes":0}`},
		"StoryView":      {StoryView{}, `{"gid":"","type":"","resource_subtype":"","text":"","created_by":null,"created_at":"","pending":false}`},
		"TaskRef":        {TaskRef{}, `{"gid":"","name":""}`},
		"TagCount":       {TagCount{}, `{"gid":"","name":"","open_tasks":0,"tasks":0}`},
		"FieldOption":    {FieldOption{}, `{"field_gid":"","field_name":"","option":"","tasks":0}`},
		"ProjectView":    {ProjectView{}, `{"gid":"","name":"","workspace_gid":"","is_member":false,"archived":false,"permalink_url":"","sections":null}`},
		"SectionView":    {SectionView{}, `{"gid":"","name":""}`},
		"TaskDetail":     {TaskDetail{}, `{"gid":"","name":"","resource_subtype":"","completed":false,"assignee":null,"memberships":null,"tags":null,"num_subtasks":0,"permalink_url":"","modified_at":"","pending":false,"liked":false,"num_likes":0,"notes":"","created_at":"","parent":null,"subtasks":null,"stories":null,"attachments":null,"followers":null,"dependencies":null,"dependents":null}`},
		"MembershipView": {MembershipView{}, `{"project_gid":"","project_name":""}`},
		"AttachmentView": {AttachmentView{}, `{"gid":"","name":"","host":"","size":0,"created_at":"","is_image":false}`},
		"DependencyView": {DependencyView{}, `{"gid":"","name":"","completed":false}`},
		"StatusUpdate":   {StatusUpdateView{}, `{"gid":"","status_type":"","title":"","text":"","author":"","created_at":"","pending":false}`},
		"TaskFilter":     {TaskFilter{}, `{"WorkspaceGID":"","AssigneeGID":"","ProjectGID":"","SectionGID":"","ParentGID":"","Completed":null,"DueBefore":"","DueAfter":"","ModifiedAfter":"","SortByModified":false,"Tag":"","Field":"","FieldValue":"","Query":"","Limit":0}`},
	} {
		b, err := json.Marshal(tc.v)
		if err != nil || string(b) != tc.want {
			t.Errorf("%s: %s (%v), want %s", name, b, err, tc.want)
		}
	}
}
