package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	dockervolumeactor "dokoko.ai/dokoko/internal/docker/volumes/actor"
	dockervolumestate "dokoko.ai/dokoko/internal/docker/volumes/state"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	dockervolume "github.com/docker/docker/api/types/volume"
)

// ── Async read-only commands ──────────────────────────────────────────────────

func cmdList(act *dockervolumeactor.Actor) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res := <-act.List(ctx, dockervolume.ListOptions{})
		if res.Err != nil {
			return readResultMsg{"Error: " + res.Err.Error()}
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%d volume(s)\n\n", len(res.Response.Volumes)))
		for _, v := range res.Response.Volumes {
			sb.WriteString(fmt.Sprintf("  %-40s  %s\n", trunc(v.Name, 40), v.Driver))
		}
		return readResultMsg{sb.String()}
	}
}

func cmdInspect(act *dockervolumeactor.Actor, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res := <-act.Inspect(ctx, name)
		if res.Err != nil {
			return readResultMsg{"Error: " + res.Err.Error()}
		}
		v := res.Volume
		return readResultMsg{fmt.Sprintf(
			"Name:       %s\nDriver:     %s\nScope:      %s\nMountpoint: %s",
			v.Name, v.Driver, v.Scope, v.Mountpoint,
		)}
	}
}

// ── Live state renderer ───────────────────────────────────────────────────────

// renderState builds the scrollable content string for the right pane viewport.
func renderState(st *dockervolumestate.State) string {
	req := st.Requested()
	act := st.Active()
	fail := st.Failed()
	abn := st.Abandoned()

	var sb strings.Builder
	sb.WriteString(dimStyle.Render(fmt.Sprintf(
		"req=%-3d  act=%-3d  fail=%-3d  abn=%-3d",
		len(req), len(act), len(fail), len(abn),
	)) + "\n\n")

	write := func(label string, style lipgloss.Style, lines []string) {
		sb.WriteString(style.Render("── "+label+" ──") + "\n")
		if len(lines) == 0 {
			sb.WriteString(dimStyle.Render("  (empty)") + "\n")
		}
		for _, l := range lines {
			sb.WriteString(l + "\n")
		}
		sb.WriteString("\n")
	}

	// Requested
	reqLines := make([]string, len(req))
	for i, c := range req {
		reqLines[i] = fmt.Sprintf("  %-8s  %-7s  %s", seqID(c.ID), c.Op, c.VolumeName)
	}
	write(fmt.Sprintf("Requested (%d)", len(req)), reqStyle, reqLines)

	// Active — last 20
	actSlice := act
	if len(actSlice) > 20 {
		actSlice = actSlice[len(actSlice)-20:]
	}
	actLines := make([]string, len(actSlice))
	for i, r := range actSlice {
		actLines[i] = fmt.Sprintf("  %-8s  %-7s  %s",
			seqID(r.Change.ID), r.Change.Op, trunc(r.VolumeName, 38))
	}
	write(fmt.Sprintf("Active (%d)", len(act)), actStyle, actLines)

	// Failed — last 10
	failSlice := fail
	if len(failSlice) > 10 {
		failSlice = failSlice[len(failSlice)-10:]
	}
	failLines := make([]string, len(failSlice))
	for i, r := range failSlice {
		failLines[i] = fmt.Sprintf("  %-8s  %-7s  %-22s  %s",
			seqID(r.Change.ID), r.Change.Op, trunc(r.Change.VolumeName, 22), trunc(r.Err, 32))
	}
	write(fmt.Sprintf("Failed (%d)", len(fail)), failStyle, failLines)

	// Abandoned — last 10
	abnSlice := abn
	if len(abnSlice) > 10 {
		abnSlice = abnSlice[len(abnSlice)-10:]
	}
	abnLines := make([]string, len(abnSlice))
	for i, r := range abnSlice {
		abnLines[i] = fmt.Sprintf("  %-8s  %-7s  %-22s  %s",
			seqID(r.Change.ID), r.Change.Op, trunc(r.Change.VolumeName, 22), trunc(r.Reason, 32))
	}
	write(fmt.Sprintf("Abandoned (%d)", len(abn)), abnStyle, abnLines)

	return sb.String()
}
