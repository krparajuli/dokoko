package dockercontainerexecops

import (
	"context"
	"fmt"
	"strings"

	"dokoko.ai/dokoko/internal/docker"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
)

// Ops provides exec-level operations against a Docker daemon.
type Ops struct {
	conn *docker.Connection
	log  *logger.Logger
}

// New constructs an Ops bound to an existing Connection.
func New(conn *docker.Connection, log *logger.Logger) *Ops {
	log.LowTrace("initialising exec ops")
	log.Trace("exec ops bound to connection %p", conn)
	return &Ops{conn: conn, log: log}
}

// Create creates a new exec instance inside the container identified by
// containerID.  The exec instance is not started; call Start or Attach
// to run it.  Returns the exec instance ID on success.
func (o *Ops) Create(ctx context.Context, containerID string, config dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
	o.log.LowTrace("creating exec in container: %s", containerID)
	o.log.Debug("exec create config: cmd=%v user=%q workingDir=%q tty=%v privileged=%v",
		config.Cmd, config.User, config.WorkingDir, config.Tty, config.Privileged)
	o.log.Debug("exec create attach: stdin=%v stdout=%v stderr=%v",
		config.AttachStdin, config.AttachStdout, config.AttachStderr)
	for i, e := range config.Env {
		o.log.Trace("  env[%d]: %s", i, e)
	}

	resp, err := o.conn.Client().ContainerExecCreate(ctx, containerID, config)
	if err != nil {
		o.log.Error("ContainerExecCreate failed for container %q: %v", containerID, err)
		return dockertypes.IDResponse{}, fmt.Errorf("exec create in container %q: %w", containerID, err)
	}

	o.log.Debug("exec instance created: execID=%s", resp.ID)
	o.log.Info("exec created: container=%s execID=%s cmd=%s",
		containerID, resp.ID, strings.Join(config.Cmd, " "))
	return resp, nil
}

// Start runs the exec instance identified by execID without streaming I/O.
// Set config.Detach = true to run the command in background and return
// immediately.  Set config.Detach = false to wait for the command to
// complete (non-interactive, no stdin/stdout capture).
func (o *Ops) Start(ctx context.Context, execID string, config dockertypes.ExecStartCheck) error {
	o.log.LowTrace("starting exec: %s", execID)
	o.log.Debug("exec start options: detach=%v tty=%v", config.Detach, config.Tty)

	if err := o.conn.Client().ContainerExecStart(ctx, execID, config); err != nil {
		o.log.Error("ContainerExecStart failed for %q: %v", execID, err)
		return fmt.Errorf("exec start %q: %w", execID, err)
	}

	o.log.Debug("exec start accepted for %q (detach=%v)", execID, config.Detach)
	o.log.Info("exec started: execID=%s", execID)
	return nil
}

// Attach attaches to the exec instance identified by execID, streaming its
// I/O over a hijacked connection.  config.Tty controls TTY allocation;
// config.Detach should be false (attaching implies non-detached mode).
//
// The caller owns the returned HijackedResponse and must call Close on it
// when finished to release the underlying network connection.
func (o *Ops) Attach(ctx context.Context, execID string, config dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error) {
	o.log.LowTrace("attaching to exec: %s", execID)
	o.log.Debug("exec attach options: tty=%v", config.Tty)

	resp, err := o.conn.Client().ContainerExecAttach(ctx, execID, config)
	if err != nil {
		o.log.Error("ContainerExecAttach failed for %q: %v", execID, err)
		return dockertypes.HijackedResponse{}, fmt.Errorf("exec attach %q: %w", execID, err)
	}

	o.log.Debug("exec attach established: execID=%s", execID)
	o.log.Info("exec attached: execID=%s", execID)
	return resp, nil
}

// Inspect returns the current status of the exec instance identified by execID,
// including whether it is running and its exit code.
func (o *Ops) Inspect(ctx context.Context, execID string) (dockertypes.ContainerExecInspect, error) {
	o.log.LowTrace("inspecting exec: %s", execID)

	resp, err := o.conn.Client().ContainerExecInspect(ctx, execID)
	if err != nil {
		o.log.Error("ContainerExecInspect failed for %q: %v", execID, err)
		return dockertypes.ContainerExecInspect{}, fmt.Errorf("exec inspect %q: %w", execID, err)
	}

	o.log.Debug("exec inspect: execID=%s containerID=%s running=%v exitCode=%d pid=%d",
		resp.ExecID, resp.ContainerID, resp.Running, resp.ExitCode, resp.Pid)
	o.log.Info("exec inspected: execID=%s running=%v exitCode=%d", execID, resp.Running, resp.ExitCode)
	return resp, nil
}

// Resize resizes the TTY of the exec instance identified by execID.
// This is only meaningful for exec instances created with Tty = true.
func (o *Ops) Resize(ctx context.Context, execID string, opts dockercontainer.ResizeOptions) error {
	o.log.LowTrace("resizing exec TTY: %s", execID)
	o.log.Debug("exec resize: height=%d width=%d", opts.Height, opts.Width)

	if err := o.conn.Client().ContainerExecResize(ctx, execID, opts); err != nil {
		o.log.Error("ContainerExecResize failed for %q: %v", execID, err)
		return fmt.Errorf("exec resize %q: %w", execID, err)
	}

	o.log.Debug("exec resize accepted for %q", execID)
	o.log.Info("exec TTY resized: execID=%s height=%d width=%d", execID, opts.Height, opts.Width)
	return nil
}
