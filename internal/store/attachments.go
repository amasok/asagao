package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/amasok/asagao/internal/asana"
)

type AttachmentView struct {
	GID       string `json:"gid"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	ViewURL   string `json:"view_url,omitempty"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
	IsImage   bool   `json:"is_image"`
	// LocalPath is set once the file has been downloaded (viewable offline).
	LocalPath string `json:"local_path,omitempty"`
}

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".heic": true, ".bmp": true}

// IsImageName reports whether a file name looks like an image.
func IsImageName(name string) bool { return imageExts[strings.ToLower(filepath.Ext(name))] }

// ReplaceAttachments stores a task's attachments, keeping local paths of files already
// downloaded and files attached offline ("local:" gids) that are not uploaded yet.
func (s *Store) ReplaceAttachments(ctx context.Context, taskGID string, atts []asana.Attachment) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		paths := map[string]string{}
		rows, err := tx.QueryContext(ctx, "SELECT gid, local_path FROM attachments WHERE parent_gid = ?", taskGID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var gid, p string
			if err := rows.Scan(&gid, &p); err != nil {
				rows.Close()
				return err
			}
			paths[gid] = p
		}
		rows.Close()
		if _, err := tx.ExecContext(ctx, "DELETE FROM attachments WHERE parent_gid = ? AND gid NOT LIKE 'local:%'", taskGID); err != nil {
			return err
		}
		for _, a := range atts {
			if _, err := tx.ExecContext(ctx, `INSERT INTO attachments
				(gid, parent_gid, name, resource_subtype, host, view_url, permanent_url, size, created_at, local_path)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				a.GID, taskGID, a.Name, a.ResourceSubtype, a.Host, a.ViewURL, a.PermanentURL, a.Size,
				formatTime(a.CreatedAt), paths[a.GID]); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetAttachmentPath records where an attachment was downloaded.
func (s *Store) SetAttachmentPath(ctx context.Context, gid, path string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE attachments SET local_path = ? WHERE gid = ?", path, gid)
	return err
}

// GetAttachment returns one attachment.
func (s *Store) GetAttachment(ctx context.Context, gid string) (*AttachmentView, error) {
	atts, err := s.queryAttachments(ctx, "WHERE gid = ?", gid)
	if err != nil {
		return nil, err
	}
	if len(atts) == 0 {
		return nil, fmt.Errorf("attachment %s: %w", gid, ErrNotFound)
	}
	return &atts[0], nil
}

func (s *Store) taskAttachments(ctx context.Context, taskGID string) ([]AttachmentView, error) {
	return s.queryAttachments(ctx, "WHERE parent_gid = ? ORDER BY created_at", taskGID)
}

func (s *Store) queryAttachments(ctx context.Context, where string, args ...any) ([]AttachmentView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT gid, name, host, view_url, size, created_at, local_path FROM attachments `+where, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	atts := []AttachmentView{}
	for rows.Next() {
		var a AttachmentView
		if err := rows.Scan(&a.GID, &a.Name, &a.Host, &a.ViewURL, &a.Size, &a.CreatedAt, &a.LocalPath); err != nil {
			return nil, err
		}
		a.IsImage = IsImageName(a.Name)
		atts = append(atts, a)
	}
	return atts, rows.Err()
}

// UpsertAttachment stores one attachment of a task (e.g. an inline image found in its notes).
func (s *Store) UpsertAttachment(ctx context.Context, taskGID string, a asana.Attachment) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO attachments
		(gid, parent_gid, name, resource_subtype, host, view_url, permanent_url, size, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gid) DO UPDATE SET name = excluded.name, host = excluded.host, view_url = excluded.view_url,
			permanent_url = excluded.permanent_url, size = excluded.size`,
		a.GID, taskGID, a.Name, a.ResourceSubtype, a.Host, a.ViewURL, a.PermanentURL, a.Size, formatTime(a.CreatedAt))
	return err
}

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
