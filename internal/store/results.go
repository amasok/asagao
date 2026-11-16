package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ResultTag is the prefix of the sync_state keys that map a tag created offline to the
// tag Asana created (see ReplaceLocalTagGID).
const ResultTag = "tag:"

// keyResultSeen + a mapping key records when PruneResults first found the mapping no
// longer needed; the mapping is deleted once that is ResultGrace old.
const keyResultSeen = "result_seen:"

// ResultGrace is how long a mapping from a local gid to the Asana gid is kept after
// nothing in the store needs it any more: someone may still hold the local gid (an MCP
// client that created the task offline, a gid copied from the CLI's output).
const ResultGrace = 30 * 24 * time.Hour

// resultKinds are, per mapping prefix, the outbox kinds whose verification (sync's
// reconcile) relies on the recorded results to not take an Asana item that already
// belongs to another local entry (RecordedResults). While any of them is queued, no
// mapping of that prefix is deleted.
var resultKinds = map[string][]string{
	ResultTask:       {OpCreateTask, OpDuplicateTask},
	ResultStory:      {OpAddComment},
	ResultAttachment: {OpUploadAttachment},
	ResultStatus:     {OpCreateStatus},
	ResultTag:        nil,
}

// resultTables are, per mapping prefix, the rows that would still carry the local gid
// when the swap to the Asana gid has not happened (or not everywhere).
var resultTables = map[string][]string{
	ResultTask:       {"SELECT 1 FROM tasks WHERE gid = ?1 OR parent_gid = ?1", "SELECT 1 FROM task_dependencies WHERE task_gid = ?1 OR dependency_gid = ?1"},
	ResultStory:      {"SELECT 1 FROM stories WHERE gid = ?1"},
	ResultAttachment: {"SELECT 1 FROM attachments WHERE gid = ?1"},
	ResultStatus:     {"SELECT 1 FROM status_updates WHERE gid = ?1"},
	ResultTag:        {"SELECT 1 FROM tags WHERE gid = ?1", "SELECT 1 FROM task_tags WHERE tag_gid = ?1"},
}

// PruneResults deletes the mappings from local gids (things created offline) to the
// Asana gids they got, once nothing needs them (#44). A mapping is kept while
//
//   - an outbox entry, in any state, refers to the local gid (as its task or anywhere in
//     its payload or base), or a row in the store still has it;
//   - an outbox entry of a kind whose verification relies on the recorded results is
//     queued (see resultKinds): deleting a mapping could let it take that item for its own;
//   - it has been found unneeded for less than ResultGrace (counted from the first prune
//     that found it so; found needed again, the count starts over).
//
// Anything it cannot check is kept for the next prune. It returns how many mappings were
// deleted.
func (s *Store) PruneResults(ctx context.Context, now time.Time) (int, error) {
	deleted := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		deleted = 0
		state, err := readSyncStatePrefixes(ctx, tx, keyResultSeen, ResultTask, ResultStory, ResultAttachment, ResultStatus, ResultTag)
		if err != nil {
			return err
		}
		blocked := map[string]bool{}
		for prefix, kinds := range resultKinds {
			for _, k := range kinds {
				var one int
				err := tx.QueryRowContext(ctx, "SELECT 1 FROM outbox WHERE kind = ? LIMIT 1", k).Scan(&one)
				if err == nil {
					blocked[prefix] = true
				} else if err != sql.ErrNoRows {
					return err
				}
			}
		}
		set := func(k, v string) error {
			_, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO sync_state(key, value) VALUES (?, ?)", k, v)
			return err
		}
		del := func(k string) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM sync_state WHERE key = ?", k)
			return err
		}
		for key := range state {
			if seen, ok := strings.CutPrefix(key, keyResultSeen); ok {
				if _, mapped := state[seen]; !mapped {
					if err := del(key); err != nil { // its mapping is gone
						return err
					}
				}
				continue
			}
			prefix, local := resultPrefix(key)
			if prefix == "" || !IsLocalGID(local) {
				continue // not a mapping of a local gid: never touched
			}
			needed := blocked[prefix]
			if !needed {
				if needed, err = localGIDInUse(ctx, tx, prefix, local); err != nil {
					return err
				}
			}
			seenKey := keyResultSeen + key
			if needed {
				if _, ok := state[seenKey]; ok {
					if err := del(seenKey); err != nil {
						return err
					}
				}
				continue
			}
			seen, err := time.Parse(time.RFC3339, state[seenKey])
			if err != nil || seen.After(now) { // first found unneeded (or a stamp that cannot be trusted)
				if err := set(seenKey, now.UTC().Format(time.RFC3339)); err != nil {
					return err
				}
				continue
			}
			if now.Sub(seen) < ResultGrace {
				continue
			}
			if err := del(key); err != nil {
				return err
			}
			if err := del(seenKey); err != nil {
				return err
			}
			deleted++
		}
		return nil
	})
	return deleted, err
}

// resultPrefix splits a mapping key into its prefix and the local gid.
func resultPrefix(key string) (prefix, local string) {
	for p := range resultKinds {
		if l, ok := strings.CutPrefix(key, p); ok {
			return p, l
		}
	}
	return "", ""
}

// localGIDInUse reports whether something in the store still refers to local.
func localGIDInUse(ctx context.Context, tx *sql.Tx, prefix, local string) (bool, error) {
	queries := append([]string{
		// Local gids are random and long, so a substring of the JSON is the gid itself (a
		// false match only keeps the mapping longer).
		"SELECT 1 FROM outbox WHERE task_gid = ?1 OR instr(payload, ?1) > 0 OR instr(base, ?1) > 0",
	}, resultTables[prefix]...)
	for _, q := range queries {
		var one int
		err := tx.QueryRowContext(ctx, q+" LIMIT 1", local).Scan(&one)
		if err == nil {
			return true, nil
		}
		if err != sql.ErrNoRows {
			return false, err
		}
	}
	return false, nil
}

// readSyncStatePrefixes returns the sync_state rows whose key starts with one of prefixes.
func readSyncStatePrefixes(ctx context.Context, tx *sql.Tx, prefixes ...string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range prefixes {
		rows, err := tx.QueryContext(ctx, "SELECT key, value FROM sync_state WHERE substr(key, 1, ?) = ?", len(p), p)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				rows.Close()
				return nil, err
			}
			out[k] = v
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
