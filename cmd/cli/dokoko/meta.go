package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

// ── Messages ──────────────────────────────────────────────────────────────────

type tickMsg     struct{}
type opResultMsg struct{ text string }

// ── Log buffer ────────────────────────────────────────────────────────────────

type logBuf struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
}

func newLogBuf(max int) *logBuf { return &logBuf{maxLines: max} }

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			b.lines = append(b.lines, line)
		}
	}
	if len(b.lines) > b.maxLines {
		b.lines = b.lines[len(b.lines)-b.maxLines:]
	}
	return len(p), nil
}

func (b *logBuf) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// ── Tab constants ─────────────────────────────────────────────────────────────

const (
	tabImages     = 0
	tabContainers = 1
	tabVolumes    = 2
	tabNetworks   = 3
	tabExecs      = 4
)

var tabNames = [5]string{"Images", "Containers", "Volumes", "Networks", "Execs"}

// ── Operation definitions ─────────────────────────────────────────────────────

type inputDesc struct {
	label    string
	required bool
}

type opDef struct {
	name   string
	key    string
	inputs []inputDesc
}

var tabOps = [5][]opDef{
	tabImages: {
		{name: "Pull",    key: "p", inputs: []inputDesc{{"Image ref (e.g. ubuntu:22.04)", true}, {"Platform (optional)", false}}},
		{name: "Remove",  key: "d", inputs: []inputDesc{{"Image ID or ref", true}}},
		{name: "Tag",     key: "t", inputs: []inputDesc{{"Source", true}, {"Target tag", true}}},
		{name: "List",    key: "L", inputs: nil},
		{name: "Inspect", key: "i", inputs: []inputDesc{{"Image ID or ref", true}}},
		{name: "Refresh", key: "f", inputs: nil},
	},
	tabContainers: {
		{name: "Create",  key: "c", inputs: []inputDesc{{"Image", true}, {"Name (optional)", false}}},
		{name: "Start",   key: "S", inputs: []inputDesc{{"Container ID/name", true}}},
		{name: "Stop",    key: "X", inputs: []inputDesc{{"Container ID/name", true}}},
		{name: "Remove",  key: "d", inputs: []inputDesc{{"Container ID/name", true}}},
		{name: "List",    key: "L", inputs: nil},
		{name: "Inspect", key: "i", inputs: []inputDesc{{"Container ID/name", true}}},
	},
	tabVolumes: {
		{name: "Create",  key: "c", inputs: []inputDesc{{"Volume name", true}, {"Driver (optional)", false}}},
		{name: "Remove",  key: "d", inputs: []inputDesc{{"Volume name", true}}},
		{name: "Prune",   key: "P", inputs: nil},
		{name: "List",    key: "L", inputs: nil},
		{name: "Inspect", key: "i", inputs: []inputDesc{{"Volume name", true}}},
		{name: "Refresh", key: "f", inputs: nil},
	},
	tabNetworks: {
		{name: "Create",  key: "c", inputs: []inputDesc{{"Network name", true}, {"Driver (optional)", false}}},
		{name: "Remove",  key: "d", inputs: []inputDesc{{"Network ID/name", true}}},
		{name: "Prune",   key: "P", inputs: nil},
		{name: "List",    key: "L", inputs: nil},
		{name: "Inspect", key: "i", inputs: []inputDesc{{"Network ID/name", true}}},
		{name: "Refresh", key: "f", inputs: nil},
	},
	tabExecs: {
		{name: "Create",  key: "c", inputs: []inputDesc{{"Container ID", true}, {"Command", true}}},
		{name: "Start",   key: "S", inputs: []inputDesc{{"Exec ID", true}}},
		{name: "Inspect", key: "i", inputs: []inputDesc{{"Exec ID", true}}},
	},
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	colorCyan    = lipgloss.Color("86")
	colorMagenta = lipgloss.Color("213")
	colorYellow  = lipgloss.Color("214")
	colorGreen   = lipgloss.Color("46")
	colorRed     = lipgloss.Color("196")
	colorBlue    = lipgloss.Color("33")
	colorGray    = lipgloss.Color("240")
	colorWhite   = lipgloss.Color("255")
	colorOrange  = lipgloss.Color("208")
	tabColors = [5]lipgloss.Color{colorCyan, colorGreen, colorYellow, colorMagenta, colorOrange}

	dimStyle      = lipgloss.NewStyle().Foreground(colorGray)
	errStyle      = lipgloss.NewStyle().Foreground(colorRed)
	boldStyle     = lipgloss.NewStyle().Bold(true)
	reqStyle      = lipgloss.NewStyle().Foreground(colorYellow)
	actStyle      = lipgloss.NewStyle().Foreground(colorGreen)
	failStyle     = lipgloss.NewStyle().Foreground(colorRed)
	abnStyle      = lipgloss.NewStyle().Foreground(colorGray)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(colorWhite).Background(lipgloss.Color("238"))
	presentStyle  = lipgloss.NewStyle().Foreground(colorGreen)
	deletedStyle  = lipgloss.NewStyle().Foreground(colorRed)
	infoStyle     = lipgloss.NewStyle().Foreground(colorBlue)
)

func tabStyle(active bool, color lipgloss.Color) lipgloss.Style {
	s := lipgloss.NewStyle().Padding(0, 2)
	if active {
		return s.Bold(true).Foreground(color).Underline(true)
	}
	return s.Foreground(colorGray)
}

func paneStyle(focused bool, color lipgloss.Color) lipgloss.Style {
	s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if focused {
		return s.BorderForeground(color)
	}
	return s.BorderForeground(colorGray)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func makeInputs(descs []inputDesc, width int) []textinput.Model {
	models := make([]textinput.Model, len(descs))
	for i, d := range descs {
		ti := textinput.New()
		ti.Width = max(width-6, 10)
		ti.Placeholder = d.label
		models[i] = ti
	}
	return models
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func seqID(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 {
		return "#" + id[i+1:]
	}
	return id
}

func fmtBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1fGB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.1fMB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1fKB", float64(n)/kb)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
