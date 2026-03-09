package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Layout ────────────────────────────────────────────────────────────────────

type layoutCalc struct {
	totalH int // usable height (termHeight - header - footer)
	leftW  int // left column width  (Actions + State stacked)
	midW   int // middle column width (Store)
	rightW int // right column width  (Logs)
	topH   int // actions pane outer height within left column
	botH   int // state pane outer height within left column
}

func (m *model) layout() layoutCalc {
	if m.termWidth == 0 || m.termHeight == 0 {
		return layoutCalc{}
	}
	totalH := m.termHeight - 2 // header + footer

	hasLeft := m.showActions || m.showState

	// Collect visible columns in order: left, mid, right
	var visible []int
	if hasLeft       { visible = append(visible, 0) }
	if m.showStore   { visible = append(visible, 1) }
	if m.showLogs    { visible = append(visible, 2) }

	widths := [3]int{}
	if n := len(visible); n > 0 {
		base := m.termWidth / n
		rem  := m.termWidth - base*n
		for i, c := range visible {
			widths[c] = base
			if i == n-1 {
				widths[c] += rem // last column absorbs remainder
			}
		}
	}

	// Left column: split actions (top) and state (bottom) vertically
	var topH, botH int
	switch {
	case m.showActions && m.showState:
		topH = totalH / 2
		botH = totalH - topH
	case m.showActions:
		topH = totalH
	default:
		botH = totalH
	}

	return layoutCalc{
		totalH: totalH,
		leftW:  widths[0],
		midW:   widths[1],
		rightW: widths[2],
		topH:   topH,
		botH:   botH,
	}
}

// ── Main View ─────────────────────────────────────────────────────────────────

func (m model) View() string {
	if m.termWidth == 0 {
		return "Initialising…"
	}
	l := m.layout()
	color := tabColors[m.activeTab]

	var cols []string

	// Left column: Actions (top) stacked on State (bottom)
	if m.showActions || m.showState {
		var leftPanes []string
		if m.showActions {
			leftPanes = append(leftPanes, m.renderActionsPane(l.leftW, l.topH, color))
		}
		if m.showState {
			leftPanes = append(leftPanes, m.renderStatePane(l.leftW, l.botH, color))
		}
		cols = append(cols, lipgloss.JoinVertical(lipgloss.Left, leftPanes...))
	}

	// Middle column: Store
	if m.showStore {
		cols = append(cols, m.renderStorePane(l.midW, l.totalH, color))
	}

	// Right column: Logs
	if m.showLogs {
		cols = append(cols, m.renderLogsPane(l.rightW, l.totalH, color))
	}

	body := ""
	if len(cols) > 0 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	}

	parts := []string{m.renderHeader(), body, m.renderFooter()}
	if m.confirmingQuit {
		banner := lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).Bold(true).
			Width(m.termWidth).
			Render("  ⚠  Press Ctrl-C again to exit.")
		parts = append(parts, banner)
	}
	return strings.Join(parts, "\n")
}

// ── Header ────────────────────────────────────────────────────────────────────

func (m model) renderHeader() string {
	brand := lipgloss.NewStyle().Bold(true).Foreground(colorCyan).Render(" dokoko ")
	sep := dimStyle.Render("│")

	var tabs []string
	for i, name := range tabNames {
		label := fmt.Sprintf("%d:%s", i+1, name)
		inner := tabStyle(m.activeTab == i, tabColors[i]).Render(label)
		tabs = append(tabs, " "+inner+" ")
	}

	tabBar := strings.Join(tabs, dimStyle.Render("·"))
	right := dimStyle.Render(" [tab] focus  [q] quit ")

	w := m.termWidth - lipgloss.Width(brand) - lipgloss.Width(sep) -
		lipgloss.Width(tabBar) - lipgloss.Width(right)
	if w < 0 {
		w = 0
	}
	spacer := strings.Repeat(" ", w)

	return lipgloss.NewStyle().
		Background(lipgloss.Color("234")).
		Width(m.termWidth).
		Render(brand + sep + tabBar + spacer + right)
}

// ── Actions pane ──────────────────────────────────────────────────────────────

func (m model) renderActionsPane(w, h int, color lipgloss.Color) string {
	focused := m.focus == focusActions
	title := lipgloss.NewStyle().Bold(true).Foreground(color).Render("Actions")
	indicator := ""
	if focused {
		indicator = lipgloss.NewStyle().Foreground(color).Render(" ●")
	}
	header := title + indicator + dimStyle.Render(" [a]")

	innerW := max(w-4, 10)

	var body string
	if m.showResult {
		body = m.renderResultOverlay(innerW)
	} else if m.formActive {
		body = m.renderForm()
	} else {
		body = m.renderActionList(innerW)
	}

	inner := header + "\n" + body
	return paneStyle(focused, color).Width(w-2).Height(h-2).Render(inner)
}

func (m model) renderActionList(w int) string {
	ops := tabOps[m.activeTab]
	var sb strings.Builder
	for i, op := range ops {
		line := fmt.Sprintf("[%s] %s", op.key, op.name)
		if i == m.actionCursor {
			sb.WriteString(selectedStyle.Width(w).Render(line))
		} else {
			sb.WriteString(dimStyle.Render("    "+op.name) + dimStyle.Render("  "+op.key))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	if m.focus == focusActions {
		sb.WriteString(dimStyle.Render("↑↓/enter  [tab] next pane"))
	} else {
		sb.WriteString(dimStyle.Render("press key to run op"))
	}
	return sb.String()
}

func (m model) renderForm() string {
	ops := tabOps[m.activeTab]
	op := ops[m.actionCursor]
	color := tabColors[m.activeTab]

	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(color).Render(op.name) + "\n\n")

	for i, inp := range m.formInputs {
		label := op.inputs[i].label
		if i == m.formFocus {
			sb.WriteString(lipgloss.NewStyle().Foreground(colorWhite).Bold(true).Render("> " + label))
		} else {
			sb.WriteString(dimStyle.Render("  " + label))
		}
		sb.WriteString("\n" + inp.View() + "\n\n")
	}

	if m.formErr != "" {
		sb.WriteString(errStyle.Render("✗ "+m.formErr) + "\n\n")
	}
	sb.WriteString(dimStyle.Render("[enter] submit  [tab] next  [esc] cancel"))
	return sb.String()
}

func (m model) renderResultOverlay(_ int) string {
	ops := tabOps[m.activeTab]
	title := ""
	if m.actionCursor < len(ops) {
		title = ops[m.actionCursor].name
	}
	var sb strings.Builder
	sb.WriteString(boldStyle.Render(title) + "\n\n")
	for _, line := range strings.Split(m.resultText, "\n") {
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n" + dimStyle.Render("[esc/enter/q] back"))
	return sb.String()
}

// ── Footer ────────────────────────────────────────────────────────────────────

func (m model) renderFooter() string {
	ops := tabOps[m.activeTab]
	var hints []string
	for _, op := range ops {
		hints = append(hints,
			dimStyle.Render("["+op.key+"]")+
				lipgloss.NewStyle().Foreground(colorGray).Render(op.name))
	}
	opHints := strings.Join(hints, "  ")
	toggles := dimStyle.Render("[a]act  [r]state  [s]store  [l]logs  ")
	tabs := dimStyle.Render("[1-5]tab")
	bar := toggles + tabs + "  " + opHints

	return lipgloss.NewStyle().
		Background(lipgloss.Color("234")).
		Width(m.termWidth).
		Foreground(colorGray).
		Render(bar)
}
