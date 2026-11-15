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
	"time"

	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

// maxAttachmentSize guards against huge downloads (Asana's free plan limit is 100MB per file).
const maxAttachmentSize = 100 << 20

// DownloadAttachment returns a local path for an attachment of a task, downloading it on
// first use. Once downloaded, the file is reused (and is available offline). Attachments
// not yet in the store (e.g. images pasted into the description) are looked up first.
func (s *Syncer) DownloadAttachment(ctx context.Context, taskGID, gid string) (string, error) {
	a, err := s.Store.GetAttachment(ctx, gid)
	if errors.Is(err, store.ErrNotFound) && s.API != nil {
		meta, err := s.API.Attachment(ctx, gid)
		if err != nil {
			return "", fmt.Errorf("画像 %s の取得: %w", gid, err)
		}
		if err := s.Store.UpsertAttachment(ctx, taskGID, *meta); err != nil {
			return "", err
		}
		a, err = s.Store.GetAttachment(ctx, gid)
		if err != nil {
			return "", err
		}
	} else if errors.Is(err, store.ErrNotFound) {
		return "", errors.New("オフラインのため画像を取得できません")
	} else if err != nil {
		return "", err
	}
	dir := s.CacheDir
	if dir == "" {
		dir = filepath.Join(config.CacheDir(), "attachments")
	}
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
	download := s.Download
	if download == nil {
		download = httpDownload
	}
	data, err := download(ctx, fresh.DownloadURL)
	if err != nil {
		return "", fmt.Errorf("%s のダウンロード: %w", a.Name, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, s.Store.SetAttachmentPath(ctx, gid, path)
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
