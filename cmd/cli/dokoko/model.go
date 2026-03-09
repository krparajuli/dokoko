package main

import (
	"strings"
	"time"

	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// focus pane constants
const (
	focusActions = 0
	focusState   = 1
	focusStore   = 2
	focusLogs    = 3
)

type model struct {
	mgr  *dockermanager.Manager
	logs *logBuf

	termWidth  int
	termHeight int

	activeTab int // 0-4

	// Pane visibility toggled by: a, r, s, l
	showActions bool
	showState   bool
	showStore   bool
	showLogs    bool

	// Which pane has keyboard focus (cycled with tab/shift+tab)
	focus int

	// Actions pane
	actionCursor int
	formActive   bool
	formInputs   []textinput.Model
	formFocus    int
	formErr      string

	// Op result shown inside actions pane
	showResult bool
	resultText string

	// Viewports for scrollable panes
	stateVP viewport.Model
	storeVP viewport.Model
	logsVP  viewport.Model
	vpReady bool

	confirmingQuit bool
}

type confirmTimeoutMsg struct{}

func newModel(mgr *dockermanager.Manager, lb *logBuf) model {
	return model{
		mgr:         mgr,
		logs:        lb,
		showActions: true,
		showState:   true,
		showStore:   true,
		showLogs:    true,
	}
}

func (m model) Init() tea.Cmd { return doTick() }

func doTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(_ time.Time) tea.Msg { return tickMsg{} })
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.resizeViewports()
		return m, nil
	case tickMsg:
		m.refreshViewportContent()
		return m, doTick()
	case opResultMsg:
		m.resultText = msg.text
		m.showResult = true
		m.formActive = false
		m.formErr = ""
		return m, nil
	case confirmTimeoutMsg:
		m.confirmingQuit = false
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.formActive && len(m.formInputs) > 0 {
		var cmd tea.Cmd
		m.formInputs[m.formFocus], cmd = m.formInputs[m.formFocus].Update(msg)
		return m, cmd
	}
	return m.updateFocusedVP(msg)
}

func (m *model) resizeViewports() {
	l := m.layout()
	stateH := max(l.botH-3, 2)
	storeH := max(l.totalH-3, 2)
	logsH  := max(l.totalH-3, 2)
	stateW := max(l.leftW-4, 10)
	storeW := max(l.midW-4, 10)
	logsW  := max(l.rightW-4, 10)

	if !m.vpReady {
		m.stateVP = viewport.New(stateW, stateH)
		m.storeVP = viewport.New(storeW, storeH)
		m.logsVP  = viewport.New(logsW, logsH)
		m.vpReady = true
	} else {
		m.stateVP.Width  = stateW
		m.stateVP.Height = stateH
		m.storeVP.Width  = storeW
		m.storeVP.Height = storeH
		m.logsVP.Width   = logsW
		m.logsVP.Height  = logsH
	}
	m.refreshViewportContent()
}

func (m *model) refreshViewportContent() {
	if !m.vpReady {
		return
	}
	m.stateVP.SetContent(m.renderStateContent())
	m.storeVP.SetContent(m.renderStoreContent())
	lines := m.logs.Lines()
	m.logsVP.SetContent(strings.Join(lines, "\n"))
	m.logsVP.GotoBottom()
}

func (m model) updateFocusedVP(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.vpReady {
		return m, nil
	}
	var cmd tea.Cmd
	switch m.focus {
	case focusState:
		m.stateVP, cmd = m.stateVP.Update(msg)
	case focusStore:
		m.storeVP, cmd = m.storeVP.Update(msg)
	case focusLogs:
		m.logsVP, cmd = m.logsVP.Update(msg)
	}
	return m, cmd
}
