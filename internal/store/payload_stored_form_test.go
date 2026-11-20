package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// The payloads the store queues are kept in the outbox as they were written before they
// were built from the structs of domain/outbox (#89): each test writes through the store's
// public writes, reads the payload column as stored (raw SQL), and compares it with the
// JSON the map literals wrote. A key that is absent, "" and null are told apart.

// storedPayloads returns the payload column of the entries of kind, oldest first.
func storedPayloads(t *testing.T, s *Store, kind Kind) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "SELECT payload FROM outbox WHERE kind = ? ORDER BY id", kind)
	must(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		must(t, rows.Scan(&p))
		out = append(out, p)
	}
	must(t, rows.Err())
	return out
}

// wantStored checks that the entries of kind hold exactly the payloads want (JSON), in
// order, whatever the order of their keys.
func wantStored(t *testing.T, s *Store, kind Kind, want ...string) {
	t.Helper()
	got := storedPayloads(t, s, kind)
	if len(got) != len(want) {
		t.Fatalf("%s: %d entries %q, want %d %q", kind, len(got), got, len(want), want)
	}
	for i := range want {
		var g, w any
		if err := json.Unmarshal([]byte(got[i]), &g); err != nil {
			t.Fatalf("%s #%d: stored %q: %v", kind, i, got[i], err)
		}
		if err := json.Unmarshal([]byte(want[i]), &w); err != nil {
			t.Fatalf("%s #%d: want %q: %v", kind, i, want[i], err)
		}
		if !reflect.DeepEqual(g, w) {
			t.Errorf("%s #%d:\n stored %s\n   want %s", kind, i, got[i], want[i])
		}
	}
}

// js quotes s as a JSON string.
func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func storedOpID(t *testing.T, s *Store, kind Kind) int64 {
	t.Helper()
	var id int64
	must(t, s.db.QueryRowContext(context.Background(), "SELECT id FROM outbox WHERE kind = ? ORDER BY id DESC LIMIT 1", kind).Scan(&id))
	return id
}

var storedAuthor = UserRef{GID: "me", Name: "Me"}

func TestStoredPayloadUpdateTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "新しい名前", "due_on": ""}, testNow))
	wantStored(t, s, OpUpdateTask, `{"name":"新しい名前","due_on":""}`)
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"completed": true, "assignee": ""}, testNow))
	wantStored(t, s, OpUpdateTask, `{"name":"新しい名前","due_on":"","completed":true,"assignee":""}`)
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ソファを買う"}, testNow)) // back: not sent
	wantStored(t, s, OpUpdateTask, `{"due_on":"","completed":true,"assignee":""}`)
}

func TestStoredPayloadCreateTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.CreateTaskLocal(ctx, NewTask{WorkspaceGID: "w1", Name: "最小"}, testNow)
	must(t, err)
	_, err = s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "全部", Notes: "説明",
		HTMLNotes: "<body>説明</body>", DueOn: "2026-12-01", StartOn: "2026-11-30", AssigneeGID: "me"}, testNow)
	must(t, err)
	_, err = s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", Name: "セクションなし"}, testNow)
	must(t, err)
	_, err = s.CreateTaskLocal(ctx, NewTask{ParentGID: "t1", ProjectGID: "p1", SectionGID: "s1", Name: "サブ"}, testNow)
	must(t, err)
	wantStored(t, s, OpCreateTask,
		`{"workspace":"w1","name":"最小"}`,
		`{"workspace":"w1","name":"全部","project":"p1","section":"s1","notes":"説明","html_notes":"<body>説明</body>",
			"due_on":"2026-12-01","start_on":"2026-11-30","assignee":"me"}`,
		`{"workspace":"w1","name":"セクションなし","project":"p1"}`,
		`{"workspace":"w1","name":"サブ","parent":"t1"}`)
}

// Edits of a task not sent yet are folded into its create (key by key), and so is a
// move to another section of its project.
func TestStoredPayloadCreateTaskEdited(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	gid, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "前", DueOn: "2026-12-01"}, testNow)
	must(t, err)
	must(t, s.UpdateTaskLocal(ctx, gid, map[string]any{"name": "後", "completed": true, "due_on": ""}, testNow))
	wantStored(t, s, OpCreateTask, `{"workspace":"w1","name":"後","project":"p1","section":"s1","due_on":"","completed":true}`)
	must(t, s.MoveTaskLocal(ctx, gid, "p1", "s2", testNow))
	wantStored(t, s, OpCreateTask, `{"workspace":"w1","name":"後","project":"p1","section":"s2","due_on":"","completed":true}`)
	if n := len(storedPayloads(t, s, OpMoveTask)) + len(storedPayloads(t, s, OpUpdateTask)); n != 0 {
		t.Errorf("%d other entries for a task not sent", n)
	}

	// created without a section: the move adds it
	g2, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", Name: "二つ目"}, testNow)
	must(t, err)
	must(t, s.MoveTaskLocal(ctx, g2, "p1", "s2", testNow))
	got := storedPayloads(t, s, OpCreateTask)
	if len(got) != 2 {
		t.Fatalf("creates %q", got)
	}
	wantStored(t, s, OpCreateTask, `{"workspace":"w1","name":"後","project":"p1","section":"s2","due_on":"","completed":true}`,
		`{"workspace":"w1","name":"二つ目","project":"p1","section":"s2"}`)
}

// A custom field value of a task not sent yet goes with its create (custom_fields); with
// none left, the key goes.
func TestStoredPayloadCreateTaskCustomFields(t *testing.T) {
	ctx := context.Background()
	s := setupFields(t)
	gid, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "新しい"}, testNow)
	must(t, err)
	_, err = s.SetCustomFieldLocal(ctx, gid, "cf1", "o1", testNow)
	must(t, err)
	_, err = s.SetCustomFieldLocal(ctx, gid, "cf2", 3.5, testNow)
	must(t, err)
	wantStored(t, s, OpCreateTask, `{"workspace":"w1","name":"新しい","project":"p1","section":"s1","custom_fields":{"cf1":"o1","cf2":3.5}}`)
	_, err = s.SetCustomFieldLocal(ctx, gid, "cf1", nil, testNow)
	must(t, err)
	wantStored(t, s, OpCreateTask, `{"workspace":"w1","name":"新しい","project":"p1","section":"s1","custom_fields":{"cf2":3.5}}`)
	_, err = s.SetCustomFieldLocal(ctx, gid, "cf2", nil, testNow)
	must(t, err)
	wantStored(t, s, OpCreateTask, `{"workspace":"w1","name":"新しい","project":"p1","section":"s1"}`)
	if got := storedPayloads(t, s, OpSetCustomField); len(got) != 0 {
		t.Errorf("set_custom_field entries for a task not sent: %q", got)
	}
}

func TestStoredPayloadMoveTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "手続き"}, {GID: "s3", Name: "空き"}}))
	must(t, s.MoveTaskLocal(ctx, "t2", "p1", "s1", testNow))
	wantStored(t, s, OpMoveTask, `{"project":"p1","section":"s1","section_name":"家具"}`)
}

func TestStoredPayloadMoveTaskPosition(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "手続き"}, {GID: "s3", Name: "空き"}}))
	must(t, s.MoveTaskAtLocal(ctx, "t1", "p1", "s2", Position{Before: "t3"}, testNow))
	wantStored(t, s, OpMoveTask, `{"project":"p1","section":"s2","section_name":"手続き","insert_before":"t3"}`)
	must(t, s.MoveTaskAtLocal(ctx, "t1", "p1", "s2", Position{After: "t2"}, testNow))
	wantStored(t, s, OpMoveTask, `{"project":"p1","section":"s2","section_name":"手続き","insert_after":"t2"}`)
	must(t, s.MoveTaskLocal(ctx, "t1", "p1", "s3", testNow)) // the last move says where: no place
	wantStored(t, s, OpMoveTask, `{"project":"p1","section":"s3","section_name":"空き"}`)
}

func TestStoredPayloadDeleteTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.DeleteTaskLocal(ctx, "t3", testNow)
	must(t, err)
	wantStored(t, s, OpDeleteTask, `{"name":"電気の契約"}`)
}

func TestStoredPayloadSetParent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.SetParentLocal(ctx, "t2", "t1", Position{}, testNow))
	must(t, s.SetParentLocal(ctx, "t3", "t1", Position{Before: "t1a"}, testNow))
	must(t, s.SetParentLocal(ctx, "t4", "t1", Position{After: "t1a"}, testNow))
	must(t, s.SetParentLocal(ctx, "t1a", "", Position{}, testNow))
	wantStored(t, s, OpSetParent,
		`{"parent":"t1","parent_name":"ソファを買う"}`,
		`{"parent":"t1","parent_name":"ソファを買う","insert_before":"t1a"}`,
		`{"parent":"t1","parent_name":"ソファを買う","insert_after":"t1a"}`,
		`{"parent":"","parent_name":""}`)
	// moved again without a place: the place goes
	must(t, s.SetParentLocal(ctx, "t3", "t2", Position{}, testNow))
	wantStored(t, s, OpSetParent,
		`{"parent":"t1","parent_name":"ソファを買う"}`,
		`{"parent":"t2","parent_name":"転居届を出す"}`,
		`{"parent":"t1","parent_name":"ソファを買う","insert_after":"t1a"}`,
		`{"parent":"","parent_name":""}`)
}

func TestStoredPayloadComments(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	plain, err := s.AddCommentLocal(ctx, "t1", "ただの文", storedAuthor, testNow)
	must(t, err)
	rich, err := s.AddRichCommentLocal(ctx, "t1", "太字", "<body><strong>太字</strong></body>", storedAuthor, testNow)
	must(t, err)
	wantStored(t, s, OpAddComment,
		`{"text":"ただの文","story_gid":`+js(plain)+`}`,
		`{"text":"太字","html_text":"<body><strong>太字</strong></body>","story_gid":`+js(rich)+`}`)

	// edited before it is sent: the create sends the new text (html_text only when rich)
	must(t, s.EditCommentLocal(ctx, plain, "直した", "<body>直した</body>", testNow))
	must(t, s.EditCommentLocal(ctx, rich, "平文に", "", testNow))
	wantStored(t, s, OpAddComment,
		`{"text":"直した","html_text":"<body>直した</body>","story_gid":`+js(plain)+`}`,
		`{"text":"平文に","story_gid":`+js(rich)+`}`)
	if got := storedPayloads(t, s, OpUpdateStory); len(got) != 0 {
		t.Errorf("update_story for comments not sent: %q", got)
	}
}

func TestStoredPayloadStories(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedComments(t, s)
	must(t, s.EditCommentLocal(ctx, "c-me", "直した", "", testNow))
	wantStored(t, s, OpUpdateStory, `{"story":"c-me","text":"直した","html_text":""}`)
	must(t, s.EditCommentLocal(ctx, "c-me", "もう一度", "<body>もう一度</body>", testNow))
	wantStored(t, s, OpUpdateStory, `{"story":"c-me","text":"もう一度","html_text":"<body>もう一度</body>"}`)

	s2 := newTestStore(t)
	seedComments(t, s2)
	must(t, s2.DeleteCommentLocal(ctx, "c-me", testNow))
	wantStored(t, s2, OpDeleteStory, `{"story":"c-me","text":"元の文"}`)
}

func TestStoredPayloadAttachments(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.ReplaceAttachments(ctx, "t1", []asana.Attachment{{GID: "a1", Name: "図面.pdf", Host: "asana"}}))
	must(t, s.DeleteAttachmentLocal(ctx, "a1", testNow))

	cache := t.TempDir()
	s.SetAttachmentDir(cache)
	src := filepath.Join(t.TempDir(), "メモ.txt")
	must(t, os.WriteFile(src, []byte("x"), 0o600))
	local, err := s.UploadAttachmentLocal(ctx, "t1", src, cache, testNow)
	must(t, err)
	a, err := s.GetAttachment(ctx, local)
	must(t, err)
	if a.LocalPath == "" {
		t.Fatal("no local copy")
	}
	wantStored(t, s, OpUploadAttachment, `{"attachment":`+js(local)+`,"name":"メモ.txt","path":`+js(a.LocalPath)+`}`)

	// the upload is being sent: the delete keeps the copy it may be sent again from (path)
	_, err = s.db.ExecContext(ctx, "UPDATE outbox SET attempts = 1 WHERE kind = ?", OpUploadAttachment)
	must(t, err)
	must(t, s.DeleteAttachmentLocal(ctx, local, testNow))
	wantStored(t, s, OpDeleteAttachment,
		`{"attachment":"a1","name":"図面.pdf"}`,
		`{"attachment":`+js(local)+`,"name":"メモ.txt","path":`+js(a.LocalPath)+`}`)
	wantStored(t, s, OpUploadAttachment, `{"attachment":`+js(local)+`,"name":"メモ.txt","path":`+js(a.LocalPath)+`}`)
}

func TestStoredPayloadTimeEntries(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedTimeEntries(t, s)
	local, err := s.AddTimeEntryLocal(ctx, "t1", 30, "2026-11-14", storedAuthor, testNow)
	must(t, err)
	wantStored(t, s, OpAddTimeEntry, `{"entry":`+js(local)+`,"duration_minutes":30,"entered_on":"2026-11-14"}`)
	must(t, s.UpdateTimeEntryLocal(ctx, local, 45, "2026-11-15", testNow)) // not sent yet: the add sends it
	wantStored(t, s, OpAddTimeEntry, `{"entry":`+js(local)+`,"duration_minutes":45,"entered_on":"2026-11-15"}`)

	must(t, s.UpdateTimeEntryLocal(ctx, "e1", 40, "2026-11-12", testNow))
	wantStored(t, s, OpUpdateTimeEntry, `{"entry":"e1","duration_minutes":40,"entered_on":"2026-11-12"}`)
	must(t, s.UpdateTimeEntryLocal(ctx, "e1", 50, "2026-11-11", testNow))
	wantStored(t, s, OpUpdateTimeEntry, `{"entry":"e1","duration_minutes":50,"entered_on":"2026-11-11"}`)

	s2 := newTestStore(t)
	seedTimeEntries(t, s2)
	must(t, s2.DeleteTimeEntryLocal(ctx, "e1", testNow))
	wantStored(t, s2, OpDeleteTimeEntry, `{"entry":"e1","duration_minutes":30,"entered_on":"2026-11-12"}`)
}

func TestStoredPayloadSections(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.RenameSectionLocal(ctx, "s1", "家具2", testNow))
	must(t, s.RenameSectionLocal(ctx, "s1", "家具3", testNow))
	wantStored(t, s, OpRenameSection, `{"name":"家具3"}`)

	_, err := s.CreateSectionLocal(ctx, "p1", "最後", "", "", testNow)
	must(t, err)
	_, err = s.CreateSectionLocal(ctx, "p1", "前", "s1", "", testNow)
	must(t, err)
	_, err = s.CreateSectionLocal(ctx, "p1", "後", "", "s2", testNow)
	must(t, err)
	wantStored(t, s, OpCreateSection,
		`{"project":"p1","name":"最後"}`,
		`{"project":"p1","name":"前","before":"s1"}`,
		`{"project":"p1","name":"後","after":"s2"}`)

	// what sending it found out first: the sections that had its name
	ids := []int64{}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM outbox WHERE kind = ? ORDER BY id", OpCreateSection)
	must(t, err)
	for rows.Next() {
		var id int64
		must(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	rows.Close()
	must(t, s.SetSectionsExisting(ctx, ids[0], []string{}))
	must(t, s.SetSectionsExisting(ctx, ids[1], []string{"s1"}))
	wantStored(t, s, OpCreateSection,
		`{"project":"p1","name":"最後","existing":[]}`,
		`{"project":"p1","name":"前","before":"s1","existing":["s1"]}`,
		`{"project":"p1","name":"後","after":"s2"}`)
}

func TestStoredPayloadSectionMovesAndDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "手続き"}, {GID: "s3", Name: "空き"}}))
	must(t, s.MoveSectionLocal(ctx, "s2", "s1", "", testNow))
	must(t, s.MoveSectionLocal(ctx, "s1", "", "s3", testNow))
	wantStored(t, s, OpInsertSection,
		`{"project":"p1","name":"手続き","before":"s1"}`,
		`{"project":"p1","name":"家具","after":"s3"}`)

	s2 := newTestStore(t)
	must(t, s2.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "手続き"}, {GID: "s3", Name: "空き"}}))
	must(t, s2.DeleteSectionLocal(ctx, "s3", testNow))
	wantStored(t, s2, OpDeleteSection, `{"project":"p1","name":"空き","position":2}`)
}

// A section created offline and deleted before it was sent is forgotten: another new
// section placed next to it goes last instead (before / after taken out).
func TestStoredPayloadForgottenSection(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ls, err := s.CreateSectionLocal(ctx, "p1", "消す", "", "", testNow)
	must(t, err)
	_, err = s.CreateSectionLocal(ctx, "p1", "隣", ls, "", testNow)
	must(t, err)
	_, err = s.CreateSectionLocal(ctx, "p1", "後ろ", "", ls, testNow)
	must(t, err)
	must(t, s.DeleteSectionLocal(ctx, ls, testNow))
	wantStored(t, s, OpCreateSection, `{"project":"p1","name":"隣"}`, `{"project":"p1","name":"後ろ"}`)
}

func TestStoredPayloadTags(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.UpdateTagLocal(ctx, "g1", map[string]any{"name": "買い物2"}, testNow))
	wantStored(t, s, OpUpdateTag, `{"name":"買い物2"}`)
	must(t, s.UpdateTagLocal(ctx, "g1", map[string]any{"color": "dark-red"}, testNow))
	wantStored(t, s, OpUpdateTag, `{"name":"買い物2","color":"dark-red"}`)
	must(t, s.UpdateTagLocal(ctx, "g1", map[string]any{"name": "買い物"}, testNow)) // back
	wantStored(t, s, OpUpdateTag, `{"color":"dark-red"}`)

	_, err := s.AddTagLocal(ctx, "t2", "g1", "", testNow)
	must(t, err)
	newTag, err := s.AddTagLocal(ctx, "t3", "", "新しいタグ", testNow)
	must(t, err)
	wantStored(t, s, OpAddTag,
		`{"tag":"g1","tag_name":"買い物","workspace":"w1"}`,
		`{"tag":`+js(newTag)+`,"tag_name":"新しいタグ","workspace":"w1"}`)
	must(t, s.RemoveTagLocal(ctx, "t1", "g1", testNow))
	wantStored(t, s, OpRemoveTag, `{"tag":"g1","tag_name":"買い物"}`)

	s2 := newTestStore(t)
	must(t, s2.DeleteTagLocal(ctx, "g1", testNow))
	wantStored(t, s2, OpDeleteTag, `{"name":"買い物","color":"","workspace":"w1","tasks":["t1"]}`)
}

func TestStoredPayloadRelations(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.SetFollowerLocal(ctx, "t1", "u2", true, testNow))
	must(t, s.SetFollowerLocal(ctx, "t2", "me", false, testNow))
	must(t, s.SetFollowerLocal(ctx, "t2", "nobody", true, testNow))
	wantStored(t, s, OpAddFollower, `{"user":"u2","user_name":"Partner"}`, `{"user":"nobody","user_name":""}`)
	wantStored(t, s, OpRemoveFollower, `{"user":"me","user_name":"Me"}`)

	must(t, s.SetProjectLocal(ctx, "t4", "p1", "s2", true, testNow))
	must(t, s.SetProjectLocal(ctx, "t1", "p2", "", true, testNow)) // p2 has no section
	must(t, s.SetProjectLocal(ctx, "t2", "p1", "", false, testNow))
	must(t, s.SetProjectLocal(ctx, "t4", "p2", "", false, testNow)) // in no section of p2
	wantStored(t, s, OpAddProject,
		`{"project":"p1","project_name":"[家族]新居プロジェクト","section":"s2"}`,
		`{"project":"p2","project_name":"[家族]事務/雑務","section":""}`)
	wantStored(t, s, OpRemoveProject,
		`{"project":"p1","project_name":"[家族]新居プロジェクト","section":"s2"}`,
		`{"project":"p2","project_name":"[家族]事務/雑務","section":""}`)

	must(t, s.ReplaceDependencies(ctx, "t3", []asana.TaskRef{{GID: "t4", Name: "年末調整"}}, nil))
	must(t, s.SetDependencyLocal(ctx, "t1", "t2", true, testNow))
	must(t, s.SetDependencyLocal(ctx, "t3", "t4", false, testNow))
	wantStored(t, s, OpAddDependency, `{"dependency":"t2","dependency_name":"転居届を出す"}`)
	wantStored(t, s, OpRemoveDependency, `{"dependency":"t4","dependency_name":"年末調整"}`)

	_, err := s.DuplicateTaskLocal(ctx, "t1", "", testNow)
	must(t, err)
	_, err = s.DuplicateTaskLocal(ctx, "t2", "写し", testNow)
	must(t, err)
	wantStored(t, s, OpDuplicateTask, `{"source":"t1","name":"ソファを買う のコピー"}`, `{"source":"t2","name":"写し"}`)
}

func TestStoredPayloadCreateStatus(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	g1, err := s.CreateStatusLocal(ctx, "p1", "on_track", "順調", "本文", "<body>本文</body>", storedAuthor, testNow)
	must(t, err)
	g2, err := s.CreateStatusLocal(ctx, "p1", "at_risk", "", "平文", "", storedAuthor, testNow)
	must(t, err)
	wantStored(t, s, OpCreateStatus,
		`{"status":`+js(g1)+`,"status_type":"on_track","title":"順調","text":"本文","html_text":"<body>本文</body>"}`,
		`{"status":`+js(g2)+`,"status_type":"at_risk","title":"","text":"平文","html_text":""}`)
}

func TestStoredPayloadCustomFields(t *testing.T) {
	ctx := context.Background()
	s := setupFieldOps(t)
	_, err := s.SetCustomFieldLocal(ctx, "t1", "cf2", 3.5, testNow)
	must(t, err)
	_, err = s.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", testNow)
	must(t, err)
	wantStored(t, s, OpSetCustomField,
		`{"field":"cf2","field_name":"見積もり","type":"number","value":3.5,"display":"3.5"}`,
		`{"field":"cf1","field_name":"優先度","type":"enum","value":"o2","display":"中"}`)
	_, err = s.SetCustomFieldLocal(ctx, "t1", "cf2", nil, testNow) // cleared: null
	must(t, err)
	_, err = s.SetCustomFieldLocal(ctx, "t1", "cf4", []string{"m2", "m1"}, testNow)
	must(t, err)
	got := storedPayloads(t, s, OpSetCustomField)
	if len(got) != 3 {
		t.Fatalf("set_custom_field entries %q", got)
	}
	wantStored(t, s, OpSetCustomField,
		`{"field":"cf2","field_name":"見積もり","type":"number","value":null,"display":""}`,
		`{"field":"cf1","field_name":"優先度","type":"enum","value":"o2","display":"中"}`,
		`{"field":"cf4","field_name":"ラベル","type":"multi_enum","value":["m1","m2"],"display":"A, B"}`)

	_, _, err = s.AddEnumOptionLocal(ctx, "cf1", "低", testNow)
	must(t, err)
	wantStored(t, s, OpCreateEnumOption, `{"field":"cf1","field_name":"優先度","name":"低","workspace":"w1"}`)

	must(t, s.SetProjectFieldLocal(ctx, "p1", "cf5", true, testNow))
	must(t, s.SetProjectFieldLocal(ctx, "p1", "cf4", false, testNow))
	wantStored(t, s, OpAddFieldSetting, `{"field":"cf5","field_name":"メモ","workspace":"w1"}`)
	wantStored(t, s, OpRemoveFieldSetting, `{"field":"cf4","field_name":"ラベル","workspace":"w1"}`)
}

func TestStoredPayloadMoveMySection(t *testing.T) {
	ctx := context.Background()
	s := newMyTasksStore(t)
	must(t, s.MoveMySectionLocal(ctx, "mt", "m1", testNow))
	wantStored(t, s, OpMoveMySection, `{"section":"m1","section_name":"今日やる","workspace":"w1"}`)
}

func TestStoredPayloadMarkNotification(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.ReplaceNotifications(ctx, "w1", []asana.Notification{
		{GID: "n1", WorkObjects: []asana.WorkRef{{GID: "t1", ResourceType: "task", Name: "古い名前"}}},
		{GID: "n2", IsRead: true, WorkObjects: []asana.WorkRef{{GID: "x9", ResourceType: "task", Name: "知らないタスク"}}},
	}))
	_, err := s.MarkNotificationsLocal(ctx, []string{"n1"}, true, testNow)
	must(t, err)
	wantStored(t, s, OpMarkNotification, `{"workspace":"w1","name":"ソファを買う","read":true}`)

	// an entry already queued for it takes the new state, its other keys as they were
	_, err = s.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
		VALUES (?, 'n2', '{"workspace":"wX","name":"記録した名前","read":true}', '{}', 'pending', ?)`,
		OpMarkNotification, formatTime(testNow))
	must(t, err)
	_, err = s.MarkNotificationsLocal(ctx, []string{"n2"}, false, testNow)
	must(t, err)
	wantStored(t, s, OpMarkNotification,
		`{"workspace":"w1","name":"ソファを買う","read":true}`,
		`{"workspace":"wX","name":"記録した名前","read":false}`)
}

func TestStoredPayloadUpdateProject(t *testing.T) {
	ctx := context.Background()
	s := projectStore(t)
	must(t, s.UpdateProjectLocal(ctx, "p1", map[string]any{"notes": "新しい説明", "html_notes": "<body>新しい説明</body>"}, testNow))
	wantStored(t, s, OpUpdateProject, `{"notes":"新しい説明","html_notes":"<body>新しい説明</body>"}`)
	must(t, s.UpdateProjectLocal(ctx, "p1", map[string]any{"owner": "", "due_on": "", "color": "dark-red"}, testNow))
	wantStored(t, s, OpUpdateProject,
		`{"notes":"新しい説明","html_notes":"<body>新しい説明</body>","owner":"","due_on":"","color":"dark-red"}`)
	must(t, s.UpdateProjectLocal(ctx, "p1", map[string]any{"start_on": "2026-12-01", "due_on": "2027-01-31"}, testNow))
	wantStored(t, s, OpUpdateProject,
		`{"notes":"新しい説明","html_notes":"<body>新しい説明</body>","owner":"","due_on":"2027-01-31","start_on":"2026-12-01","color":"dark-red"}`)
}

func TestStoredPayloadUpdateProjectBrief(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		brief *asana.ProjectBrief
		edit  BriefEdit
		want  string
	}{
		{"delete", asanaBrief, BriefEdit{Delete: true}, `{"delete":true}`},
		{"new", nil, BriefEdit{Title: "題", HTMLText: "<body>本文</body>", Text: "本文"}, `{"title":"題","html_text":"<body>本文</body>","text":"本文"}`},
		{"new without title", nil, BriefEdit{HTMLText: "<body>本文</body>", Text: "本文"}, `{"title":"","html_text":"<body>本文</body>","text":"本文"}`},
		{"title only", asanaBrief, BriefEdit{Title: "新しい題", HTMLText: asanaBrief.HTMLText, Text: asanaBrief.Text}, `{"title":"新しい題"}`},
		{"title cleared", asanaBrief, BriefEdit{Title: "", HTMLText: asanaBrief.HTMLText, Text: asanaBrief.Text}, `{"title":""}`},
		{"body only", asanaBrief, BriefEdit{Title: asanaBrief.Title, HTMLText: "<body>来年</body>", Text: "来年"}, `{"html_text":"<body>来年</body>","text":"来年"}`},
		{"both", asanaBrief, BriefEdit{Title: "題2", HTMLText: "<body>来年</body>", Text: "来年"}, `{"title":"題2","html_text":"<body>来年</body>","text":"来年"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := briefStore(t, c.brief)
			must(t, s.UpdateProjectBriefLocal(ctx, "p1", c.edit, testNow))
			wantStored(t, s, OpUpdateBrief, c.want)
		})
	}
	// a second edit replaces the payload: what differs from the base now
	s, _ := briefStore(t, asanaBrief)
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: "題2", HTMLText: "<body>来年</body>", Text: "来年"}, testNow))
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Title: asanaBrief.Title, HTMLText: "<body>来年</body>", Text: "来年"}, testNow))
	wantStored(t, s, OpUpdateBrief, `{"html_text":"<body>来年</body>","text":"来年"}`)
}
