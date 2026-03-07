package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	dockervolume "github.com/docker/docker/api/types/volume"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Async op dispatch ─────────────────────────────────────────────────────────

func (m model) makeCmd(opIdx int, vals []string) tea.Cmd {
	tab := m.activeTab
	mgr := m.mgr
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		v := func(i int) string {
			if vals != nil && i < len(vals) {
				return vals[i]
			}
			return ""
		}
		var text string
		switch tab {
		case tabImages:
			text = runImageOp(ctx, mgr, opIdx, v)
		case tabContainers:
			text = runContainerOp(ctx, mgr, opIdx, v)
		case tabVolumes:
			text = runVolumeOp(ctx, mgr, opIdx, v)
		case tabNetworks:
			text = runNetworkOp(ctx, mgr, opIdx, v)
		case tabExecs:
			text = runExecOp(ctx, mgr, opIdx, v)
		default:
			text = "unknown tab"
		}
		return opResultMsg{text}
	}
}

func runImageOp(ctx context.Context, mgr *dockermanager.Manager, opIdx int, v func(int) string) string {
	switch opIdx {
	case 0: // Pull
		if _, err := mgr.Images().Pull(ctx, v(0), dockerimage.PullOptions{Platform: v(1)}); err != nil {
			return "Error: " + err.Error()
		}
		return "Pull dispatched: " + v(0)
	case 1: // Remove
		if _, err := mgr.Images().Remove(ctx, v(0), dockerimage.RemoveOptions{Force: true}); err != nil {
			return "Error: " + err.Error()
		}
		return "Remove dispatched: " + v(0)
	case 2: // Tag
		if _, err := mgr.Images().Tag(ctx, v(0), v(1)); err != nil {
			return "Error: " + err.Error()
		}
		return fmt.Sprintf("Tagged %s → %s", v(0), v(1))
	case 3: // List
		res := <-mgr.Images().List(ctx, dockerimage.ListOptions{})
		if res.Err != nil {
			return "Error: " + res.Err.Error()
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d image(s)\n\n", len(res.Images))
		for _, img := range res.Images {
			tags := strings.Join(img.RepoTags, ", ")
			if tags == "" {
				tags = "<untagged>"
			}
			fmt.Fprintf(&sb, "  %-20s  %s\n", trunc(img.ID, 20), tags)
		}
		return sb.String()
	case 4: // Inspect
		res := <-mgr.Images().Inspect(ctx, v(0))
		if res.Err != nil {
			return "Error: " + res.Err.Error()
		}
		i := res.Info
		return fmt.Sprintf("ID:      %s\nOS:      %s\nArch:    %s\nSize:    %s\nTags:    %s",
			i.ID, i.Os, i.Architecture, fmtBytes(i.Size), strings.Join(i.RepoTags, ", "))
	case 5: // Refresh
		if err := mgr.Images().Refresh(ctx); err != nil {
			return "Error: " + err.Error()
		}
		return "Image store refreshed."
	}
	return "unknown op"
}

func runContainerOp(ctx context.Context, mgr *dockermanager.Manager, opIdx int, v func(int) string) string {
	switch opIdx {
	case 0: // Create
		cfg := &dockercontainer.Config{Image: v(0)}
		if _, err := mgr.Containers().Create(ctx, v(1), cfg, nil, nil); err != nil {
			return "Error: " + err.Error()
		}
		return fmt.Sprintf("Container created (image=%s name=%s)", v(0), v(1))
	case 1: // Start
		if _, err := mgr.Containers().Start(ctx, v(0), dockercontainer.StartOptions{}); err != nil {
			return "Error: " + err.Error()
		}
		return "Start dispatched: " + v(0)
	case 2: // Stop
		if _, err := mgr.Containers().Stop(ctx, v(0), dockercontainer.StopOptions{}); err != nil {
			return "Error: " + err.Error()
		}
		return "Stop dispatched: " + v(0)
	case 3: // Remove
		if _, err := mgr.Containers().Remove(ctx, v(0), dockercontainer.RemoveOptions{Force: true}); err != nil {
			return "Error: " + err.Error()
		}
		return "Remove dispatched: " + v(0)
	case 4: // List
		res := <-mgr.Containers().List(ctx, dockercontainer.ListOptions{All: true})
		if res.Err != nil {
			return "Error: " + res.Err.Error()
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d container(s)\n\n", len(res.Containers))
		for _, c := range res.Containers {
			name := ""
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			fmt.Fprintf(&sb, "  %-14s  %-20s  %s\n", trunc(c.ID, 14), trunc(name, 20), c.State)
		}
		return sb.String()
	case 5: // Inspect
		res := <-mgr.Containers().Inspect(ctx, v(0))
		if res.Err != nil {
			return "Error: " + res.Err.Error()
		}
		c := res.Info
		img := ""
		if c.Config != nil {
			img = c.Config.Image
		}
		status := ""
		if c.State != nil {
			status = c.State.Status
		}
		return fmt.Sprintf("ID:     %s\nName:   %s\nImage:  %s\nStatus: %s\nPlatform: %s",
			trunc(c.ID, 20), c.Name, img, status, c.Platform)
	}
	return "unknown op"
}

func runVolumeOp(ctx context.Context, mgr *dockermanager.Manager, opIdx int, v func(int) string) string {
	switch opIdx {
	case 0: // Create
		if _, err := mgr.Volumes().Create(ctx, dockervolume.CreateOptions{Name: v(0), Driver: v(1)}); err != nil {
			return "Error: " + err.Error()
		}
		return "Volume created: " + v(0)
	case 1: // Remove
		if _, err := mgr.Volumes().Remove(ctx, v(0), false); err != nil {
			return "Error: " + err.Error()
		}
		return "Volume removed: " + v(0)
	case 2: // Prune
		if _, err := mgr.Volumes().Prune(ctx, dockerfilters.Args{}); err != nil {
			return "Error: " + err.Error()
		}
		return "Volume prune dispatched."
	case 3: // List
		res := <-mgr.Volumes().List(ctx, dockervolume.ListOptions{})
		if res.Err != nil {
			return "Error: " + res.Err.Error()
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d volume(s)\n\n", len(res.Response.Volumes))
		for _, vol := range res.Response.Volumes {
			fmt.Fprintf(&sb, "  %-30s  %s\n", trunc(vol.Name, 30), vol.Driver)
		}
		return sb.String()
	case 4: // Inspect
		res := <-mgr.Volumes().Inspect(ctx, v(0))
		if res.Err != nil {
			return "Error: " + res.Err.Error()
		}
		vol := res.Volume
		return fmt.Sprintf("Name:   %s\nDriver: %s\nMount:  %s\nScope:  %s",
			vol.Name, vol.Driver, vol.Mountpoint, vol.Scope)
	case 5: // Refresh
		if err := mgr.Volumes().Refresh(ctx); err != nil {
			return "Error: " + err.Error()
		}
		return "Volume store refreshed."
	}
	return "unknown op"
}

func runNetworkOp(ctx context.Context, mgr *dockermanager.Manager, opIdx int, v func(int) string) string {
	switch opIdx {
	case 0: // Create
		if _, err := mgr.Networks().Create(ctx, v(0), dockertypes.NetworkCreate{Driver: v(1)}); err != nil {
			return "Error: " + err.Error()
		}
		return "Network created: " + v(0)
	case 1: // Remove
		if _, err := mgr.Networks().Remove(ctx, v(0)); err != nil {
			return "Error: " + err.Error()
		}
		return "Network removed: " + v(0)
	case 2: // Prune
		if _, err := mgr.Networks().Prune(ctx, dockerfilters.Args{}); err != nil {
			return "Error: " + err.Error()
		}
		return "Network prune dispatched."
	case 3: // List
		res := <-mgr.Networks().List(ctx, dockertypes.NetworkListOptions{})
		if res.Err != nil {
			return "Error: " + res.Err.Error()
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d network(s)\n\n", len(res.Networks))
		for _, net := range res.Networks {
			fmt.Fprintf(&sb, "  %-14s  %-25s  %s\n", trunc(net.ID, 14), trunc(net.Name, 25), net.Driver)
		}
		return sb.String()
	case 4: // Inspect
		res := <-mgr.Networks().Inspect(ctx, v(0), dockertypes.NetworkInspectOptions{})
		if res.Err != nil {
			return "Error: " + res.Err.Error()
		}
		net := res.Network
		return fmt.Sprintf("ID:      %s\nName:    %s\nDriver:  %s\nScope:   %s\nIPv6:    %v",
			trunc(net.ID, 20), net.Name, net.Driver, net.Scope, net.EnableIPv6)
	case 5: // Refresh
		if err := mgr.Networks().Refresh(ctx); err != nil {
			return "Error: " + err.Error()
		}
		return "Network store refreshed."
	}
	return "unknown op"
}

func runExecOp(ctx context.Context, mgr *dockermanager.Manager, opIdx int, v func(int) string) string {
	switch opIdx {
	case 0: // Create
		cmd := strings.Fields(v(1))
		if len(cmd) == 0 {
			cmd = []string{"/bin/sh"}
		}
		cfg := dockertypes.ExecConfig{Cmd: cmd, AttachStdout: true, AttachStderr: true}
		if _, err := mgr.Exec().Create(ctx, v(0), cfg); err != nil {
			return "Error: " + err.Error()
		}
		return "Exec created in container: " + v(0)
	case 1: // Start
		if _, err := mgr.Exec().Start(ctx, v(0), dockertypes.ExecStartCheck{Detach: true}); err != nil {
			return "Error: " + err.Error()
		}
		return "Exec started: " + v(0)
	case 2: // Inspect
		res := <-mgr.Exec().Inspect(ctx, v(0))
		if res.Err != nil {
			return "Error: " + res.Err.Error()
		}
		e := res.Info
		return fmt.Sprintf("ID:        %s\nContainer: %s\nRunning:   %v\nExitCode:  %d",
			trunc(e.ExecID, 20), trunc(e.ContainerID, 20), e.Running, e.ExitCode)
	}
	return "unknown op"
}
