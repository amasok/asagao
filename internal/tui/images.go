package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/domain/rich"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
)

type imagesReadyMsg struct {
	paths []string
	err   error
}

// taskImages lists the images of a task in reading order: images pasted into the
// description, then into comments, then other image attachments.
func taskImages(d *readmodel.TaskDetail) []string {
	var gids []string
	seen := map[string]bool{}
	add := func(gid string) {
		if !seen[gid] {
			seen[gid] = true
			gids = append(gids, gid)
		}
	}
	textImages := func(htmlText, plain string) []string {
		if htmlText != "" {
			return richtext.Images(htmlText)
		}
		return rich.InlineAssets(plain)
	}
	for _, g := range textImages(d.HTMLNotes, d.Notes) {
		add(g)
	}
	for _, st := range d.Stories {
		if st.Type == "comment" {
			for _, g := range textImages(st.HTMLText, st.Text) {
				add(g)
			}
		}
	}
	for _, a := range d.Attachments {
		if a.IsImage {
			add(a.GID)
		}
	}
	return gids
}

// viewImages downloads (or reuses cached copies of) the selected task's images and then
// hands the terminal to the image viewer.
func (m *Model) viewImages() tea.Cmd {
	d := m.detail.task
	if d == nil {
		return nil
	}
	images := taskImages(d)
	if len(images) == 0 {
		if d.DetailsFetchedAt == "" {
			m.status = "添付ファイルはまだ取得していません（オンラインでタスクを開くと取得します）"
		} else {
			m.status = "画像はありません"
		}
		return nil
	}
	if m.deps.ViewImages == nil {
		m.status = "画像を表示できません"
		return nil
	}
	syncer := m.syncer
	if syncer == nil {
		syncer = m.deps.LocalSyncer() // offline: cached files only
		syncer.CacheDir = m.deps.CacheDir
	}
	taskGID := d.GID
	m.status = fmt.Sprintf("画像を準備しています（%d 件）…", len(images))
	return func() tea.Msg {
		var paths []string
		var errs []string
		for _, gid := range images {
			p, err := syncer.DownloadAttachment(m.ctx, taskGID, gid)
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
