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
)

// maxAttachmentSize guards against huge downloads (Asana's free plan limit is 100MB per file).
const maxAttachmentSize = 100 << 20

// DownloadAttachment returns a local path for an attachment, downloading it on first use.
// Once downloaded, the file is reused (and is available offline).
func (s *Syncer) DownloadAttachment(ctx context.Context, gid string) (string, error) {
	a, err := s.Store.GetAttachment(ctx, gid)
	if err != nil {
		return "", err
	}
	if a.LocalPath != "" {
		if _, err := os.Stat(a.LocalPath); err == nil {
			return a.LocalPath, nil
		}
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
	dir := s.CacheDir
	if dir == "" {
		dir = filepath.Join(config.CacheDir(), "attachments")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, gid+"-"+safeName(a.Name))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, s.Store.SetAttachmentPath(ctx, gid, path)
}

var unsafeChars = regexp.MustCompile(`[/\\:\x00-\x1f]`)

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
