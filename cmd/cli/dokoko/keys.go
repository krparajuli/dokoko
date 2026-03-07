package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Key handling ──────────────────────────────────────────────────────────────

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	if m.formActive {
		return m.handleFormKey(msg)
	}
	if m.showResult {
		if k == "esc" || k == "enter" || k == "q" {
			m.showResult = false
			m.resultText = ""
		}
		return m, nil
	}

	switch k {
	case "1", "2", "3", "4", "5":
		idx := int(k[0] - '1')
		if idx != m.activeTab {
			m.activeTab = idx
			m.actionCursor = 0
		}
		return m, nil
	case "q":
		return m, tea.Quit
	case "a":
		m.showActions = !m.showActions
		if !m.showActions && m.focus == focusActions {
			m.focus = m.nextFocus(m.focus)
		}
		m.resizeViewports()
		return m, nil
	case "r":
		m.showState = !m.showState
		if !m.showState && m.focus == focusState {
			m.focus = m.nextFocus(m.focus)
		}
		m.resizeViewports()
		return m, nil
	case "s":
		m.showStore = !m.showStore
		if !m.showStore && m.focus == focusStore {
			m.focus = m.nextFocus(m.focus)
		}
		m.resizeViewports()
		return m, nil
	case "l":
		m.showLogs = !m.showLogs
		if !m.showLogs && m.focus == focusLogs {
			m.focus = m.nextFocus(m.focus)
		}
		m.resizeViewports()
		return m, nil
	case "tab":
		m.focus = m.nextFocus(m.focus)
		return m, nil
	case "shift+tab":
		m.focus = m.prevFocus(m.focus)
		return m, nil
	}

	// Actions pane navigation when focused
	if m.focus == focusActions && m.showActions {
		return m.handleActionsNav(k)
	}

	// Hotkeys work from any focus
	ops := tabOps[m.activeTab]
	for idx, op := range ops {
		if k == op.key {
			return m.triggerOp(idx)
		}
	}

	return m.updateFocusedVP(msg)
}

func (m model) handleActionsNav(k string) (tea.Model, tea.Cmd) {
	ops := tabOps[m.activeTab]
	switch k {
	case "up", "k":
		if m.actionCursor > 0 {
			m.actionCursor--
		}
	case "down", "j":
		if m.actionCursor < len(ops)-1 {
			m.actionCursor++
		}
	case "enter":
		if m.actionCursor < len(ops) {
			return m.triggerOp(m.actionCursor)
		}
	default:
		for idx, op := range ops {
			if k == op.key {
				return m.triggerOp(idx)
			}
		}
	}
	return m, nil
}

func (m model) handleFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	switch k {
	case "esc":
		m.formActive = false
		m.formErr = ""
		m.formInputs = nil
		return m, nil
	case "tab", "down":
		if len(m.formInputs) > 1 {
			m.formInputs[m.formFocus].Blur()
			m.formFocus = (m.formFocus + 1) % len(m.formInputs)
			m.formInputs[m.formFocus].Focus()
		}
		return m, textinput.Blink
	case "shift+tab", "up":
		if len(m.formInputs) > 1 {
			m.formInputs[m.formFocus].Blur()
			m.formFocus = (m.formFocus - 1 + len(m.formInputs)) % len(m.formInputs)
			m.formInputs[m.formFocus].Focus()
		}
		return m, textinput.Blink
	case "enter":
		if m.formFocus < len(m.formInputs)-1 {
			m.formInputs[m.formFocus].Blur()
			m.formFocus++
			m.formInputs[m.formFocus].Focus()
			return m, textinput.Blink
		}
		return m.submitForm()
	}
	var cmd tea.Cmd
	m.formInputs[m.formFocus], cmd = m.formInputs[m.formFocus].Update(msg)
	return m, cmd
}

func (m model) triggerOp(opIdx int) (tea.Model, tea.Cmd) {
	ops := tabOps[m.activeTab]
	if opIdx >= len(ops) {
		return m, nil
	}
	op := ops[opIdx]
	m.actionCursor = opIdx
	if len(op.inputs) == 0 {
		return m, m.makeCmd(opIdx, nil)
	}
	l := m.layout()
	w := max(l.leftW-4, 20)
	m.formInputs = makeInputs(op.inputs, w)
	m.formFocus = 0
	m.formInputs[0].Focus()
	m.formActive = true
	m.formErr = ""
	return m, textinput.Blink
}

func (m model) submitForm() (tea.Model, tea.Cmd) {
	ops := tabOps[m.activeTab]
	op := ops[m.actionCursor]
	vals := make([]string, len(m.formInputs))
	for i, inp := range m.formInputs {
		vals[i] = strings.TrimSpace(inp.Value())
	}
	for i, d := range op.inputs {
		if d.required && vals[i] == "" {
			m.formErr = d.label + " is required"
			return m, nil
		}
	}
	m.formErr = ""
	m.formActive = false
	m.formInputs = nil
	return m, m.makeCmd(m.actionCursor, vals)
}

// ── Focus helpers ─────────────────────────────────────────────────────────────

func (m model) visiblePanes() []int {
	var p []int
	if m.showActions {
		p = append(p, focusActions)
	}
	if m.showState {
		p = append(p, focusState)
	}
	if m.showStore {
		p = append(p, focusStore)
	}
	if m.showLogs {
		p = append(p, focusLogs)
	}
	return p
}

func (m model) nextFocus(cur int) int {
	panes := m.visiblePanes()
	if len(panes) == 0 {
		return cur
	}
	for i, p := range panes {
		if p == cur {
			return panes[(i+1)%len(panes)]
		}
	}
	return panes[0]
}

func (m model) prevFocus(cur int) int {
	panes := m.visiblePanes()
	if len(panes) == 0 {
		return cur
	}
	for i, p := range panes {
		if p == cur {
			return panes[(i-1+len(panes))%len(panes)]
		}
	}
	return panes[len(panes)-1]
}
