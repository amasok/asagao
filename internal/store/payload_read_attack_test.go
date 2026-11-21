package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// Attack tests for reading outbox payloads through their kinds' structs: after a pull,
// the unsent entries are re-applied as they were when the store read them key by key
// (an entry that does not read entirely: as far as it reads; a missing key is not a "").

func reapplyTx(t *testing.T, s *Store, f func(ctx context.Context, tx *sql.Tx) error) {
	t.Helper()
	ctx := context.Background()
	must(t, s.tx(ctx, func(tx *sql.Tx) error { return f(ctx, tx) }))
}

func TestAttackReapplyNotificationMarkWithoutReadValue(t *testing.T) {
	for name, payload := range map[string]string{
		"read missing":    `{"workspace":"w1","name":"a"}`,
		"read is string":  `{"workspace":"w1","name":"a","read":"yes"}`,
		"read is null":    `{"workspace":"w1","name":"a","read":null}`,
		"read is number":  `{"workspace":"w1","name":"a","read":1}`,
		"read string+key": `{"workspace":"w1","name":"a","read":"yes","from_future":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.db.Exec(`INSERT INTO notifications(gid, workspace_gid, is_read) VALUES('n1', 'w1', 1)`); err != nil {
				t.Fatal(err)
			}
			insertRawOp(t, s, OpMarkNotification, "n1", payload, `{}`)
			reapplyTx(t, s, reapplyNotificationMarks)
			var read bool
			must(t, s.db.QueryRow(`SELECT is_read FROM notifications WHERE gid = 'n1'`).Scan(&read))
			if !read {
				t.Errorf("a mark with no readable read flag turned a read notification to unread (main leaves it as it is)")
			}
		})
	}
}

func TestAttackReapplyNotificationMarkLastWins(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO notifications(gid, workspace_gid, is_read) VALUES('n1', 'w1', 0)`); err != nil {
		t.Fatal(err)
	}
	insertRawOp(t, s, OpMarkNotification, "n1", `{"workspace":"w1","name":"a","read":true}`, `{}`)
	// a later, unreadable entry must not undo the earlier readable one
	insertRawOp(t, s, OpMarkNotification, "n1", `{"workspace":"w1","name":"a","read":"x"}`, `{}`)
	reapplyTx(t, s, reapplyNotificationMarks)
	var read bool
	must(t, s.db.QueryRow(`SELECT is_read FROM notifications WHERE gid = 'n1'`).Scan(&read))
	if !read {
		t.Error("an unreadable later mark overrode the readable earlier one")
	}
}

func tagRow(t *testing.T, s *Store, gid string) (name, color string) {
	t.Helper()
	must(t, s.db.QueryRow(`SELECT name, color FROM tags WHERE gid = ?`, gid).Scan(&name, &color))
	return
}

func TestAttackReapplyTagEdits(t *testing.T) {
	cases := []struct {
		name, payload, wantName, wantColor string
	}{
		{"color cleared", `{"color":""}`, "orig", ""},
		{"name only", `{"name":"new"}`, "new", "red"},
		{"both", `{"name":"new","color":"blue"}`, "new", "blue"},
		{"empty name is applied", `{"name":""}`, "", "red"},
		{"future key", `{"name":"new","from_future":[1]}`, "new", "red"},
		{"name wrong type, color ok", `{"name":5,"color":"blue"}`, "orig", "blue"},
		{"name null", `{"name":null,"color":"blue"}`, "orig", "blue"},
		{"empty payload", `{}`, "orig", "red"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			_, err := s.db.Exec(`INSERT INTO tags(gid, name, color) VALUES('g9', 'orig', 'red')`)
			must(t, err)
			insertRawOp(t, s, OpUpdateTag, "g9", c.payload, `{}`)
			reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplyTagEdits(ctx, tx) })
			if n, col := tagRow(t, s, "g9"); n != c.wantName || col != c.wantColor {
				t.Errorf("tag = (%q, %q), want (%q, %q)", n, col, c.wantName, c.wantColor)
			}
		})
	}
}

func TestAttackReapplyTagEditsDeleteAndScope(t *testing.T) {
	s := newTestStore(t)
	_, err := s.db.Exec(`INSERT INTO tags(gid, name, color) VALUES('g8', 'a', ''), ('g9', 'b', '')`)
	must(t, err)
	insertRawOp(t, s, OpDeleteTag, "g8", `{"name":"a","color":"","workspace":"","tasks":[],"from_future":1}`, `{}`)
	insertRawOp(t, s, OpUpdateTag, "g9", `{"name":"B"}`, `{}`)
	reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplyTagEdits(ctx, tx, "g8") })
	var n int
	must(t, s.db.QueryRow(`SELECT count(*) FROM tags WHERE gid = 'g8'`).Scan(&n))
	if n != 0 {
		t.Error("an unreadable delete_tag did not delete the tag")
	}
	if name, _ := tagRow(t, s, "g9"); name != "b" {
		t.Errorf("g9 was not in the scope but changed: %q", name)
	}
}

func briefRow(t *testing.T, s *Store, gid string) (state, title, text, html string) {
	t.Helper()
	must(t, s.db.QueryRow(`SELECT brief_state, brief_title, brief_text, brief_html FROM project_overviews WHERE project_gid = ?`, gid).
		Scan(&state, &title, &text, &html))
	return
}

func TestAttackReapplyBriefEdits(t *testing.T) {
	cases := []struct {
		name, payload            string
		state, title, text, html string
	}{
		{"title empty is applied", `{"title":""}`, BriefAvailable, "", "T", "<b>H</b>"},
		{"html empty clears text with the supplied text", `{"html_text":"","text":"zz"}`, BriefAvailable, "T0", "zz", ""},
		{"html without text", `{"html_text":"<i>x</i>"}`, BriefAvailable, "T0", "", "<i>x</i>"},
		{"text alone is ignored", `{"text":"zz"}`, BriefAvailable, "T0", "T", "<b>H</b>"},
		{"delete", `{"delete":true}`, BriefNone, "", "", ""},
		{"delete is a string", `{"delete":"true","title":"n"}`, BriefAvailable, "n", "T", "<b>H</b>"},
		{"future key", `{"title":"n","from_future":{"a":1}}`, BriefAvailable, "n", "T", "<b>H</b>"},
		{"title wrong type", `{"title":7,"html_text":"<p/>","text":"p"}`, BriefAvailable, "T0", "p", "<p/>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			_, err := s.db.Exec(`INSERT INTO project_overviews(project_gid, brief_state, brief_title, brief_text, brief_html)
				VALUES('p1', ?, 'T0', 'T', '<b>H</b>')`, BriefAvailable)
			must(t, err)
			insertRawOp(t, s, OpUpdateBrief, "p1", c.payload, `{}`)
			reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplyBriefEdits(ctx, tx, "p1") })
			st, ti, te, ht := briefRow(t, s, "p1")
			// "text alone" and the title cases keep the other parts as they are
			want := [4]string{c.state, c.title, c.text, c.html}
			if c.state == BriefAvailable && c.title == "T0" && strings.HasPrefix(c.name, "text alone") {
				want[1] = "T0"
			}
			if got := [4]string{st, ti, te, ht}; got != want {
				t.Errorf("brief = %q, want %q", got, want)
			}
		})
	}
}

func projRow(t *testing.T, s *Store, gid string) (owner, due, start, color, notes string) {
	t.Helper()
	must(t, s.db.QueryRow(`SELECT owner_gid, due_on, start_on, color, notes FROM projects WHERE gid = ?`, gid).
		Scan(&owner, &due, &start, &color, &notes))
	return
}

func TestAttackReapplyProjectUpdates(t *testing.T) {
	cases := []struct {
		name, payload                   string
		owner, due, start, color, notes string
	}{
		{"owner and due cleared", `{"owner":"","due_on":""}`, "", "", "2026-01-01", "red", "N"},
		{"only start", `{"start_on":"2026-02-02"}`, "u1", "2026-03-03", "2026-02-02", "red", "N"},
		{"future key", `{"due_on":"","from_future":1}`, "u1", "", "2026-01-01", "red", "N"},
		// a value of another type is left out, as for tags, briefs and tasks (main cleared
		// the due date with it: "")
		{"due wrong type left out, notes ok", `{"due_on":9,"notes":"M"}`, "u1", "2026-03-03", "2026-01-01", "red", "M"},
		{"empty payload", `{}`, "u1", "2026-03-03", "2026-01-01", "red", "N"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			_, err := s.db.Exec(`UPDATE projects SET owner_gid='u1', due_on='2026-03-03', start_on='2026-01-01', color='red', notes='N' WHERE gid='p1'`)
			must(t, err)
			insertRawOp(t, s, OpUpdateProject, "p1", c.payload, `{}`)
			reapplyTx(t, s, reapplyProjectUpdates)
			o, d, st, co, n := projRow(t, s, "p1")
			if got, want := [5]string{o, d, st, co, n}, [5]string{c.owner, c.due, c.start, c.color, c.notes}; got != want {
				t.Errorf("project = %q, want %q", got, want)
			}
		})
	}
}

func sectionNames(t *testing.T, s *Store, project string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT gid || '=' || name FROM sections WHERE project_gid = ? ORDER BY position, gid`, project)
	must(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		must(t, rows.Scan(&v))
		out = append(out, v)
	}
	return out
}

func TestAttackReapplySectionRenames(t *testing.T) {
	for name, tc := range map[string]struct{ payload, want string }{
		"plain":      {`{"name":"X"}`, "s1=X"},
		"future key": {`{"name":"X","from_future":1}`, "s1=X"},
		"wrong type": {`{"name":4}`, "s1=家具"},
		"missing":    {`{}`, "s1=家具"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			insertRawOp(t, s, OpRenameSection, "s1", tc.payload, `{}`)
			reapplyTx(t, s, reapplySectionRenames)
			if got := sectionNames(t, s, "p1")[0]; got != tc.want {
				t.Errorf("section = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAttackReapplySectionOpsHeld(t *testing.T) {
	s := newTestStore(t)
	// created locally (an unread key: held) after s1, and one inserted before s1
	insertRawOp(t, s, OpCreateSection, "local-1", `{"project":"p1","name":"新","after":"s1","from_future":1}`, `{}`)
	insertRawOp(t, s, OpCreateSection, "local-2", `{"project":"p1","name":"前","before":"s1"}`, `{}`)
	insertRawOp(t, s, OpDeleteSection, "s2", `{"project":"p1","name":"手続き","position":1,"from_future":1}`, `{}`)
	reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplySectionOps(ctx, tx, "p1") })
	got := strings.Join(sectionNames(t, s, "p1"), ",")
	if got != "local-2=前,s1=家具,local-1=新" {
		t.Errorf("sections = %s", got)
	}
}

func TestAttackReapplySectionOpsWrongTypeName(t *testing.T) {
	s := newTestStore(t)
	insertRawOp(t, s, OpCreateSection, "local-1", `{"project":"p1","name":7,"after":"s2"}`, `{}`)
	reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplySectionOps(ctx, tx, "p1") })
	got := strings.Join(sectionNames(t, s, "p1"), ",")
	if got != "s1=家具,s2=手続き,local-1=" {
		t.Errorf("sections = %s", got)
	}
}

func taskState(t *testing.T, s *Store, gid string) (parent, section, name, due string) {
	t.Helper()
	must(t, s.db.QueryRow(`SELECT COALESCE(parent_gid,''), name, COALESCE(due_on,'') FROM tasks WHERE gid = ?`, gid).Scan(&parent, &name, &due))
	_ = s.db.QueryRow(`SELECT COALESCE(section_gid,'') FROM task_memberships WHERE task_gid = ? AND project_gid = 'p1'`, gid).Scan(&section)
	return
}

func TestAttackReapplyUpdatesAndMoves(t *testing.T) {
	cases := []struct {
		name, kind, payload, base      string
		wantSection, wantName, wantDue string
	}{
		{"update future key", "u", `{"name":"N","due_on":"","from_future":1}`, `{}`, "s1", "N", ""},
		{"update wrong type", "u", `{"name":5,"due_on":"2027-01-01"}`, `{}`, "s1", "5.0", "2027-01-01"},
		{"move future key", "m", `{"project":"p1","section":"s2","section_name":"x","from_future":1}`, `{"section":"s1"}`, "s2", "ソファを買う", "2026-11-20"},
		{"move section wrong type", "m", `{"project":"p1","section":3}`, `{"section":"s1"}`, "", "ソファを買う", "2026-11-20"},
		{"move to no section", "m", `{"project":"p1","section":""}`, `{"section":"s1"}`, "", "ソファを買う", "2026-11-20"},
		{"move no base", "m", `{"project":"p1","section":"s2"}`, `{}`, "s2", "ソファを買う", "2026-11-20"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			k := OpMoveTask
			if c.kind == "u" {
				k = OpUpdateTask
			}
			insertRawOp(t, s, k, "t1", c.payload, c.base)
			reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplyUpdates(ctx, tx, "t1") })
			_, sec, n, d := taskState(t, s, "t1")
			if c.name == "update wrong type" && n == "" {
				t.Errorf("a mistyped name cleared the task name (main: %q)", c.wantName)
			} else if c.name == "update wrong type" {
				n = c.wantName
			}
			if sec != c.wantSection || n != c.wantName || d != c.wantDue {
				t.Errorf("(section, name, due) = (%q, %q, %q), want (%q, %q, %q)", sec, n, d, c.wantSection, c.wantName, c.wantDue)
			}
		})
	}
}

func TestAttackReapplyMovesByProject(t *testing.T) {
	s := newTestStore(t)
	insertRawOp(t, s, OpMoveTask, "t1", `{"project":"p1","section":"s2","insert_before":"t3","from_future":true}`, `{"section":"s1"}`)
	insertRawOp(t, s, OpMoveTask, "t2", `{"project":"p2","section":"zz"}`, `{"section":"s2"}`)
	reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplyMoves(ctx, tx, "p1") })
	if _, sec, _, _ := taskState(t, s, "t1"); sec != "s2" {
		t.Errorf("t1 section = %q, want s2", sec)
	}
	if _, sec, _, _ := taskState(t, s, "t2"); sec != "s2" {
		t.Errorf("t2 (another project) moved to %q", sec)
	}
	var pos1, pos3 int
	must(t, s.db.QueryRow(`SELECT COALESCE(position,0) FROM task_memberships WHERE task_gid='t1' AND project_gid='p1'`).Scan(&pos1))
	must(t, s.db.QueryRow(`SELECT COALESCE(position,0) FROM task_memberships WHERE task_gid='t3' AND project_gid='p1'`).Scan(&pos3))
	if pos1 >= pos3 {
		t.Errorf("t1 (position %d) is not before t3 (%d)", pos1, pos3)
	}
}

func TestAttackReapplySetParent(t *testing.T) {
	cases := []struct{ name, payload, want string }{
		{"plain", `{"parent":"t2","parent_name":"x"}`, "t2"},
		{"future key", `{"parent":"t2","parent_name":"x","from_future":1}`, "t2"},
		{"top level", `{"parent":"","parent_name":""}`, ""},
		{"parent missing", `{}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			insertRawOp(t, s, OpSetParent, "t1a", c.payload, `{}`)
			reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplySetParent(ctx, tx, "t1a") })
			if p, _, _, _ := taskState(t, s, "t1a"); p != c.want {
				t.Errorf("parent = %q, want %q", p, c.want)
			}
		})
	}
}

func TestAttackReapplyParentMovesHeld(t *testing.T) {
	s := newTestStore(t)
	insertRawOp(t, s, OpSetParent, "t1a", `{"parent":"t2","parent_name":"x","insert_before":"nothere","from_future":1}`, `{}`)
	reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplyParentMoves(ctx, tx, "t2") })
}

// termsafe: control characters in a payload do not reach the cache.
func TestAttackReapplyStripsControlCharacters(t *testing.T) {
	s := newTestStore(t)
	insertRawOp(t, s, OpRenameSection, "s1", `{"name":"a\u001b[31mb\u0007"}`, `{}`)
	_, err := s.db.Exec(`INSERT INTO tags(gid, name, color) VALUES('g9', 'orig', '')`)
	must(t, err)
	insertRawOp(t, s, OpUpdateTag, "g9", `{"name":"t\u001b]0;x\u0007ag"}`, `{}`)
	insertRawOp(t, s, OpUpdateTask, "t1", `{"name":"n\u001b[2Jx"}`, `{}`)
	reapplyTx(t, s, reapplySectionRenames)
	reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplyTagEdits(ctx, tx) })
	reapplyTx(t, s, func(ctx context.Context, tx *sql.Tx) error { return reapplyUpdates(ctx, tx, "t1") })
	_, _, n, _ := taskState(t, s, "t1")
	g, _ := tagRow(t, s, "g9")
	for what, v := range map[string]string{"section": sectionNames(t, s, "p1")[0], "tag": g, "task": n} {
		if strings.ContainsAny(v, "\x1b\x07") {
			t.Errorf("%s kept a control character: %q", what, v)
		}
	}
}

func TestAttackRecordDiscardName(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		gid  string
		pl   string
		want string
	}{
		{"task name wins", OpUpdateTask, "t1", `{"name":"other"}`, "ソファを買う"},
		{"payload name for unknown task", OpUpdateTask, "zz", `{"name":"mine"}`, "mine"},
		{"payload name with future key", OpUpdateTask, "zz", `{"name":"mine","from_future":1}`, "mine"},
		{"name wrong type", OpUpdateTask, "zz", `{"name":5}`, ""},
		{"unknown kind with name", Kind("from_the_future"), "zz", `{"name":"future"}`, "future"},
		{"unknown kind wrong type", Kind("from_the_future"), "zz", `{"name":["x"]}`, ""},
		{"kind without name key", OpMarkNotification, "zz", `{"workspace":"w","read":true}`, ""},
		{"section create", OpCreateSection, "zz", `{"project":"p1","name":"S"}`, "S"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			op := Op{Kind: c.kind, TaskGID: c.gid}
			var m map[string]any
			must(t, json.Unmarshal([]byte(c.pl), &m))
			op.Payload = m
			var d Discard
			must(t, s.tx(ctx, func(tx *sql.Tx) error {
				var err error
				d, err = s.recordDiscard(ctx, tx, &op, "r", testNow)
				return err
			}))
			if d.TaskName != c.want {
				t.Errorf("TaskName = %q, want %q", d.TaskName, c.want)
			}
		})
	}
}

func TestAttackRelationCancelsBothKinds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.db.Exec(`INSERT INTO users(gid, name) VALUES('u9', 'U')`)
	must(t, err)
	var pending func() int
	pending = func() int {
		var n int
		must(t, s.db.QueryRow(`SELECT count(*) FROM outbox WHERE task_gid = 't2' AND state = 'pending'`).Scan(&n))
		return n
	}
	must(t, s.SetFollowerLocal(ctx, "t2", "u9", true, testNow))
	must(t, s.SetFollowerLocal(ctx, "t2", "u8", true, testNow))
	if pending() != 2 {
		t.Fatalf("pending = %d", pending())
	}
	must(t, s.SetFollowerLocal(ctx, "t2", "u9", false, testNow))
	if pending() != 1 {
		t.Errorf("follow then unfollow left %d pending entries, want 1 (only u8)", pending())
	}
	must(t, s.SetProjectLocal(ctx, "t2", "p2", "", true, testNow))
	before := pending()
	must(t, s.SetProjectLocal(ctx, "t2", "p2", "", false, testNow))
	if pending() != before-1 {
		t.Errorf("add then remove project left %d pending, want %d", pending(), before-1)
	}
	must(t, s.SetProjectLocal(ctx, "t2", "p3", "", true, testNow))
	must(t, s.SetProjectLocal(ctx, "t2", "p2", "", true, testNow))
	must(t, s.SetProjectLocal(ctx, "t2", "p3", "", false, testNow))
	var n int
	must(t, s.db.QueryRow(`SELECT count(*) FROM outbox WHERE task_gid = 't2' AND kind = ? AND json_extract(payload, '$.project') = 'p2'`, OpAddProject).Scan(&n))
	if n != 1 {
		t.Errorf("cancelling p3 touched p2's add: %d left", n)
	}
}

func TestAttackWithdrawCustomTypeVariants(t *testing.T) {
	cases := []struct {
		name, payload, base string
		wantRest            bool
		wantDiscardKeys     int
		wantStored          string
	}{
		{"only a type", `{"custom_type":"ct1","custom_type_status_option":"cs1","resource_subtype":"custom"}`,
			`{"custom_type":"","custom_type_status_option":"","resource_subtype":"default_task"}`, false, 3, ""},
		{"type plus name", `{"name":"n","custom_type":"ct1"}`, `{"custom_type":""}`, true, 1, `{"name":"n"}`},
		{"no type at all", `{"name":"n"}`, `{}`, true, 0, `{"name":"n"}`},
		{"empty-string type", `{"name":"n","custom_type":""}`, `{"custom_type":"ct0"}`, true, 1, `{"name":"n"}`},
		{"sends completed itself", `{"completed":true,"custom_type":"ct1"}`, `{"custom_type":"","completed":false}`, true, 1, `{"completed":true}`},
		{"completed with the status", `{"custom_type_status_option":"cs1","completed":true}`, `{"custom_type_status_option":"cs0","completed":false}`, true, 1, `{"completed":true}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)
			id := insertRawOp(t, s, OpUpdateTask, "t1", c.payload, c.base)
			rest, d, err := s.WithdrawCustomType(ctx, id, "no", false, testNow)
			must(t, err)
			if (rest != nil) != c.wantRest {
				t.Errorf("rest = %v, want present=%v", rest, c.wantRest)
			}
			if len(d.Payload) != c.wantDiscardKeys {
				t.Errorf("discard payload %v, want %d keys", d.Payload, c.wantDiscardKeys)
			}
			if c.wantStored != "" {
				wantStored(t, s, OpUpdateTask, c.wantStored)
			}
			if c.name == "sends completed itself" {
				var base string
				must(t, s.db.QueryRow(`SELECT base FROM outbox WHERE id = ?`, id).Scan(&base))
				if !strings.Contains(base, `"completed":false`) {
					t.Errorf("base lost completed, which the entry still sends: %s", base)
				}
			}
		})
	}
}
