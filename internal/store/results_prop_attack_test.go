package store

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Property: a mapping whose local gid appears anywhere in an outbox entry's payload or base
// (as a value, key, inside a longer string, next to a longer/shorter gid, in a nested array,
// or in a payload that is not JSON at all) is never deleted, whatever the other gids look like.
func TestPruneResultsNeverDropsAGIDThatSubstringMatches(t *testing.T) {
	r := rand.New(rand.NewSource(44))
	alpha := "abcXYZ019_-.:日"
	rg := func() string {
		n := r.Intn(8) + 1
		b := []rune("local:")
		for i := 0; i < n; i++ {
			rs := []rune(alpha)
			b = append(b, rs[r.Intn(len(rs))])
		}
		return string(b)
	}
	for iter := 0; iter < 150; iter++ {
		st := resultsStore(t)
		var gids []string
		seen := map[string]bool{}
		for i := r.Intn(12) + 2; i > 0; i-- {
			g := rg()
			if r.Intn(3) == 0 && len(gids) > 0 { // a gid that extends or truncates another
				o := []rune(gids[r.Intn(len(gids))])
				if r.Intn(2) == 0 {
					g = string(o) + string([]rune(alpha)[r.Intn(5)])
				} else if len(o) > 7 {
					g = string(o[:len(o)-1])
				}
			}
			if !seen[g] {
				seen[g] = true
				gids = append(gids, g)
				mustSet(t, st, resultPrefixTask+g, "1")
			}
		}
		var texts []string
		for i := r.Intn(6); i > 0; i-- {
			g := gids[r.Intn(len(gids))]
			var s string
			switch r.Intn(7) {
			case 0:
				b, _ := json.Marshal(map[string]any{"parent": g})
				s = string(b)
			case 1:
				b, _ := json.Marshal(map[string]any{g: true})
				s = string(b)
			case 2:
				b, _ := json.Marshal(map[string]any{"deps": []any{1, []any{"x", g}}})
				s = string(b)
			case 3:
				b, _ := json.Marshal(map[string]any{"notes": "see " + g + " and " + rg() + "."})
				s = string(b)
			case 4:
				s = "not json " + g + "}{"
			case 5:
				b, _ := json.Marshal(map[string]any{"n": strings.Repeat("local:", 50) + g + "\n\"<>&"})
				s = string(b)
			default:
				b, _ := json.Marshal(map[string]any{"a": map[string]any{"b": []any{g + "x"}}})
				s = string(b)
			}
			texts = append(texts, s)
		}
		for i, s := range texts {
			if i%2 == 0 {
				insertOp(t, st, OpUpdateTask, "t1", s, "{}", "pending")
			} else {
				insertOp(t, st, OpUpdateTask, "t1", "{}", s, "failed")
			}
		}
		prune(t, st, attackNow)
		prune(t, st, attackNow.Add(2*ResultGrace))
		for _, g := range gids {
			named := false
			for _, s := range texts {
				if strings.Contains(s, g) {
					named = true
				}
			}
			if named && syncStateOf(t, st, resultPrefixTask+g) == "" {
				t.Fatalf("iter %d: %q is in an outbox entry but its mapping was deleted\nentries: %s", iter, g, fmt.Sprint(texts))
			}
		}
	}
}
