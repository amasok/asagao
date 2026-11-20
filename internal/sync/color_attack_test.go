package sync

import (
	"context"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/tag"
)

// A tag colored with another name of the same new color is no conflict (dark-blue and
// light-blue are both blue); Asana's real change to another color still is. Choosing the
// cached color again by another name queues nothing at all (#52).
func TestAttackTagColorSameNewName(t *testing.T) {
	for _, c := range []struct {
		name, stored, asana, want string
		conflicts                 int
	}{
		{"blue is blue", "blue", "blue", "light-blue", 0},
		{"asana changed, same as wanted", "yellow", "blue", "dark-blue", 0},
		{"asana changed, light-blue wanted", "yellow", "blue", "light-blue", 0},
		{"asana changed, light-red wanted", "blue", "pink", "light-red", 0},
		{"asana changed, dark-brown wanted", "blue", "yellow", "dark-brown", 0},
		{"asana changed to other", "yellow", "red", "dark-blue", 1},
		{"asana changed to none", "red", "none", "dark-blue", 1},
		{"asana changed to other, want none", "red", "blue", "", 1},
		{"stored old name equals asana new", "dark-red", "red", "light-teal", 0},
		{"asana unknown color name", "red", "yellow-green", "dark-blue", 1},
		{"case of asana's answer", "blue", "RED", "dark-red", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, s, st := setupSections(t)
			ctx := context.Background()
			_ = st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ", Color: c.stored}})
			f.mu.Lock()
			f.tags["g1"].Color = c.asana
			f.mu.Unlock()
			if err := st.UpdateTagLocal(ctx, "g1", map[string]any{"color": c.want}, now); err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil || res.Conflicts != c.conflicts || res.Sent+res.Conflicts != queued(c.stored, c.want) {
				t.Fatalf("push = %+v, %v", res, err)
			}
		})
	}
}

// queued is the number of entries a color change from stored to want queues: none when it
// is the same color by another name (#52).
func queued(stored, want string) int {
	if tag.CanonicalColor(stored) == tag.CanonicalColor(want) {
		return 0
	}
	return 1
}

// A project, the same.
func TestAttackProjectColorSameNewName(t *testing.T) {
	for _, c := range []struct {
		name, stored, asana, want string
		conflicts                 int
	}{
		{"light and dark blue", "dark-blue", "blue", "light-blue", 0},
		{"asana changed to the wanted new name", "yellow", "blue", "dark-blue", 0},
		{"light-blue is blue", "yellow", "blue", "light-blue", 0},
		{"light-yellow is yellow", "blue", "yellow", "light-yellow", 0},
		{"asana changed to other", "dark-blue", "orange", "light-red", 1},
		{"asana: none, stored: color", "dark-green", "none", "light-pink", 1},
		{"want none, asana changed", "dark-green", "red", "none", 1},
		{"want none, asana same", "dark-green", "green", "none", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			ps, s, st := setupProject(t)
			ps.color = c.stored
			p, _ := s.API.Project(ctx, "p1")
			if err := saveProject(ctx, s.Store, p); err != nil {
				t.Fatal(err)
			}
			ps.color = c.asana
			if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"color": c.want}, now); err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil || res.Conflicts != c.conflicts || res.Sent+res.Conflicts != queued(c.stored, c.want) {
				t.Fatalf("push = %+v, %v", res, err)
			}
		})
	}
}

// light-brown is refused when a change is made (not queued to fail at Asana).
func TestAttackLightBrownIsRefusedLocally(t *testing.T) {
	ctx := context.Background()
	_, _, st := setupProject(t)
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"color": "light-brown"}, now); err == nil {
		t.Fatal("project accepted light-brown")
	}
	if err := st.UpdateProjectLocal(ctx, "p1", map[string]any{"color": "light-yellow"}, now); err != nil {
		t.Fatal(err)
	}
}

// A rename together with a color: a same-color answer is no conflict, a changed name is
// one (for the name only: the color is not reported as conflicting).
func TestAttackTagNameAndColorTogether(t *testing.T) {
	for _, c := range []struct {
		name, asanaName, asanaColor string
		conflicts                   int
	}{
		{"nothing changed there", "急ぎ", "blue", 0},
		{"name changed there", "別名", "blue", 1},
		{"color changed there", "急ぎ", "red", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, s, st := setupSections(t)
			ctx := context.Background()
			_ = st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ", Color: "dark-blue"}})
			f.mu.Lock()
			f.tags["g1"].Name, f.tags["g1"].Color = c.asanaName, c.asanaColor
			f.mu.Unlock()
			if err := st.UpdateTagLocal(ctx, "g1", map[string]any{"name": "新", "color": "light-red"}, now); err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil || res.Conflicts != c.conflicts || res.Sent+res.Conflicts != 1 {
				t.Fatalf("push = %+v, %v", res, err)
			}
		})
	}
}
