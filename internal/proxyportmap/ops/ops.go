// Package proxyportmapops provides Docker exec operations for discovering TCP
// ports in LISTEN state inside a running container.
package proxyportmapops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	"dokoko.ai/dokoko/internal/docker"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerstdcopy "github.com/docker/docker/pkg/stdcopy"
)

// TtydContainerPort is excluded from scan results (ttyd's own terminal port).
const TtydContainerPort uint16 = 7681

// Ops wraps a Docker connection for port-scan operations.
type Ops struct {
	conn *docker.Connection
	log  *logger.Logger
}

// New returns an Ops bound to an existing Connection.
func New(conn *docker.Connection, log *logger.Logger) *Ops {
	log.LowTrace("initialising proxyportmap ops")
	return &Ops{conn: conn, log: log}
}

// ScanListeningPorts discovers TCP ports in LISTEN state inside the named
// container. It queries all three sources (/proc/net/tcp, /proc/net/tcp6, ss)
// and merges the results. Port 7681 (ttyd) is always excluded.
//
// Docker exec does not propagate the command's exit code as an error, so we
// cannot rely on a failed ss invocation returning an error — instead we always
// try all sources and merge what we find.
func (o *Ops) ScanListeningPorts(ctx context.Context, containerName string) ([]uint16, error) {
	o.log.LowTrace("proxyportmap ops: scanning ports in %s", containerName)

	seen := make(map[uint16]bool)
	var ports []uint16

	add := func(pp []uint16) {
		for _, p := range pp {
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}

	// /proc/net/tcp — IPv4 listeners; always present in Linux containers.
	if out, err := o.execRead(ctx, containerName, []string{"cat", "/proc/net/tcp"}); err == nil {
		add(parseProcNetTCP(out))
	} else {
		o.log.Debug("proxyportmap ops: /proc/net/tcp unavailable in %s: %v", containerName, err)
	}

	// /proc/net/tcp6 — IPv6 listeners; same on-disk format as /proc/net/tcp.
	if out, err := o.execRead(ctx, containerName, []string{"cat", "/proc/net/tcp6"}); err == nil {
		add(parseProcNetTCP(out))
	}

	// ss — best-effort supplement (may not be installed or may not support -H).
	if out, err := o.execRead(ctx, containerName, []string{"ss", "-tlnH"}); err == nil {
		add(parseSS(out))
	}

	filtered := make([]uint16, 0, len(ports))
	for _, p := range ports {
		if p != TtydContainerPort {
			filtered = append(filtered, p)
		}
	}

	o.log.Info("proxyportmap ops: %s → %d listening port(s): %v", containerName, len(filtered), filtered)
	return filtered, nil
}

// ── Scan implementations ──────────────────────────────────────────────────────

// execRead runs cmd inside the container and returns the combined stdout bytes.
// Docker's multiplexed stream is demuxed via dockerstdcopy.StdCopy.
func (o *Ops) execRead(ctx context.Context, containerName string, cmd []string) ([]byte, error) {
	execResp, err := o.conn.Client().ContainerExecCreate(ctx, containerName, dockertypes.ExecConfig{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: false,
	})
	if err != nil {
		return nil, fmt.Errorf("exec create %v in %s: %w", cmd, containerName, err)
	}

	attached, err := o.conn.Client().ContainerExecAttach(ctx, execResp.ID, dockertypes.ExecStartCheck{})
	if err != nil {
		return nil, fmt.Errorf("exec attach in %s: %w", containerName, err)
	}
	defer attached.Close()

	var stdout bytes.Buffer
	if _, err := dockerstdcopy.StdCopy(&stdout, io.Discard, attached.Reader); err != nil {
		return nil, fmt.Errorf("read exec output from %s: %w", containerName, err)
	}
	return stdout.Bytes(), nil
}

// ── Output parsers ────────────────────────────────────────────────────────────

// parseSS extracts listening TCP ports from `ss -tlnH` output.
//
// Two formats exist depending on iproute2 version:
//
//	Old (no Netid column): LISTEN 0 128 0.0.0.0:8080 0.0.0.0:*
//	New (Netid column):    tcp LISTEN 0 128 0.0.0.0:8080 0.0.0.0:*
//
// Old format: fields[0]=="LISTEN", local at fields[3].
// New format: fields[1]=="LISTEN", local at fields[4].
func parseSS(data []byte) []uint16 {
	seen := make(map[uint16]bool)
	var ports []uint16
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		var localIdx int
		switch {
		case len(fields) >= 5 && fields[0] == "LISTEN":
			localIdx = 3 // old format: no Netid column
		case len(fields) >= 6 && fields[1] == "LISTEN":
			localIdx = 4 // new format: Netid column present
		default:
			continue
		}
		if p := portFromAddr(fields[localIdx]); p > 0 && !seen[p] {
			ports = append(ports, p)
			seen[p] = true
		}
	}
	return ports
}

// parseProcNetTCP extracts LISTEN entries from /proc/net/tcp.
// Format: sl  local_address rem_address  st ...
// local_address is "HEXIP:HEXPORT"; state 0A = LISTEN.
func parseProcNetTCP(data []byte) []uint16 {
	seen := make(map[uint16]bool)
	var ports []uint16
	scanner := bufio.NewScanner(bytes.NewReader(data))
	header := true
	for scanner.Scan() {
		if header {
			header = false
			continue // skip the header line
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		if fields[3] != "0A" { // 0A = TCP_LISTEN
			continue
		}
		parts := strings.SplitN(fields[1], ":", 2)
		if len(parts) != 2 {
			continue
		}
		b, err := hex.DecodeString(parts[1])
		if err != nil || len(b) != 2 {
			continue
		}
		p := uint16(b[0])<<8 | uint16(b[1])
		if p > 0 && !seen[p] {
			ports = append(ports, p)
			seen[p] = true
		}
	}
	return ports
}

// portFromAddr extracts the port number from "HOST:PORT" where HOST may be an
// IPv4 address, bracketed IPv6 address, or "*".
func portFromAddr(addr string) uint16 {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return 0
	}
	n, err := strconv.ParseUint(addr[idx+1:], 10, 16)
	if err != nil {
		return 0
	}
	return uint16(n)
}
