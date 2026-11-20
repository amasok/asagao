package outbox

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

// declaredKinds reads the Kind constants declared in this package's source, so that a
// kind added without a spec is caught.
func declaredKinds(t *testing.T) map[Kind]string {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[Kind]string{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Kind" {
						continue
					}
					for i, name := range vs.Names {
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok {
							t.Fatalf("%s is not a string literal", name.Name)
						}
						v, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatal(err)
						}
						if prev, dup := out[Kind(v)]; dup {
							t.Errorf("%s and %s share the stored value %q", prev, name.Name, v)
						}
						out[Kind(v)] = name.Name
					}
				}
			}
		}
	}
	return out
}

// Every declared kind has a spec, and AllKinds lists exactly the declared kinds.
func TestEveryKindHasASpec(t *testing.T) {
	declared := declaredKinds(t)
	if len(declared) != 37 {
		t.Errorf("%d kinds declared, want 37 (update this number with the kind and its spec)", len(declared))
	}
	all := AllKinds()
	for k, name := range declared {
		s, ok := SpecOf(k)
		if !ok || !k.Known() {
			t.Errorf("%s (%q) has no spec", name, k)
		}
		if s.Subject == 0 {
			t.Errorf("%s (%q) has no subject", name, k)
		}
		if !slices.Contains(all, k) {
			t.Errorf("AllKinds misses %s", name)
		}
	}
	if len(all) != len(declared) {
		t.Errorf("AllKinds has %d kinds, %d declared", len(all), len(declared))
	}
	if !slices.IsSorted(all) {
		t.Error("AllKinds is not sorted")
	}
}

func TestUnknownKind(t *testing.T) {
	for _, k := range []Kind{"", "unknown", "Update_Task", "update_task "} {
		if k.Known() || k.OnTask() || SubjectOf(k) != 0 {
			t.Errorf("%q is known", k)
		}
		if _, ok := SpecOf(k); ok {
			t.Errorf("%q has a spec", k)
		}
	}
}

func TestSubjects(t *testing.T) {
	for k, want := range map[Kind]Subject{
		KindUpdateTask:         SubjectTask,
		KindMoveMySection:      SubjectTask,
		KindCreateTask:         SubjectNewTask,
		KindDuplicateTask:      SubjectNewTask,
		KindRenameSection:      SubjectSection,
		KindInsertSection:      SubjectSection,
		KindUpdateTag:          SubjectTag,
		KindDeleteTag:          SubjectTag,
		KindCreateStatus:       SubjectProject,
		KindUpdateProjectBrief: SubjectProject,
		KindRemoveFieldSetting: SubjectProject,
		KindMarkNotification:   SubjectNotification,
		KindCreateEnumOption:   SubjectEnumOption,
	} {
		if got := SubjectOf(k); got != want {
			t.Errorf("SubjectOf(%s) = %d, want %d", k, got, want)
		}
		if k.OnTask() != (want == SubjectTask) {
			t.Errorf("%s.OnTask() = %v", k, k.OnTask())
		}
	}
}

func TestTaskDeps(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   Op
		want []string
	}{
		{"an edit waits for its task", Op{Kind: KindUpdateTask, TaskGID: "local:a"}, []string{"local:a"}},
		{"a new parent and the task", Op{Kind: KindSetParent, TaskGID: "t1", Payload: map[string]any{"parent": "local:p"}}, []string{"local:p", "t1"}},
		{"to the top level: only the task", Op{Kind: KindSetParent, TaskGID: "t1", Payload: map[string]any{"parent": ""}}, []string{"t1"}},
		{"a subtask waits for its parent", Op{Kind: KindCreateTask, TaskGID: "local:c", Payload: map[string]any{"parent": "local:p"}}, []string{"local:p"}},
		{"a top-level create waits for nothing", Op{Kind: KindCreateTask, TaskGID: "local:c"}, nil},
		{"a duplicate is its own create", Op{Kind: KindDuplicateTask, TaskGID: "local:c", Payload: map[string]any{"source": "t1"}}, nil},
		{"a section is not a task", Op{Kind: KindRenameSection, TaskGID: "local:s"}, nil},
		{"a tag is not a task", Op{Kind: KindUpdateTag, TaskGID: "local:g"}, nil},
		{"an unknown kind", Op{Kind: "unknown", TaskGID: "local:a"}, nil},
	} {
		if got := TaskDeps(tc.op); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Every kind that creates something in Asana is unsafe to resend; every other one (but an
// add_tag of a tag created offline) is safe.
func TestSafeToResendFollowsCreates(t *testing.T) {
	never := func(string) (bool, error) { t.Fatal("tagCreated called"); return false, nil }
	for _, k := range AllKinds() {
		s, _ := SpecOf(k)
		safe, err := SafeToResend(Op{Kind: k, Payload: map[string]any{"tag": "g1"}}, never)
		if err != nil || safe == s.Creates {
			t.Errorf("%s: safe = %v, creates = %v (%v)", k, safe, s.Creates, err)
		}
	}
}
