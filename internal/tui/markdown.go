package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	glamourstyles "charm.land/glamour/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/imageview"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/sync"
)

// Descriptions and comments are rendered as Markdown (converted from Asana's rich text).
// Images appear where they were in the text: drawn inline with kitty Unicode placeholders
// when the terminal supports it (Deps.InlineImages), otherwise as an "[画像N]" line.

// --- markdown ---

// textMarkdown returns Markdown for a description or comment, preferring the rich text.
func textMarkdown(htmlText, plain string) string {
	if htmlText != "" {
		return richtext.ToMarkdown(htmlText)
	}
	plain = readmodel.ReplaceInlineAssets(plain, func(gid string) string {
		return "\n\n" + richtext.ImageToken(gid) + "\n\n"
	})
	return richtext.FromPlain(plain)
}

type mdKey struct {
	text  string
	width int
	dark  bool
}

// markdown renders Markdown to styled text that fits width (cached).
func (m *Model) markdown(md string, width int) string {
	if md == "" {
		return ""
	}
	key := mdKey{md, width, m.dark}
	if s, ok := m.mdCache[key]; ok {
		return s
	}
	cfg := glamourstyles.DarkStyleConfig
	if !m.dark {
		cfg = glamourstyles.LightStyleConfig
	}
	zero := uint(0)
	cfg.Document.Margin = &zero // the pane already has padding
	cfg.Document.BlockPrefix, cfg.Document.BlockSuffix = "", ""
	out := md
	if r, err := glamour.NewTermRenderer(glamour.WithStyles(cfg), glamour.WithWordWrap(width)); err == nil {
		if s, err := r.Render(md); err == nil {
			out = strings.Trim(s, "\n")
		}
	}
	if m.mdCache == nil || len(m.mdCache) > 300 {
		m.mdCache = map[mdKey]string{}
	}
	m.mdCache[key] = out
	return out
}

// withImages replaces image-token lines in rendered Markdown with the images.
func (m *Model) withImages(rendered string, width int, number map[string]int) string {
	if !strings.Contains(rendered, "⟦img:") {
		return rendered
	}
	var out []string
	for _, line := range strings.Split(rendered, "\n") {
		plain := ansi.Strip(line)
		match := richtext.ImageTokenRe.FindStringSubmatch(plain)
		if match == nil {
			out = append(out, line)
			continue
		}
		indent := len(plain) - len(strings.TrimLeft(plain, " "))
		pad := strings.Repeat(" ", indent)
		for _, l := range m.imageBlock(match[1], width-indent, number[match[1]]) {
			out = append(out, pad+l)
		}
	}
	return strings.Join(out, "\n")
}

// --- inline images ---

type inlineImage struct {
	id       int // kitty image id (1-255)
	png      []byte
	pxW, pxH int
	wantCols int // size wanted by the last render
	wantRows int
	sentCols int // size of the virtual placement sent to the terminal
	sentRows int
	loading  bool
	err      error
}

type inlineLoadedMsg struct {
	gid      string
	png      []byte
	pxW, pxH int
	err      error
}

// imageBlock returns the lines that show image gid (number n) at most width cells wide.
func (m *Model) imageBlock(gid string, width, n int) []string {
	label := m.st.tag.Render(fmt.Sprintf("[画像%d]", n))
	img := m.inline[gid]
	if !m.deps.InlineImages || img == nil || img.png == nil {
		hint := "（i で表示）"
		switch {
		case m.deps.InlineImages && img != nil && img.loading:
			hint = "（読み込み中…）"
		case m.deps.InlineImages && img != nil && img.err != nil:
			hint = "（表示できません: " + shortError(img.err, max(10, width-ansi.StringWidth(label)-20)) + "）"
		}
		return []string{ansi.Truncate(label+m.st.dim.Render(hint), max(1, width), "…")}
	}
	maxRows := min(imageview.MaxPlaceholderCells, max(4, m.listHeight()-2))
	cw, ch := m.cellW, m.cellH
	cols, rows := imageview.Fit(img.pxW, img.pxH, imageview.Box{Cols: min(width, imageview.MaxPlaceholderCells), Rows: maxRows, CellW: cw, CellH: ch})
	img.wantCols, img.wantRows = cols, rows
	if img.sentCols != cols || img.sentRows != rows {
		m.inlineDirty = true
		return []string{label + m.st.dim.Render("（表示準備中…）")}
	}
	return imageview.PlaceholderLines(img.id, cols, rows)
}

// prepareInline starts loading the images of the current task for inline display.
func (m *Model) prepareInline() tea.Cmd {
	d := m.detail
	if !m.deps.InlineImages || d == nil {
		return nil
	}
	syncer := m.syncer
	if syncer == nil {
		syncer = &sync.Syncer{Store: m.deps.Store, CacheDir: m.deps.CacheDir} // offline: cached files only
	}
	var cmds []tea.Cmd
	for _, gid := range taskImages(d) {
		if img := m.inline[gid]; img != nil && (img.loading || img.png != nil) {
			continue // loading or loaded; failed images are tried again
		}
		if m.inline == nil {
			m.inline = map[string]*inlineImage{}
		}
		m.inline[gid] = &inlineImage{loading: true}
		gid, taskGID := gid, d.GID
		cmds = append(cmds, func() tea.Msg {
			path, err := syncer.DownloadAttachment(m.ctx, taskGID, gid)
			if err != nil {
				return inlineLoadedMsg{gid: gid, err: err}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return inlineLoadedMsg{gid: gid, err: err}
			}
			png, w, h, err := imageview.ToPNG(data)
			if err != nil {
				err = fmt.Errorf("画像として読めません（%s）: %w", filepath.Base(path), err)
			}
			return inlineLoadedMsg{gid: gid, png: png, pxW: w, pxH: h, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (m *Model) onInlineLoaded(msg inlineLoadedMsg) {
	img := m.inline[msg.gid]
	if img == nil {
		img = &inlineImage{}
		m.inline[msg.gid] = img
	}
	img.loading, img.err = false, msg.err
	if msg.err != nil {
		// Keep the error so the image line says why; prepareInline tries again next time.
		if d := m.detail; d != nil {
			for i, g := range taskImages(d) {
				if g == msg.gid {
					m.status = fmt.Sprintf("画像%d を表示できません: %s", i+1, msg.err.Error())
					break
				}
			}
		}
		m.refreshDetailView()
		return
	}
	m.nextImageID = m.nextImageID%255 + 1
	img.id, img.png, img.pxW, img.pxH = m.nextImageID, msg.png, msg.pxW, msg.pxH
	m.refreshDetailView()
}

// shortError is err's message on one line, at most width cells.
func shortError(err error, width int) string {
	msg := strings.Join(strings.Fields(err.Error()), " ")
	return ansi.Truncate(msg, width, "…")
}

// flushInline sends images whose placement size changed, then re-renders the detail so the
// placeholders appear. It is called after every update.
func (m *Model) flushInline() tea.Cmd {
	if !m.inlineDirty {
		return nil
	}
	m.inlineDirty = false
	var seq strings.Builder
	for _, img := range m.inline {
		if img.png == nil || img.wantCols == 0 || (img.sentCols == img.wantCols && img.sentRows == img.wantRows) {
			continue
		}
		seq.WriteString(imageview.KittyVirtual(img.id, img.png, img.wantCols, img.wantRows))
		img.sentCols, img.sentRows = img.wantCols, img.wantRows
	}
	m.refreshDetailView()
	if seq.Len() == 0 {
		return nil
	}
	return tea.Raw(seq.String())
}
