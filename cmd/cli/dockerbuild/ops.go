package main

import (
	"fmt"
	"strings"

	dockerbuildstate "dokoko.ai/dokoko/internal/docker/builds/state"
	"github.com/charmbracelet/lipgloss"
)

// ── Live state renderer ───────────────────────────────────────────────────────

// renderState builds the scrollable content string for the right pane viewport.
func renderState(st *dockerbuildstate.State) string {
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
		tags := c.Tags
		if tags == "" {
			tags = "(untagged)"
		}
		reqLines[i] = fmt.Sprintf("  %-8s  %-12s  %s", seqID(c.ID), c.Op, trunc(tags, 28))
	}
	write(fmt.Sprintf("Requested (%d)", len(req)), reqStyle, reqLines)

	// Active — last 20
	actSlice := act
	if len(actSlice) > 20 {
		actSlice = actSlice[len(actSlice)-20:]
	}
	actLines := make([]string, len(actSlice))
	for i, r := range actSlice {
		tags := r.Change.Tags
		if tags == "" {
			tags = "(untagged)"
		}
		imageID := r.ImageID
		if len(imageID) > 19 {
			imageID = imageID[:19]
		}
		if imageID == "" {
			imageID = dimStyle.Render("(no id)")
		}
		actLines[i] = fmt.Sprintf("  %-8s  %-12s  %-22s  %s",
			seqID(r.Change.ID), r.Change.Op, trunc(tags, 22), imageID)
	}
	write(fmt.Sprintf("Active (%d)", len(act)), actStyle, actLines)

	// Failed — last 10
	failSlice := fail
	if len(failSlice) > 10 {
		failSlice = failSlice[len(failSlice)-10:]
	}
	failLines := make([]string, len(failSlice))
	for i, r := range failSlice {
		tags := r.Change.Tags
		if tags == "" {
			tags = "(untagged)"
		}
		failLines[i] = fmt.Sprintf("  %-8s  %-12s  %-18s  %s",
			seqID(r.Change.ID), r.Change.Op, trunc(tags, 18), trunc(r.Err, 28))
	}
	write(fmt.Sprintf("Failed (%d)", len(fail)), failStyle, failLines)

	// Abandoned — last 10
	abnSlice := abn
	if len(abnSlice) > 10 {
		abnSlice = abnSlice[len(abnSlice)-10:]
	}
	abnLines := make([]string, len(abnSlice))
	for i, r := range abnSlice {
		tags := r.Change.Tags
		if tags == "" {
			tags = "(untagged)"
		}
		abnLines[i] = fmt.Sprintf("  %-8s  %-12s  %-18s  %s",
			seqID(r.Change.ID), r.Change.Op, trunc(tags, 18), trunc(r.Reason, 28))
	}
	write(fmt.Sprintf("Abandoned (%d)", len(abn)), abnStyle, abnLines)

	return sb.String()
}
