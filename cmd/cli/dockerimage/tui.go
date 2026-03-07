package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	dockerimageactor "dokoko.ai/dokoko/internal/docker/images/actor"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	dockerimage "github.com/docker/docker/api/types/image"
)

// ── Model ─────────────────────────────────────────────────────────────────────

type model struct {
	actor *dockerimageactor.Actor
	state *dockerimagestate.State

	termWidth  int
	termHeight int

	leftView leftView
	activeOp int
	inputs   []textinput.Model
	focusIdx int
	formErr  string

	resultText string

	rightVP viewport.Model
	vpReady bool
}

func newModel(act *dockerimageactor.Actor, st *dockerimagestate.State) model {
	return model{actor: act, state: st}
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m model) Init() tea.Cmd { return doTick() }

func doTick() tea.Cmd {
	return tea.Tick(300*time.Millisecond, func(_ time.Time) tea.Msg { return tickMsg{} })
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		vpW := msg.Width - leftOuterW - paneGap - 4
		if vpW < 10 {
			vpW = 10
		}
		vpH := msg.Height - 6
		if vpH < 4 {
			vpH = 4
		}
		if !m.vpReady {
			m.rightVP = viewport.New(vpW, vpH)
			m.vpReady = true
		} else {
			m.rightVP.Width = vpW
			m.rightVP.Height = vpH
		}
		m.rightVP.SetContent(renderState(m.state))
		return m, nil

	case tickMsg:
		m.rightVP.SetContent(renderState(m.state))
		return m, doTick()

	case readResultMsg:
		m.resultText = msg.text
		m.leftView = viewResult
		return m, nil

	case goMenuMsg:
		m.leftView = viewMenu
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Forward non-key messages to focused input when in form view.
	if m.leftView == viewForm && len(m.inputs) > 0 {
		var cmd tea.Cmd
		m.inputs[m.focusIdx], cmd = m.inputs[m.focusIdx].Update(msg)
		return m, cmd
	}

	if m.vpReady {
		var cmd tea.Cmd
		m.rightVP, cmd = m.rightVP.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.leftView {
	case viewMenu:
		return m.handleMenuKey(msg)
	case viewForm:
		return m.handleFormKey(msg)
	case viewResult:
		return m.handleResultKey(msg)
	}
	return m, nil
}

func (m model) handleMenuKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "1", "2", "3", "4", "5", "6":
		op := int(msg.String()[0] - '0')
		m.activeOp = op
		m.formErr = ""
		// List has no inputs — run immediately.
		if op == 4 {
			m.resultText = "Loading…"
			m.leftView = viewResult
			return m, cmdList(m.actor)
		}
		m.inputs = makeInputs(opInputDescs[op])
		m.focusIdx = 0
		if len(m.inputs) > 0 {
			m.inputs[0].Focus()
		}
		m.leftView = viewForm
		return m, textinput.Blink
	case "up", "k", "down", "j", "pgup", "pgdown":
		if m.vpReady {
			var cmd tea.Cmd
			m.rightVP, cmd = m.rightVP.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m model) handleFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.leftView = viewMenu
		return m, nil
	case "tab", "shift+tab":
		if len(m.inputs) > 1 {
			m.inputs[m.focusIdx].Blur()
			if msg.String() == "tab" {
				m.focusIdx = (m.focusIdx + 1) % len(m.inputs)
			} else {
				m.focusIdx = (m.focusIdx - 1 + len(m.inputs)) % len(m.inputs)
			}
			m.inputs[m.focusIdx].Focus()
		}
		return m, textinput.Blink
	case "enter":
		if m.focusIdx < len(m.inputs)-1 {
			m.inputs[m.focusIdx].Blur()
			m.focusIdx++
			m.inputs[m.focusIdx].Focus()
			return m, textinput.Blink
		}
		return m.submitForm()
	}
	var cmd tea.Cmd
	m.inputs[m.focusIdx], cmd = m.inputs[m.focusIdx].Update(msg)
	return m, cmd
}

func (m model) handleResultKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter", "q":
		m.leftView = viewMenu
		m.resultText = ""
	}
	return m, nil
}

// submitForm validates and dispatches the active operation.
func (m model) submitForm() (tea.Model, tea.Cmd) {
	vals := make([]string, len(m.inputs))
	for i, inp := range m.inputs {
		vals[i] = strings.TrimSpace(inp.Value())
	}
	for i, d := range opInputDescs[m.activeOp] {
		if d.required && vals[i] == "" {
			m.formErr = d.label + " is required"
			return m, nil
		}
	}
	m.formErr = ""

	switch m.activeOp {
	case 1: // Pull
		platform := ""
		if len(vals) > 1 {
			platform = vals[1]
		}
		_, err := m.actor.Pull(context.Background(), vals[0], dockerimage.PullOptions{Platform: platform})
		if err != nil {
			m.formErr = err.Error()
			return m, nil
		}
	case 2: // Remove
		_, err := m.actor.Remove(context.Background(), vals[0], dockerimage.RemoveOptions{Force: true})
		if err != nil {
			m.formErr = err.Error()
			return m, nil
		}
	case 3: // Tag
		_, err := m.actor.Tag(context.Background(), vals[0], vals[1])
		if err != nil {
			m.formErr = err.Error()
			return m, nil
		}
	case 5: // Inspect
		m.resultText = "Loading…"
		m.leftView = viewResult
		return m, cmdInspect(m.actor, vals[0])
	case 6: // Exists
		m.resultText = "Loading…"
		m.leftView = viewResult
		return m, cmdExists(m.actor, vals[0])
	}

	m.leftView = viewMenu
	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m model) View() string {
	if m.termWidth == 0 {
		return "Initialising…"
	}
	left := m.renderLeft()
	right := m.renderRight()
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", paneGap), right)
}

func (m model) renderLeft() string {
	var inner string
	switch m.leftView {
	case viewMenu:
		inner = m.renderMenu()
	case viewForm:
		inner = m.renderForm()
	case viewResult:
		inner = m.renderOpResult()
	}
	return lipgloss.NewStyle().
		Width(leftContentW).
		Height(m.termHeight - 2).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		Render(inner)
}

func (m model) renderMenu() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Docker Image Manager") + "\n\n")
	sb.WriteString(dimStyle.Render("Select an operation:\n\n"))
	for i := 1; i <= 6; i++ {
		sb.WriteString(fmt.Sprintf("  [%d]  %s\n", i, opNames[i]))
	}
	sb.WriteString("\n" + dimStyle.Render("  [q]  Quit") + "\n\n")
	sb.WriteString(dimStyle.Render("↑↓/j/k scroll state pane"))
	return sb.String()
}

func (m model) renderForm() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(opNames[m.activeOp]) + "\n\n")
	for i, inp := range m.inputs {
		label := opInputDescs[m.activeOp][i].label
		if i == m.focusIdx {
			sb.WriteString(activeStyle.Render("> "+label) + "\n")
		} else {
			sb.WriteString(dimStyle.Render("  "+label) + "\n")
		}
		sb.WriteString(inp.View() + "\n\n")
	}
	if m.formErr != "" {
		sb.WriteString(errStyle.Render("✗ "+m.formErr) + "\n\n")
	}
	sb.WriteString(dimStyle.Render("[Enter] submit  [Tab] next  [Esc] back"))
	return sb.String()
}

func (m model) renderOpResult() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(opNames[m.activeOp]) + "\n\n")
	sb.WriteString(m.resultText + "\n\n")
	sb.WriteString(dimStyle.Render("[Esc] back"))
	return sb.String()
}

func (m model) renderRight() string {
	rightOuterW := m.termWidth - leftOuterW - paneGap
	if rightOuterW < 20 {
		rightOuterW = 20
	}
	rightContentW := rightOuterW - 4

	header := titleStyle.Render("Live State") + "\n\n"
	vpView := ""
	if m.vpReady {
		vpView = m.rightVP.View()
	}

	return lipgloss.NewStyle().
		Width(rightContentW).
		Height(m.termHeight - 2).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		Render(header + vpView)
}
