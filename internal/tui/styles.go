package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// styles adapt to the terminal background (detected via tea.RequestBackgroundColor).
type styles struct {
	accent, muted, danger, warn, ok color.Color

	header, headerScope, headerFilter lipgloss.Style
	paneTitle                         lipgloss.Style
	selected, selectedBlur            lipgloss.Style
	sectionHeader                     lipgloss.Style
	dim, overdue, today, done, tag    lipgloss.Style
	footer, footerKey, errorText      lipgloss.Style
	label                             lipgloss.Style
	border                            lipgloss.Border
}

func newStyles(dark bool, accent string) styles {
	ld := lipgloss.LightDark(dark)
	s := styles{
		accent: ld(lipgloss.Color("#d1407a"), lipgloss.Color("#f06a99")), // Asana-ish coral pink
		muted:  ld(lipgloss.Color("#8a8a8a"), lipgloss.Color("#6c6c6c")),
		danger: ld(lipgloss.Color("#c62828"), lipgloss.Color("#ff6b6b")),
		warn:   ld(lipgloss.Color("#b26a00"), lipgloss.Color("#ffb74d")),
		ok:     ld(lipgloss.Color("#2e7d32"), lipgloss.Color("#81c784")),
		border: lipgloss.RoundedBorder(),
	}
	if accent != "" {
		s.accent = lipgloss.Color(accent)
	}
	fg := ld(lipgloss.Color("#1a1a1a"), lipgloss.Color("#e6e6e6"))
	selBg := ld(lipgloss.Color("#f8d7e3"), lipgloss.Color("#4a2433"))
	blurBg := ld(lipgloss.Color("#e8e8e8"), lipgloss.Color("#303030"))

	s.header = lipgloss.NewStyle().Bold(true).Foreground(s.accent)
	s.headerScope = lipgloss.NewStyle().Bold(true).Foreground(fg)
	s.headerFilter = lipgloss.NewStyle().Foreground(s.muted)
	s.paneTitle = lipgloss.NewStyle().Bold(true).Foreground(s.accent)
	s.selected = lipgloss.NewStyle().Background(selBg).Foreground(fg).Bold(true)
	s.selectedBlur = lipgloss.NewStyle().Background(blurBg).Foreground(fg)
	s.sectionHeader = lipgloss.NewStyle().Bold(true).Foreground(s.muted)
	s.dim = lipgloss.NewStyle().Foreground(s.muted)
	s.overdue = lipgloss.NewStyle().Foreground(s.danger)
	s.today = lipgloss.NewStyle().Foreground(s.warn)
	s.done = lipgloss.NewStyle().Foreground(s.muted).Strikethrough(true)
	s.tag = lipgloss.NewStyle().Foreground(s.ok)
	s.footer = lipgloss.NewStyle().Foreground(s.muted)
	s.footerKey = lipgloss.NewStyle().Foreground(fg).Bold(true)
	s.errorText = lipgloss.NewStyle().Foreground(s.danger)
	s.label = lipgloss.NewStyle().Foreground(s.muted)
	return s
}
