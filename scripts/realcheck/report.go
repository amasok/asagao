package main

import (
	"bytes"
	"fmt"
	"strings"
)

// Report collects the results as Markdown (pasted into the issues).
type Report struct {
	lines  []string
	failed int
	log    bytes.Buffer
}

func (r *Report) group(name string) { r.lines = append(r.lines, "", "### "+name) }

func (r *Report) check(name string, ok bool, detail string) {
	mark := "OK"
	if !ok {
		mark = "NG"
		r.failed++
	}
	r.lines = append(r.lines, fmt.Sprintf("- %s %s", mark, name))
	if detail = strings.TrimSpace(detail); detail != "" && !ok {
		r.lines = append(r.lines, indent(detail))
	}
}

// note records an observation that is neither pass nor fail (e.g. a 402).
func (r *Report) note(name, detail string) {
	r.lines = append(r.lines, fmt.Sprintf("- 記録 %s: %s", name, oneLine(detail)))
}

func (r *Report) fail(name, detail string) { r.check(name, false, detail) }

func (r *Report) exitCode() int {
	if r.failed > 0 {
		return 1
	}
	return 0
}

func (r *Report) Markdown() string {
	head := fmt.Sprintf("## realcheck の結果 (NG %d 件)\n", r.failed)
	return head + strings.Join(r.lines, "\n") + "\n"
}

func indent(s string) string {
	if len(s) > 1500 {
		s = s[:1500] + " …"
	}
	return "  ```\n  " + strings.ReplaceAll(s, "\n", "\n  ") + "\n  ```"
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + " …"
	}
	return s
}
