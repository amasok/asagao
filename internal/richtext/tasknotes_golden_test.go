package richtext

import (
	"encoding/json"
	"math/rand"
	"os"
	"strings"
	"testing"
)

type goldenCase struct{ In, HTML, Plain string }

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

// A task's notes convert exactly as they did before #63: the golden file holds what the
// FromMarkdown / PlainFromMarkdown of main (45ff98d) made of goldenInputs.
func TestTaskNotesMatchMain(t *testing.T) {
	b, err := os.ReadFile("testdata/tasknotes_golden.json")
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
