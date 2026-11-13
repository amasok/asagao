package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

type imagesReadyMsg struct {
	paths []string
	err   error
}

// viewImages downloads (or reuses cached copies of) the selected task's image attachments
// and then hands the terminal to the image viewer.
func (m *Model) viewImages() tea.Cmd {
	d := m.detail
	if d == nil {
		return nil
	}
	var images []store.AttachmentView
	for _, a := range d.Attachments {
		if a.IsImage {
			images = append(images, a)
		}
	}
	if len(images) == 0 {
		if d.DetailsFetchedAt == "" {
			m.status = "添付ファイルはまだ取得していません（オンラインでタスクを開くと取得します）"
		} else {
			m.status = "画像の添付はありません"
		}
		return nil
	}
	if m.deps.ViewImages == nil {
		m.status = "画像を表示できません"
		return nil
	}
	syncer := m.syncer
	if syncer == nil {
		syncer = &sync.Syncer{Store: m.deps.Store} // offline: cached files only
	}
	m.status = fmt.Sprintf("画像を準備しています（%d 件）…", len(images))
	return func() tea.Msg {
		var paths []string
		var errs []string
		for _, a := range images {
			p, err := syncer.DownloadAttachment(m.ctx, a.GID)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			if _, err := os.Stat(p); err == nil {
				paths = append(paths, p)
			}
		}
		var err error
		if len(errs) > 0 {
			err = errors.New(strings.Join(errs, " / "))
		}
		return imagesReadyMsg{paths: paths, err: err}
	}
}

func (m *Model) onImagesReady(msg imagesReadyMsg) tea.Cmd {
	if len(msg.paths) == 0 {
		m.status = "画像を取得できませんでした"
		if msg.err != nil {
			m.status += ": " + msg.err.Error()
		}
		return nil
	}
	m.status = ""
	if msg.err != nil {
		m.status = "一部の画像を取得できませんでした: " + msg.err.Error()
	}
	return m.deps.ViewImages(msg.paths)
}

func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n>>10)
	case n > 0:
		return fmt.Sprintf("%dB", n)
	}
	return ""
}
