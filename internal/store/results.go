package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

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
	resultPrefixTask:       {OpCreateTask, OpDuplicateTask},
	resultPrefixStory:      {OpAddComment},
	resultPrefixAttachment: {OpUploadAttachment},
	resultPrefixStatus:     {OpCreateStatus},
	resultPrefixTag:        nil,
	resultPrefixSection:    {OpCreateSection},
	resultPrefixEnumOption: {OpCreateEnumOption},
}

// resultTables are, per mapping prefix, the local gids in the rows that would still carry
// one when the swap to the Asana gid has not happened (or not everywhere).
var resultTables = map[string][]string{
	resultPrefixTask: {localIn("tasks", "gid"), localIn("tasks", "parent_gid"),
		localIn("task_dependencies", "task_gid"), localIn("task_dependencies", "dependency_gid")},
	resultPrefixStory:      {localIn("stories", "gid")},
	resultPrefixAttachment: {localIn("attachments", "gid")},
	resultPrefixStatus:     {localIn("status_updates", "gid")},
	resultPrefixTag:        {localIn("tags", "gid"), localIn("task_tags", "tag_gid")},
	resultPrefixSection:    {localIn("sections", "gid"), localIn("task_memberships", "section_gid")},
	// An option is stored inside JSON: in the field's options, and in the tasks' values.
	resultPrefixEnumOption: {localInJSON("custom_fields", "enum_options", "$.gid"), localInJSON("task_custom_fields", "value", "")},
}

// localIn selects the local gids in a column (substr, not LIKE: "_" and "%" are literal).
func localIn(table, column string) string {
	return "SELECT DISTINCT " + column + " FROM " + table + " WHERE substr(" + column + ", 1, " +
		strconv.Itoa(len(LocalPrefix)) + ") = '" + LocalPrefix + "'"
}

// localInJSON selects the local gids in a JSON column: the strings of an array or the
// value itself, or (path set) the strings at path in each element of an array.
func localInJSON(table, column, path string) string {
	elem := "j.value"
	if path != "" {
		elem = "json_extract(j.value, '" + path + "')"
	}
	column = table + "." + column
	return "SELECT DISTINCT " + elem + " FROM " + table + ", json_each(CASE WHEN json_valid(" + column + ") THEN " +
		"(CASE WHEN json_type(" + column + ") = 'array' THEN " + column + " ELSE json_array(json(" + column + ")) END) ELSE '[]' END) j " +
		"WHERE typeof(" + elem + ") = 'text' AND substr(" + elem + ", 1, " + strconv.Itoa(len(LocalPrefix)) + ") = '" + LocalPrefix + "'"
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
	// Its write transaction begins immediate, so another process's write (a push) makes it
	// wait for up to busy_timeout rather than fail at once (#50). Still busy after that, try
	// once more; then it is left for the next sync without an error (nothing was changed).
	n, err := s.pruneResults(ctx, now)
	if isBusy(err) {
		n, err = s.pruneResults(ctx, now)
	}
	if isBusy(err) {
		return 0, nil
	}
	return n, err
}

// isBusy reports whether err is SQLite's "database is locked" (SQLITE_BUSY, including
// SQLITE_BUSY_SNAPSHOT).
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// pruneResults reads what to change in a read-only transaction, which takes no write lock:
// most prunes find nothing to change (mappings found unneeded wait for ResultGrace). Only
// when there is something does it begin a write transaction, and read again in it, as
// another process may have queued a change in between.
func (s *Store) pruneResults(ctx context.Context, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	changes, _, err := planPruneResults(ctx, tx, now)
	tx.Rollback()
	if err != nil || len(changes) == 0 {
		return 0, err
	}
	if pruneResultsPlanned != nil {
		pruneResultsPlanned()
	}
	deleted := 0
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		changes, deleted, err = planPruneResults(ctx, tx, now)
		if err != nil {
			return err
		}
		for _, c := range changes {
			if c.del {
				_, err = tx.ExecContext(ctx, "DELETE FROM sync_state WHERE key = ?", c.key)
			} else {
				_, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO sync_state(key, value) VALUES (?, ?)", c.key, c.value)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// pruneResultsPlanned, set by tests, runs between the read-only plan and the write
// transaction.
var pruneResultsPlanned func()

// stateChange is a change to a sync_state row: deleted, or set to value.
type stateChange struct {
	key, value string
	del        bool
}

// planPruneResults reads in tx what PruneResults changes, and how many mappings that
// deletes. It changes nothing.
func planPruneResults(ctx context.Context, tx *sql.Tx, now time.Time) ([]stateChange, int, error) {
	var changes []stateChange
	deleted := 0
	state, err := readSyncStatePrefixes(ctx, tx, keyResultSeen, resultPrefixTask, resultPrefixStory, resultPrefixAttachment, resultPrefixStatus, resultPrefixTag, resultPrefixEnumOption)
	if err != nil {
		return nil, 0, err
	}
	maxLen := 0
	for key := range state {
		if _, local := splitResultKey(key); IsLocalGID(local) {
			maxLen = max(maxLen, len(local))
		}
	}
	refs, err := readLocalRefs(ctx, tx, maxLen)
	if err != nil {
		return nil, 0, err
	}
	blocked := map[string]bool{}
	for prefix, kinds := range resultKinds {
		for _, k := range kinds {
			var one int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM outbox WHERE kind = ? LIMIT 1", k).Scan(&one)
			if err == nil {
				blocked[prefix] = true
			} else if err != sql.ErrNoRows {
				return nil, 0, err
			}
		}
	}
	set := func(k, v string) { changes = append(changes, stateChange{key: k, value: v}) }
	del := func(k string) { changes = append(changes, stateChange{key: k, del: true}) }
	for key := range state {
		if seen, ok := strings.CutPrefix(key, keyResultSeen); ok {
			if _, mapped := state[seen]; !mapped {
				del(key) // its mapping is gone
			}
			continue
		}
		prefix, local := splitResultKey(key)
		if prefix == "" || !IsLocalGID(local) {
			continue // not a mapping of a local gid: never touched
		}
		needed := blocked[prefix] || refs.inUse(prefix, local)
		seenKey := keyResultSeen + key
		if needed {
			if _, ok := state[seenKey]; ok {
				del(seenKey)
			}
			continue
		}
		seen, err := time.Parse(time.RFC3339, state[seenKey])
		if err != nil || seen.After(now) { // first found unneeded (or a stamp that cannot be trusted)
			set(seenKey, now.UTC().Format(time.RFC3339))
			continue
		}
		if now.Sub(seen) < ResultGrace {
			continue
		}
		del(key)
		del(seenKey)
		deleted++
	}
	return changes, deleted, nil
}

// splitResultKey splits a mapping key into its prefix and the local gid.
func splitResultKey(key string) (prefix, local string) {
	for p := range resultKinds {
		if l, ok := strings.CutPrefix(key, p); ok {
			return p, l
		}
	}
	return "", ""
}

// localRefs is what in the store still refers to local gids, read once per prune: the
// text of the outbox entries (their task, and every JSON string in their payload and base,
// from each "local:" in it to its end), and the local gids in the rows of resultTables.
type localRefs struct {
	outbox []string                   // sorted: a gid is referred to if one starts with it
	rows   map[string]map[string]bool // per prefix
}

// Texts are cut at maxLen, the longest local gid of the mappings: whether a text starts
// with one of them does not change, and a payload full of "local:" stays cheap.
func readLocalRefs(ctx context.Context, tx *sql.Tx, maxLen int) (*localRefs, error) {
	refs := &localRefs{rows: map[string]map[string]bool{}}
	seen := map[string]bool{}
	add := func(s string) {
		for i := strings.Index(s, LocalPrefix); i >= 0; {
			if t := s[i:min(len(s), i+maxLen)]; !seen[t] {
				seen[t] = true
				refs.outbox = append(refs.outbox, t)
			}
			j := strings.Index(s[i+1:], LocalPrefix)
			if j < 0 {
				break
			}
			i += 1 + j
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT task_gid, payload, base FROM outbox")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var task, payload, base string
		if err := rows.Scan(&task, &payload, &base); err != nil {
			rows.Close()
			return nil, err
		}
		add(task)
		for _, raw := range []string{payload, base} {
			if !strings.Contains(raw, LocalPrefix) {
				continue
			}
			var v any
			if json.Unmarshal([]byte(raw), &v) != nil {
				add(raw) // not JSON: the raw text, as it is
				continue
			}
			jsonStrings(v, add)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(refs.outbox)
	for prefix, queries := range resultTables {
		set := map[string]bool{}
		for _, q := range queries {
			rows, err := tx.QueryContext(ctx, q)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var g string
				if err := rows.Scan(&g); err != nil {
					rows.Close()
					return nil, err
				}
				set[g] = true
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		refs.rows[prefix] = set
	}
	return refs, nil
}

// jsonStrings calls f with every string in v (keys included).
func jsonStrings(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case []any:
		for _, e := range x {
			jsonStrings(e, f)
		}
	case map[string]any:
		for k, e := range x {
			f(k)
			jsonStrings(e, f)
		}
	}
}

// inUse reports whether something in the store still refers to local (a mapping of prefix).
// A text that only starts with local (a longer gid) counts too: that only keeps it longer.
func (r *localRefs) inUse(prefix, local string) bool {
	if r.rows[prefix][local] {
		return true
	}
	i := sort.SearchStrings(r.outbox, local)
	return i < len(r.outbox) && strings.HasPrefix(r.outbox[i], local)
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
