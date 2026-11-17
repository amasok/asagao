package mcpserver

import (
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// limit: a negative or huge argument never exceeds the cap or fails; the search's own limit
// is the default and the argument overrides it.
func TestSavedSearchAttackLimit(t *testing.T) {
	f := searchFile(t,
		readmodel.SavedSearch{Name: "all", Query: "を", Limit: 1},
		readmodel.SavedSearch{Name: "none", Query: "を"},
	)
	cs := connect(t, &Server{Store: seed(t), Searches: f})
	for _, tc := range []struct {
		name  string
		limit any
		max   int
	}{
		{"all", nil, 1},
		{"all", -5, 1},
		{"all", 0, 1},
		{"all", 1 << 30, maxLimit},
		{"none", -1, maxLimit},
		{"none", 1, 1},
	} {
		args := map[string]any{"name": tc.name}
		if tc.limit != nil {
			args["limit"] = tc.limit
		}
		var out RunSavedSearchOutput
		if res := call(t, cs, "run_saved_search", args, &out); res.IsError {
			t.Fatalf("%+v: %s", tc, toolError(res))
		}
		if out.Count > tc.max || out.Count != len(out.Tasks) {
			t.Errorf("%+v: count %d, max %d", tc, out.Count, tc.max)
		}
	}
}

// The tools are read-only: they never write the search file, and a name that is only
// almost right (case, trailing space) is not found.
func TestSavedSearchAttackExactNameOnly(t *testing.T) {
	f := searchFile(t, readmodel.SavedSearch{Name: "Weekly", DueBefore: "+7d"})
	cs := connect(t, &Server{Store: seed(t), Searches: f})
	for _, n := range []string{"weekly", "Weekly ", " Weekly", "Week"} {
		if res := call(t, cs, "run_saved_search", map[string]any{"name": n}, nil); !res.IsError {
			t.Errorf("%q ran", n)
		}
	}
}
