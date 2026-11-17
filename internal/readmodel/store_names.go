package readmodel

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Names of the local cache that the entry points use without importing the store (d4b).
// The store keeps aliases for them.

// Types of custom fields (CustomFieldView.Type) that asagao shows and edits.
const (
	CFText      = "text"
	CFNumber    = "number"
	CFEnum      = "enum"
	CFMultiEnum = "multi_enum"
	CFDate      = "date"
	CFPeople    = "people"
)

// Keys of the local cache's sync state that the entry points read or write.
const (
	// KeyMeGID is the gid of the user the local data was synced with ("" before the first
	// sync).
	KeyMeGID = "me_gid"
	// KeyCollapsed + scope: the TUI's collapsed groups of a list ("my", "inbox", or a
	// project gid).
	KeyCollapsed = "ui:collapsed:"
)

// ResetResult says what a reset with a backup did.
type ResetResult struct {
	Unsent     int    `json:"unsent"`                // unsent changes deleted
	BackupPath string `json:"backup_path,omitempty"` // "" when there was nothing to keep
}

// AnyUnsent lets a reset delete every unsent change, however many there are.
const AnyUnsent int64 = -1

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".heic": true, ".bmp": true}

// IsImageName reports whether a file name looks like an image.
func IsImageName(name string) bool { return imageExts[strings.ToLower(filepath.Ext(name))] }

// inlineAsset matches images pasted into task descriptions and comments, which the API
// returns as links like https://app.asana.com/app/asana/-/get_asset?asset_id=1220507679870013
// (the asset id is the attachment gid).
var inlineAsset = regexp.MustCompile(`https://app\.asana\.com/app/asana/-/get_asset\?[^\s)\]>]*?asset_id=(\d+)[^\s)\]>]*`)

// InlineAssets returns the attachment gids of images embedded in text, in order.
func InlineAssets(text string) []string {
	var gids []string
	for _, m := range inlineAsset.FindAllStringSubmatch(text, -1) {
		gids = append(gids, m[1])
	}
	return gids
}

// ReplaceInlineAssets replaces each embedded image link with repl(gid).
func ReplaceInlineAssets(text string, repl func(gid string) string) string {
	return inlineAsset.ReplaceAllStringFunc(text, func(link string) string {
		return repl(inlineAsset.FindStringSubmatch(link)[1])
	})
}
