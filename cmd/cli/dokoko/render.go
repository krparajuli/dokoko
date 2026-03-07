package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── State pane ────────────────────────────────────────────────────────────────

func (m model) renderStatePane(w, h int, color lipgloss.Color) string {
	focused := m.focus == focusState
	title := lipgloss.NewStyle().Bold(true).Foreground(color).Render("State")
	indicator := ""
	if focused {
		indicator = lipgloss.NewStyle().Foreground(color).Render(" ●")
	}
	header := title + indicator + dimStyle.Render(" [r]")

	body := ""
	if m.vpReady {
		body = m.stateVP.View()
	}
	inner := header + "\n" + body
	return paneStyle(focused, color).Width(w-2).Height(h-2).Render(inner)
}

func (m model) renderStateContent() string {
	var sb strings.Builder
	switch m.activeTab {
	case tabImages:
		st := m.mgr.ImageState()
		req, act, fail, abn := st.Summary()
		writeStateSummary(&sb, req, act, fail, abn)
		writeStateItems(&sb, "Requested", reqStyle, fmtSlice(st.Requested()))
		writeStateItems(&sb, "Active", actStyle, fmtSlice(st.Active()))
		writeStateItems(&sb, "Failed", failStyle, fmtSlice(st.Failed()))
	case tabContainers:
		st := m.mgr.ContainerState()
		req, act, fail, abn := st.Summary()
		writeStateSummary(&sb, req, act, fail, abn)
		writeStateItems(&sb, "Requested", reqStyle, fmtSlice(st.Requested()))
		writeStateItems(&sb, "Active", actStyle, fmtSlice(st.Active()))
		writeStateItems(&sb, "Failed", failStyle, fmtSlice(st.Failed()))
	case tabVolumes:
		st := m.mgr.VolumeState()
		req, act, fail, abn := st.Summary()
		writeStateSummary(&sb, req, act, fail, abn)
		writeStateItems(&sb, "Requested", reqStyle, fmtSlice(st.Requested()))
		writeStateItems(&sb, "Active", actStyle, fmtSlice(st.Active()))
		writeStateItems(&sb, "Failed", failStyle, fmtSlice(st.Failed()))
	case tabNetworks:
		st := m.mgr.NetworkState()
		req, act, fail, abn := st.Summary()
		writeStateSummary(&sb, req, act, fail, abn)
		writeStateItems(&sb, "Requested", reqStyle, fmtSlice(st.Requested()))
		writeStateItems(&sb, "Active", actStyle, fmtSlice(st.Active()))
		writeStateItems(&sb, "Failed", failStyle, fmtSlice(st.Failed()))
	case tabExecs:
		st := m.mgr.ExecState()
		req, act, fail, abn := st.Summary()
		writeStateSummary(&sb, req, act, fail, abn)
		writeStateItems(&sb, "Requested", reqStyle, fmtSlice(st.Requested()))
		writeStateItems(&sb, "Active", actStyle, fmtSlice(st.Active()))
		writeStateItems(&sb, "Failed", failStyle, fmtSlice(st.Failed()))
	}
	return sb.String()
}

func writeStateSummary(sb *strings.Builder, req, act, fail, abn int) {
	sb.WriteString(dimStyle.Render(fmt.Sprintf(
		"req=%-3d  act=%-3d  fail=%-3d  abn=%-3d", req, act, fail, abn,
	)) + "\n\n")
}

func writeStateItems(sb *strings.Builder, label string, style lipgloss.Style, lines []string) {
	sb.WriteString(style.Render(fmt.Sprintf("── %s (%d) ──", label, len(lines))) + "\n")
	if len(lines) == 0 {
		sb.WriteString(dimStyle.Render("  (empty)") + "\n")
	}
	cap := 15
	if len(lines) > cap {
		lines = lines[len(lines)-cap:]
	}
	for _, l := range lines {
		sb.WriteString(l + "\n")
	}
	sb.WriteString("\n")
}

func fmtSlice[T any](items []T) []string {
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("  %v", item)
	}
	return lines
}

// ── Store pane ────────────────────────────────────────────────────────────────

func (m model) renderStorePane(w, h int, color lipgloss.Color) string {
	focused := m.focus == focusStore
	title := lipgloss.NewStyle().Bold(true).Foreground(color).Render("Store")
	indicator := ""
	if focused {
		indicator = lipgloss.NewStyle().Foreground(color).Render(" ●")
	}
	header := title + indicator + dimStyle.Render(" [s]")

	body := ""
	if m.vpReady {
		body = m.storeVP.View()
	}
	inner := header + "\n" + body
	return paneStyle(focused, color).Width(w-2).Height(h-2).Render(inner)
}

func (m model) renderStoreContent() string {
	var sb strings.Builder
	switch m.activeTab {
	case tabImages:
		records := m.mgr.Images().Store().All()
		sort.Slice(records, func(i, j int) bool {
			return records[i].UpdatedAt.After(records[j].UpdatedAt)
		})
		sb.WriteString(dimStyle.Render(fmt.Sprintf("total: %d images", len(records))) + "\n\n")
		for _, r := range records {
			status := statusStyle(string(r.Status))
			tags := strings.Join(r.RepoTags, ", ")
			if tags == "" {
				tags = "<untagged>"
			}
			sb.WriteString(fmt.Sprintf("  %s  %-14s  %-8s  %s\n",
				status, r.ShortID, fmtBytes(r.Size), trunc(tags, 40)))
		}
	case tabContainers:
		sb.WriteString(dimStyle.Render("No persistent store for containers.\n\n"))
		sb.WriteString(infoStyle.Render("Use State pane or List action to see live containers."))
	case tabVolumes:
		records := m.mgr.Volumes().Store().All()
		sort.Slice(records, func(i, j int) bool {
			return records[i].UpdatedAt.After(records[j].UpdatedAt)
		})
		sb.WriteString(dimStyle.Render(fmt.Sprintf("total: %d volumes", len(records))) + "\n\n")
		for _, r := range records {
			status := statusStyle(string(r.Status))
			sb.WriteString(fmt.Sprintf("  %s  %-30s  %s\n",
				status, trunc(r.Name, 30), r.Driver))
		}
	case tabNetworks:
		records := m.mgr.Networks().Store().All()
		sort.Slice(records, func(i, j int) bool {
			return records[i].UpdatedAt.After(records[j].UpdatedAt)
		})
		sb.WriteString(dimStyle.Render(fmt.Sprintf("total: %d networks", len(records))) + "\n\n")
		for _, r := range records {
			status := statusStyle(string(r.Status))
			sb.WriteString(fmt.Sprintf("  %s  %-14s  %-25s  %s\n",
				status, r.ShortID, trunc(r.Name, 25), r.Driver))
		}
	case tabExecs:
		sb.WriteString(dimStyle.Render("No persistent store for execs.\n\n"))
		sb.WriteString(infoStyle.Render("Use State pane or Inspect action to view exec instances."))
	}
	return sb.String()
}

func statusStyle(status string) string {
	switch status {
	case "present":
		return presentStyle.Render("●")
	case "deleted", "deleted_out_of_band":
		return deletedStyle.Render("✗")
	case "errored":
		return errStyle.Render("!")
	case "active":
		return actStyle.Render("▶")
	case "failed":
		return failStyle.Render("✗")
	case "abandoned":
		return abnStyle.Render("·")
	case "requested":
		return reqStyle.Render("…")
	default:
		return dimStyle.Render("?")
	}
}

// ── Logs pane ─────────────────────────────────────────────────────────────────

func (m model) renderLogsPane(w, h int, color lipgloss.Color) string {
	focused := m.focus == focusLogs
	title := lipgloss.NewStyle().Bold(true).Foreground(colorGray).Render("Logs")
	indicator := ""
	if focused {
		indicator = lipgloss.NewStyle().Foreground(color).Render(" ●")
	}
	header := title + indicator + dimStyle.Render(" [l]")

	body := ""
	if m.vpReady {
		body = m.logsVP.View()
	}
	inner := header + "\n" + body
	return paneStyle(focused, color).Width(w-2).Height(h-2).Render(inner)
}
