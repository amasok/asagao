package readmodel

import (
	"slices"
	"testing"
)

func TestIsImageName(t *testing.T) {
	for name, want := range map[string]bool{
		"a.png": true, "b.JPG": true, "c.jpeg": true, "d.gif": true, "e.webp": true, "f.HEIC": true, "g.bmp": true,
		"h.pdf": false, "png": false, "i.png.txt": false, "": false, ".png": true,
	} {
		if got := IsImageName(name); got != want {
			t.Errorf("IsImageName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestInlineAssets(t *testing.T) {
	const a = "https://app.asana.com/app/asana/-/get_asset?asset_id=1220507679870013"
	const b = "https://app.asana.com/app/asana/-/get_asset?width=10&asset_id=42&x=1"
	for _, tc := range []struct {
		text     string
		gids     []string
		replaced string
	}{
		{"前 " + a + " 後", []string{"1220507679870013"}, "前 [1220507679870013] 後"},
		{"(" + b + ")\n" + a, []string{"42", "1220507679870013"}, "([42])\n[1220507679870013]"},
		{"![x](" + a + ")", []string{"1220507679870013"}, "![x]([1220507679870013])"},
		{"https://app.asana.com/app/asana/-/get_asset?id=1", nil, "https://app.asana.com/app/asana/-/get_asset?id=1"},
		{"https://example.com/get_asset?asset_id=1", nil, "https://example.com/get_asset?asset_id=1"},
		{"", nil, ""},
	} {
		if got := InlineAssets(tc.text); !slices.Equal(got, tc.gids) {
			t.Errorf("InlineAssets(%q) = %q, want %q", tc.text, got, tc.gids)
		}
		if got := ReplaceInlineAssets(tc.text, func(g string) string { return "[" + g + "]" }); got != tc.replaced {
			t.Errorf("ReplaceInlineAssets(%q) = %q, want %q", tc.text, got, tc.replaced)
		}
	}
}

// The values are stored (sync_state keys, custom field types from Asana), so they must
// not change.
func TestStoredNames(t *testing.T) {
	for got, want := range map[string]string{
		KeyMeGID: "me_gid", KeyCollapsed: "ui:collapsed:",
		KeyLastSync: "last_sync", KeyEvents: "events:", KeyCustomFields: "custom_fields:",
		ResultTask: "gid:", ResultStory: "story:", ResultAttachment: "att:", ResultStatus: "status:", ResultTag: "tag:",
		CFText: "text", CFNumber: "number", CFEnum: "enum", CFMultiEnum: "multi_enum", CFDate: "date", CFPeople: "people",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	if AnyUnsent != -1 {
		t.Errorf("AnyUnsent = %d", AnyUnsent)
	}
}
