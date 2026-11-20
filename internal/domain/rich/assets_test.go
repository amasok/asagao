package rich

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
