package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

// ── Messages ──────────────────────────────────────────────────────────────────

type tickMsg      struct{}
type readResultMsg struct{ text string }

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	activeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	reqStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	actStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	failStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	abnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

// ── Left-pane view modes ──────────────────────────────────────────────────────

type leftView int

const (
	viewMenu   leftView = iota
	viewForm
	viewResult
)

// ── Op metadata ───────────────────────────────────────────────────────────────

type inputDesc struct {
	label    string
	required bool
}

var (
	opNames = map[int]string{
		1: "Build (local dir)",
		2: "Build (remote URL)",
		3: "Prune Cache",
	}
	opInputDescs = map[int][]inputDesc{
		// 1 — Build from local directory
		1: {
			{"Context Dir", true},
			{"Tags  (comma-separated, blank = untagged)", false},
			{"Dockerfile  (blank = Dockerfile)", false},
			{"Target stage  (blank = all stages)", false},
		},
		// 2 — Build from remote URL (Git repo or HTTP tarball)
		2: {
			{"Remote URL  (git or http tarball)", true},
			{"Tags  (comma-separated, blank = untagged)", false},
			{"Dockerfile  (blank = Dockerfile)", false},
			{"Target stage  (blank = all stages)", false},
		},
		// 3 — Prune build cache: no inputs, runs immediately
		3: {},
	}
)

// ── Layout constants ──────────────────────────────────────────────────────────

const (
	leftContentW = 42               // content width inside left pane (excl. padding+border)
	leftOuterW   = leftContentW + 4 // +2 padding + 2 border
	paneGap      = 1
)

// ── Small helpers ─────────────────────────────────────────────────────────────

// makeInputs creates a slice of textinput.Model from descriptors.
func makeInputs(descs []inputDesc) []textinput.Model {
	models := make([]textinput.Model, len(descs))
	for i, d := range descs {
		ti := textinput.New()
		ti.Width = leftContentW - 2
		ti.Placeholder = d.label
		models[i] = ti
	}
	return models
}

// parseTags splits a comma-separated tag string into individual tags.
func parseTags(s string) []string {
	if s == "" {
		return nil
	}
	var tags []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// seqID extracts the sequence suffix from a change ID (after the last "-").
func seqID(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 {
		return "#" + id[i+1:]
	}
	return id
}

// trunc truncates s to at most n runes, appending "…" if needed.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
