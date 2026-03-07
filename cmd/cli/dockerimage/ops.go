package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	dockerimageactor "dokoko.ai/dokoko/internal/docker/images/actor"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	dockerimage "github.com/docker/docker/api/types/image"
)

// ── Async read-only commands ──────────────────────────────────────────────────

func cmdList(act *dockerimageactor.Actor) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res := <-act.List(ctx, dockerimage.ListOptions{})
		if res.Err != nil {
			return readResultMsg{"Error: " + res.Err.Error()}
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%d image(s)\n\n", len(res.Images)))
		for _, img := range res.Images {
			id := img.ID
			if len(id) > 19 {
				id = id[:19]
			}
			tags := strings.Join(img.RepoTags, ", ")
			if tags == "" {
				tags = "<untagged>"
			}
			sb.WriteString(fmt.Sprintf("  %s  %s\n", id, tags))
		}
		return readResultMsg{sb.String()}
	}
}

func cmdInspect(act *dockerimageactor.Actor, ref string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res := <-act.Inspect(ctx, ref)
		if res.Err != nil {
			return readResultMsg{"Error: " + res.Err.Error()}
		}
		i := res.Info
		return readResultMsg{fmt.Sprintf(
			"ID:      %s\nOS:      %s\nArch:    %s\nSize:    %d bytes\nTags:    %s\nCreated: %s",
			i.ID, i.Os, i.Architecture, i.Size,
			strings.Join(i.RepoTags, ", "),
			i.Created,
		)}
	}
}

func cmdExists(act *dockerimageactor.Actor, ref string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res := <-act.Exists(ctx, ref)
		if res.Err != nil {
			return readResultMsg{"Error: " + res.Err.Error()}
		}
		if res.Present {
			return readResultMsg{ref + "\n\nPresent in local store."}
		}
		return readResultMsg{ref + "\n\nNot found in local store."}
	}
}

// ── Live state renderer ───────────────────────────────────────────────────────

// renderState builds the scrollable content string for the right pane viewport.
func renderState(st *dockerimagestate.State) string {
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
		reqLines[i] = fmt.Sprintf("  %-8s  %-7s  %s", seqID(c.ID), c.Op, c.ImageRef)
	}
	write(fmt.Sprintf("Requested (%d)", len(req)), reqStyle, reqLines)

	// Active — last 20
	actSlice := act
	if len(actSlice) > 20 {
		actSlice = actSlice[len(actSlice)-20:]
	}
	actLines := make([]string, len(actSlice))
	for i, r := range actSlice {
		did := r.DockerID
		if len(did) > 19 {
			did = did[:19]
		}
		if did == "" {
			did = dimStyle.Render("(no id)")
		}
		actLines[i] = fmt.Sprintf("  %-8s  %-7s  %-22s  %s",
			seqID(r.Change.ID), r.Change.Op, trunc(r.Change.ImageRef, 22), did)
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
			seqID(r.Change.ID), r.Change.Op, trunc(r.Change.ImageRef, 22), trunc(r.Err, 32))
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
			seqID(r.Change.ID), r.Change.Op, trunc(r.Change.ImageRef, 22), trunc(r.Reason, 32))
	}
	write(fmt.Sprintf("Abandoned (%d)", len(abn)), abnStyle, abnLines)

	return sb.String()
}

