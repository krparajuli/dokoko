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

// PortInfo describes a single TCP port found in LISTEN state.
type PortInfo struct {
	Port    uint16
	Process string // program name from ss -p, empty if unknown
}

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
// container. It queries /proc/net/tcp, /proc/net/tcp6, and ss, then resolves
// process names by cross-referencing socket inodes with /proc/[pid]/fd.
//
// Port 7681 (ttyd default) and any port whose process name contains "ttyd"
// are always excluded.
func (o *Ops) ScanListeningPorts(ctx context.Context, containerName string) ([]PortInfo, error) {
	o.log.LowTrace("proxyportmap ops: scanning ports in %s", containerName)

	// seen maps port → index in ports slice, for deduplication that preserves
	// the entry with a non-empty Process.
	seen := make(map[uint16]int)
	var ports []PortInfo

	add := func(pp []PortInfo) {
		for _, info := range pp {
			if idx, exists := seen[info.Port]; exists {
				if ports[idx].Process == "" && info.Process != "" {
					ports[idx].Process = info.Process
				}
			} else {
				seen[info.Port] = len(ports)
				ports = append(ports, info)
			}
		}
	}

	// Collect port→inode mappings so we can resolve process names via /proc.
	portToInode := make(map[uint16]uint64)

	// /proc/net/tcp — IPv4 listeners.
	if out, err := o.execRead(ctx, containerName, []string{"cat", "/proc/net/tcp"}); err == nil {
		add(parseProcNetTCP(out))
		for k, v := range parseProcNetTCPInodes(out) {
			portToInode[k] = v
		}
	} else {
		o.log.Debug("proxyportmap ops: /proc/net/tcp unavailable in %s: %v", containerName, err)
	}

	// /proc/net/tcp6 — IPv6 listeners.
	if out, err := o.execRead(ctx, containerName, []string{"cat", "/proc/net/tcp6"}); err == nil {
		add(parseProcNetTCP(out))
		for k, v := range parseProcNetTCPInodes(out) {
			portToInode[k] = v
		}
	}

	// Resolve process names via /proc inode cross-reference (reliable in any
	// container that runs as root — no CAP_SYS_PTRACE required).
	if procNames := o.resolveProcessNamesViaInodes(ctx, containerName, portToInode); len(procNames) > 0 {
		for i, info := range ports {
			if ports[i].Process == "" {
				if name, ok := procNames[info.Port]; ok {
					ports[i].Process = name
				}
			}
		}
	}

	// ss — best-effort supplement; may not be installed or may lack -p support.
	if out, err := o.execRead(ctx, containerName, []string{"ss", "-tlnHp"}); err == nil {
		add(parseSS(out))
	}

	// Exclude ttyd: by well-known port 7681 and by process name.
	filtered := make([]PortInfo, 0, len(ports))
	for _, info := range ports {
		if info.Port == TtydContainerPort || strings.Contains(info.Process, "ttyd") {
			continue
		}
		filtered = append(filtered, info)
	}

	o.log.Info("proxyportmap ops: %s → %d listening port(s): %v", containerName, len(filtered), filtered)
	return filtered, nil
}

// resolveProcessNamesViaInodes maps port numbers to process names by:
//  1. Running a shell inside the container to list all socket inodes held by
//     each PID (via /proc/[pid]/fd symlinks).
//  2. Cross-referencing with the port→inode map from /proc/net/tcp.
//
// This approach works without CAP_SYS_PTRACE — only root access inside the
// container is needed, which is the default for Docker containers.
func (o *Ops) resolveProcessNamesViaInodes(ctx context.Context, containerName string, portToInode map[uint16]uint64) map[uint16]string {
	if len(portToInode) == 0 {
		return nil
	}

	// One shell invocation: emit "inode<TAB>comm" for every socket fd in /proc.
	// NOTE: use socket:\[*) not socket:[*) — an unclosed bracket expression in
	// a POSIX case pattern is undefined; dash/busybox (Alpine) never match it,
	// silently producing zero output.
	const script = `for pid in /proc/[0-9]*; do` +
		` comm=$(cat "$pid/comm" 2>/dev/null) || continue;` +
		` for fd in "$pid"/fd/*; do` +
		` l=$(readlink "$fd" 2>/dev/null) || continue;` +
		` case $l in socket:\[*)` +
		` inode=${l#socket:[}; inode=${inode%]};` +
		` printf '%s\t%s\n' "$inode" "$comm";;` +
		` esac; done; done`

	out, err := o.execRead(ctx, containerName, []string{"sh", "-c", script})
	if err != nil {
		o.log.Debug("proxyportmap ops: inode→comm script failed in %s: %v", containerName, err)
		return nil
	}

	// Build inode → process name.
	inodeToProc := make(map[uint64]string)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "\t", 2)
		if len(parts) != 2 {
			continue
		}
		inode, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			continue
		}
		inodeToProc[inode] = strings.TrimSpace(parts[1])
	}

	// Cross-reference with port→inode.
	result := make(map[uint16]string)
	for port, inode := range portToInode {
		if proc, ok := inodeToProc[inode]; ok {
			result[port] = proc
		}
	}
	return result
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

// parseSS extracts listening TCP ports from `ss -tlnHp` output.
//
// Two formats exist depending on iproute2 version:
//
//	Old (no Netid column): LISTEN 0 128 0.0.0.0:8080 0.0.0.0:*
//	New (Netid column):    tcp LISTEN 0 128 0.0.0.0:8080 0.0.0.0:*
//
// Old format: fields[0]=="LISTEN", local at fields[3].
// New format: fields[1]=="LISTEN", local at fields[4].
//
// When the -p flag is present, ss appends a users column:
//
//	users:(("procname",pid=123,fd=4))
//
// The process name is extracted from the first double-quoted string after
// "users:((".
func parseSS(data []byte) []PortInfo {
	seen := make(map[uint16]bool)
	var ports []PortInfo
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		var localIdx int
		switch {
		case len(fields) >= 5 && fields[0] == "LISTEN":
			localIdx = 3 // old format: no Netid column
		case len(fields) >= 6 && fields[1] == "LISTEN":
			localIdx = 4 // new format: Netid column present
		default:
			continue
		}
		p := portFromAddr(fields[localIdx])
		if p == 0 || seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, PortInfo{Port: p, Process: extractSSProcess(line)})
	}
	return ports
}

// extractSSProcess extracts the process name from an ss -p line such as:
//
//	... users:(("python3",pid=123,fd=4))
//
// Returns an empty string if the column is absent or malformed.
func extractSSProcess(line string) string {
	const marker = `users:((`
	idx := strings.Index(line, marker)
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(marker):]
	// Expect the next character to be a double-quote opening the process name.
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:] // skip opening quote
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// parseProcNetTCP extracts LISTEN entries from /proc/net/tcp.
// Format: sl  local_address rem_address  st ...
// local_address is "HEXIP:HEXPORT"; state 0A = LISTEN.
func parseProcNetTCP(data []byte) []PortInfo {
	seen := make(map[uint16]bool)
	var ports []PortInfo
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
			ports = append(ports, PortInfo{Port: p})
			seen[p] = true
		}
	}
	return ports
}

// parseProcNetTCPInodes extracts the socket inode for each LISTEN entry in
// /proc/net/tcp (or /proc/net/tcp6).  The returned map is used to correlate
// ports with process names via /proc/[pid]/fd symlinks.
func parseProcNetTCPInodes(data []byte) map[uint16]uint64 {
	result := make(map[uint16]uint64)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	header := true
	for scanner.Scan() {
		if header {
			header = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		// Need at least 10 columns: sl local rem st … uid timeout inode
		if len(fields) < 10 {
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
		port := uint16(b[0])<<8 | uint16(b[1])
		if port == 0 {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		result[port] = inode
	}
	return result
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
