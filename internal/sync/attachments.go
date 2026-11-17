package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/termsafe"
	"github.com/amasok/asagao/internal/xdg"
)

// maxAttachmentSize guards against huge downloads (Asana's free plan limit is 100MB per file).
const maxAttachmentSize = 100 << 20

// DownloadAttachment returns a local path for an attachment of a task, downloading it on
// first use. Once downloaded, the file is reused (and is available offline). Attachments
// not yet in the store (e.g. images pasted into the description) are looked up first, see
// downloadUnknown.
func (s *Syncer) DownloadAttachment(ctx context.Context, taskGID, gid string) (string, error) {
	a, err := s.Store.GetAttachment(ctx, gid)
	if errors.Is(err, store.ErrNotFound) {
		return s.downloadUnknown(ctx, gid)
	} else if err != nil {
		return "", err
	}
	dir := s.cacheDir()
	path := filepath.Join(dir, gid+"-"+safeName(a.Name))
	if a.LocalPath != "" {
		if _, err := os.Stat(a.LocalPath); err == nil {
			if a.LocalPath == path || filepath.Base(a.LocalPath) == filepath.Base(path) {
				return a.LocalPath, nil
			}
			// Downloaded by an older version under a name with spaces: rename it so the
			// path can be used as a Markdown link (editor image plugins need that).
			if err := os.MkdirAll(dir, 0o700); err == nil {
				if err := os.Rename(a.LocalPath, path); err == nil {
					// The file is usable even if the path cannot be recorded now; the
					// check below finds it next time.
					_ = s.Store.SetAttachmentPath(ctx, gid, path)
					return path, nil
				}
			}
			return a.LocalPath, nil
		}
	}
	// Already in the cache under its usual name (e.g. the path could not be recorded).
	if _, err := os.Stat(path); err == nil {
		_ = s.Store.SetAttachmentPath(ctx, gid, path)
		return path, nil
	}
	if s.API == nil {
		return "", errors.New("オフラインのためダウンロードできません")
	}
	fresh, err := s.API.Attachment(ctx, gid) // download_url expires quickly; get a new one
	if err != nil {
		return "", err
	}
	if fresh.DownloadURL == "" {
		return "", fmt.Errorf("%s はダウンロードできません（%s 上のファイル）", a.Name, fresh.Host)
	}
	if err := s.saveDownload(ctx, fresh.DownloadURL, a.Name, path); err != nil {
		return "", err
	}
	return path, s.Store.SetAttachmentPath(ctx, gid, path)
}

func (s *Syncer) cacheDir() string {
	if s.CacheDir != "" {
		return s.CacheDir
	}
	return filepath.Join(xdg.CacheDir(), "attachments")
}

// saveDownload downloads url to path (through a temporary file).
func (s *Syncer) saveDownload(ctx context.Context, url, name, path string) error {
	download := s.Download
	if download == nil {
		download = httpDownload
	}
	data, err := download(ctx, url)
	if err != nil {
		return fmt.Errorf("%s のダウンロード: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var digits = regexp.MustCompile(`^[0-9]+$`)

// downloadUnknown handles an attachment that is not in the store, such as an image pasted
// into a description (often copied from another task). It is recorded under the task Asana
// says it belongs to when that task is in the store, and then downloaded as usual.
// Otherwise it is not recorded (it must not appear among the attachments of a task it does
// not belong to), but it is still downloaded to the cache so it can be shown; offline, the
// cached file is used.
func (s *Syncer) downloadUnknown(ctx context.Context, gid string) (string, error) {
	if !digits.MatchString(gid) {
		return "", fmt.Errorf("画像の gid が正しくありません: %q", gid)
	}
	dir := s.cacheDir()
	// Downloaded before (attachments do not change): no API call, and it works offline.
	if p := cachedFile(dir, gid); p != "" {
		return p, nil
	}
	if s.API == nil {
		return "", errors.New("オフラインのため画像を取得できません")
	}
	meta, err := s.API.Attachment(ctx, gid)
	if err != nil {
		return "", fmt.Errorf("画像 %s の取得: %w", gid, err)
	}
	if meta.GID != gid {
		// recording it would not make gid known, and the download below would ask again
		return "", fmt.Errorf("画像 %s の取得: 別の添付 (%s) が返されました", gid, termsafe.String(meta.GID))
	}
	// not read through the store, so make the text safe for the terminal here (it ends up
	// in error messages shown in the TUI)
	meta.Name, meta.Host = termsafe.String(meta.Name), termsafe.String(meta.Host)
	if parent := s.cachedParent(ctx, meta); parent != "" {
		if err := s.Store.UpsertAttachment(ctx, parent, *meta); err != nil {
			return "", err
		}
		return s.DownloadAttachment(ctx, parent, gid)
	}
	path := filepath.Join(dir, gid+"-"+safeName(meta.Name))
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if meta.DownloadURL == "" {
		return "", fmt.Errorf("%s はダウンロードできません（%s 上のファイル）", meta.Name, meta.Host)
	}
	if err := s.saveDownload(ctx, meta.DownloadURL, meta.Name, path); err != nil {
		return "", err
	}
	return path, nil
}

// cachedParent returns the gid of the task an attachment belongs to in Asana, if that task
// is in the store, or "".
func (s *Syncer) cachedParent(ctx context.Context, a *model.Attachment) string {
	if a.Parent == nil || a.Parent.GID == "" {
		return ""
	}
	if _, err := s.Store.GetTask(ctx, a.Parent.GID); err != nil {
		return ""
	}
	return a.Parent.GID
}

// cachedFile returns a file downloaded earlier for attachment gid ("<gid>-name"), or "".
func cachedFile(dir, gid string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, gid+"-") && !strings.HasSuffix(n, ".tmp") {
			return filepath.Join(dir, n)
		}
	}
	return ""
}

// unsafeChars are replaced in cached file names: path separators, control characters, and
// whitespace / brackets, which would break the path when used as a Markdown link target.
var unsafeChars = regexp.MustCompile(`[/\\:\x00-\x1f\s()<>\[\]]`)

func safeName(name string) string {
	name = unsafeChars.ReplaceAllString(name, "_")
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

var downloadClient = &http.Client{Timeout: 2 * time.Minute}

func httpDownload(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttachmentSize {
		return nil, errors.New("ファイルが大きすぎます（100MB 超）")
	}
	return data, nil
}
