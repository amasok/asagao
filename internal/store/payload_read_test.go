package store

import (
	"context"
	"database/sql"
	"testing"
)

// The store reads payloads through their kinds' structs (archtest:
// TestStoreReadsPayloadsThroughTheirStructs): a mark whose payload does not read is not
// sent along with another, and a held entry (a key of a newer version) is still applied
// locally as far as it reads.

func TestPendingNotificationMarksLeavesOutUnreadable(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	first := insertRawOp(t, s, OpMarkNotification, "n1", `{"workspace":"w1","name":"a","read":true}`, `{}`)
	insertRawOp(t, s, OpMarkNotification, "n2", `{"workspace":"w1","name":"b","read":true,"from_future":1}`, `{}`)
	insertRawOp(t, s, OpMarkNotification, "n3", `{"workspace":"w1","name":"c","read":"yes"}`, `{}`)
	readable := insertRawOp(t, s, OpMarkNotification, "n4", `{"workspace":"w1","name":"d","read":true}`, `{}`)
	insertRawOp(t, s, OpMarkNotification, "n5", `{"workspace":"w1","name":"e","read":false}`, `{}`)
	insertRawOp(t, s, OpMarkNotification, "n6", `{"workspace":"w2","name":"f","read":true}`, `{}`)
	ops, err := s.queryOps(ctx, "WHERE o.id = ?", first)
	must(t, err)
	peers, err := s.PendingNotificationMarks(ctx, ops[0], 10)
	must(t, err)
	if len(peers) != 1 || peers[0].ID != readable {
		t.Errorf("peers = %+v, want only entry %d", peers, readable)
	}
}

func TestReapplyHeldEntriesAsFarAsTheyRead(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.db.Exec(`INSERT INTO notifications(gid, workspace_gid, is_read) VALUES('n1', 'w1', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	insertRawOp(t, s, OpMarkNotification, "n1", `{"workspace":"w1","name":"a","read":true,"from_future":1}`, `{}`)
	must(t, s.tx(ctx, func(tx *sql.Tx) error { return reapplyNotificationMarks(ctx, tx) }))
	var read bool
	must(t, s.db.QueryRow(`SELECT is_read FROM notifications WHERE gid = 'n1'`).Scan(&read))
	if !read {
		t.Error("a held mark is not applied locally")
	}
}
