package richtext

import (
	"encoding/json"
	"math/rand"
	"os"
	"strings"
	"testing"
)

type goldenCase struct{ In, HTML, Plain string }

const goldenPath = "testdata/tasknotes_golden.json"

// goldenInputs is the corpus testdata/tasknotes_golden.json was made from.
func goldenInputs() []string {
	r := rand.New(rand.NewSource(630))
	var ins []string
	for i := 0; i < 1500; i++ {
		n := 0
		ins = append(ins, strings.Join(genTargetBlock(r, 0, &n), "\n"))
	}
	return ins
}

// A task's notes convert as the golden file says: it holds what the FromMarkdown /
// PlainFromMarkdown of main (45ff98d) made of goldenInputs, changed only where a fix meant
// to change it (#64, #65, #72; see openspec/specs/rich-text/spec.md).
//
// RICHTEXT_UPDATE_GOLDEN=1 go test -run TestTaskNotesMatchMain ./internal/richtext writes the
// golden from the code as it is now and logs each case that changed, to check by eye.
func TestTaskNotesMatchMain(t *testing.T) {
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var cs []goldenCase
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatal(err)
	}
	ins := goldenInputs()
	if len(cs) != len(ins) {
		t.Fatalf("golden has %d cases, corpus %d", len(cs), len(ins))
	}
	if os.Getenv("RICHTEXT_UPDATE_GOLDEN") != "" {
		updateGolden(t, cs)
		return
	}
	for i, c := range cs {
		if c.In != ins[i] {
			t.Fatalf("case %d: corpus changed (regenerate the golden from main)", i)
		}
		if got := TaskNotes.FromMarkdown(c.In); got != c.HTML {
			t.Fatalf("TaskNotes.FromMarkdown(%q)\ngot  %q\nmain %q", c.In, got, c.HTML)
		}
		if got := TaskNotes.PlainFromMarkdown(c.In); got != c.Plain {
			t.Fatalf("TaskNotes.PlainFromMarkdown(%q)\ngot  %q\nmain %q", c.In, got, c.Plain)
		}
	}
}

func updateGolden(t *testing.T, cs []goldenCase) {
	changed := 0
	for i, c := range cs {
		h, p := TaskNotes.FromMarkdown(c.In), TaskNotes.PlainFromMarkdown(c.In)
		if h != c.HTML || p != c.Plain {
			changed++
			t.Logf("case %d %q\nhtml  %q\n   -> %q\nplain %q\n   -> %q", i, c.In, c.HTML, h, c.Plain, p)
		}
		cs[i].HTML, cs[i].Plain = h, p
	}
	out, err := json.MarshalIndent(cs, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goldenPath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d of %d cases changed", changed, len(cs))
}
