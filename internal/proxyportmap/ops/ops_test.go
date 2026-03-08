package proxyportmapops

import (
	"strings"
	"testing"
)

// ── parseProcNetTCP ───────────────────────────────────────────────────────────

func TestParseProcNetTCP_empty(t *testing.T) {
	if got := parseProcNetTCP(nil); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
	if got := parseProcNetTCP([]byte{}); len(got) != 0 {
		t.Errorf("expected empty for empty input, got %v", got)
	}
}

func TestParseProcNetTCP_headerOnly(t *testing.T) {
	data := []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n")
	if got := parseProcNetTCP(data); len(got) != 0 {
		t.Errorf("expected empty for header-only input, got %v", got)
	}
}

func TestParseProcNetTCP_port8080(t *testing.T) {
	// Realistic /proc/net/tcp snippet with 0.0.0.0:8080 in LISTEN state.
	// 8080 = 0x1F90 in hex.
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
`)
	got := parseProcNetTCP(data)
	if len(got) != 1 || got[0].Port != 8080 {
		t.Errorf("expected [8080], got %v", got)
	}
}

func TestParseProcNetTCP_port8000(t *testing.T) {
	// 8000 = 0x1F40
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F40 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 99999 1 0000000000000000 100 0 0 10 0
`)
	got := parseProcNetTCP(data)
	if len(got) != 1 || got[0].Port != 8000 {
		t.Errorf("expected [8000], got %v", got)
	}
}

func TestParseProcNetTCP_skipsNonListen(t *testing.T) {
	// Only state 0A (LISTEN) should be returned; ESTABLISHED (01) must be skipped.
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 11111 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F40 A4B30A0A:0050 01 00000000:00000000 00:00000000 00000000  1000        0 22222 1 0000000000000000 20 4 24 10 -1
`)
	got := parseProcNetTCP(data)
	if len(got) != 1 || got[0].Port != 8080 {
		t.Errorf("expected only [8080] (LISTEN), got %v", got)
	}
}

func TestParseProcNetTCP_multiplePorts(t *testing.T) {
	// 80=0x0050, 443=0x01BB, 8080=0x1F90
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   1: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
   2: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3 1 0000000000000000 100 0 0 10 0
`)
	got := parseProcNetTCP(data)
	want := map[uint16]bool{80: true, 443: true, 8080: true}
	if len(got) != 3 {
		t.Fatalf("expected 3 ports, got %v", got)
	}
	for _, info := range got {
		if !want[info.Port] {
			t.Errorf("unexpected port %d in result %v", info.Port, got)
		}
	}
}

func TestParseProcNetTCP_deduplicates(t *testing.T) {
	// Same port appearing twice should be deduplicated.
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
`)
	got := parseProcNetTCP(data)
	if len(got) != 1 {
		t.Errorf("expected deduplicated to 1 entry, got %v", got)
	}
}

// /proc/net/tcp6 uses 32-char hex IPv6 addresses but the same state/port format.
func TestParseProcNetTCP_tcp6Format(t *testing.T) {
	// Same parser handles tcp6: local_address is 32-char hex + :HEXPORT.
	// Port 8080 = 0x1F90.
	data := []byte(`  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 99 1 0000000000000000 100 0 0 10 0
`)
	got := parseProcNetTCP(data)
	if len(got) != 1 || got[0].Port != 8080 {
		t.Errorf("expected [8080] from tcp6 format, got %v", got)
	}
}

// parseProcNetTCP always returns empty Process since /proc/net/tcp has no pid info.
func TestParseProcNetTCP_processAlwaysEmpty(t *testing.T) {
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
`)
	got := parseProcNetTCP(data)
	if len(got) != 1 {
		t.Fatalf("expected 1 port, got %v", got)
	}
	if got[0].Process != "" {
		t.Errorf("expected empty Process from /proc/net/tcp, got %q", got[0].Process)
	}
}

// ── parseSS ───────────────────────────────────────────────────────────────────

// oldFormatSS is what older iproute2 produces: no Netid column.
// fields: [State, Recv-Q, Send-Q, Local, Peer]
const oldFormatSS = `LISTEN 0 128 0.0.0.0:8080 0.0.0.0:*
LISTEN 0 128 0.0.0.0:443  0.0.0.0:*
`

// newFormatSS is what Ubuntu 22.04 / iproute2 5.x produces: Netid column present.
// fields: [Netid, State, Recv-Q, Send-Q, Local, Peer]
const newFormatSS = `tcp LISTEN 0 128 0.0.0.0:8080 0.0.0.0:*
tcp LISTEN 0 128 0.0.0.0:443  0.0.0.0:*
`

func TestParseSS_oldFormat(t *testing.T) {
	got := parseSS([]byte(oldFormatSS))
	if len(got) != 2 {
		t.Fatalf("old format: expected 2 ports, got %v", got)
	}
}

func TestParseSS_newFormat_fieldIndex(t *testing.T) {
	// This test documents the current behavior: parseSS uses fields[3] which is
	// Send-Q in new format, so it fails to extract ports from new-format output.
	// After the fix, this test should pass.
	got := parseSS([]byte(newFormatSS))
	want := map[uint16]bool{8080: true, 443: true}
	for _, info := range got {
		if !want[info.Port] {
			t.Errorf("unexpected port %d", info.Port)
		}
	}
	if len(got) != 2 {
		t.Errorf("new format: expected 2 ports but got %v (field index bug — fields[3] is Send-Q not Local)", got)
	}
}

func TestParseSS_processName(t *testing.T) {
	// ss -tlnHp with new format: Netid column + users column
	data := []byte(`tcp LISTEN 0 128 0.0.0.0:8080 0.0.0.0:* users:(("python3",pid=123,fd=4))
`)
	got := parseSS(data)
	if len(got) != 1 {
		t.Fatalf("expected 1 port, got %v", got)
	}
	if got[0].Port != 8080 {
		t.Errorf("expected port 8080, got %d", got[0].Port)
	}
	if got[0].Process != "python3" {
		t.Errorf("expected process %q, got %q", "python3", got[0].Process)
	}
}

func TestParseSS_noProcessName(t *testing.T) {
	// Lines without users:((...)) should have empty Process.
	data := []byte(`tcp LISTEN 0 128 0.0.0.0:3000 0.0.0.0:*
`)
	got := parseSS(data)
	if len(got) != 1 {
		t.Fatalf("expected 1 port, got %v", got)
	}
	if got[0].Port != 3000 {
		t.Errorf("expected port 3000, got %d", got[0].Port)
	}
	if got[0].Process != "" {
		t.Errorf("expected empty Process, got %q", got[0].Process)
	}
}

func TestParseSS_oldFormatWithProcessName(t *testing.T) {
	// Old format (no Netid column) with users column.
	data := []byte(`LISTEN 0 128 0.0.0.0:8080 0.0.0.0:* users:(("nginx",pid=42,fd=6))
`)
	got := parseSS(data)
	if len(got) != 1 {
		t.Fatalf("expected 1 port, got %v", got)
	}
	if got[0].Port != 8080 {
		t.Errorf("expected port 8080, got %d", got[0].Port)
	}
	if got[0].Process != "nginx" {
		t.Errorf("expected process %q, got %q", "nginx", got[0].Process)
	}
}

// ── parseProcNetTCPInodes ─────────────────────────────────────────────────────

func TestParseProcNetTCPInodes_basic(t *testing.T) {
	// 8080 = 0x1F90, inode = 12345 at fields[9].
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
`)
	got := parseProcNetTCPInodes(data)
	if got[8080] != 12345 {
		t.Errorf("expected inode 12345 for port 8080, got %v", got)
	}
}

func TestParseProcNetTCPInodes_skipNonListen(t *testing.T) {
	// State 01 = ESTABLISHED, must be ignored.
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F40 A4B30A0A:0050 01 00000000:00000000 00:00000000 00000000  1000        0 99999 1 0000000000000000 20 4 24 10 -1
`)
	got := parseProcNetTCPInodes(data)
	if len(got) != 0 {
		t.Errorf("expected empty map for non-LISTEN entries, got %v", got)
	}
}

func TestParseProcNetTCPInodes_multiplePorts(t *testing.T) {
	// 80=0x0050 (inode 1111), 8080=0x1F90 (inode 2222).
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1111 1 0000000000000000 100 0 0 10 0
   1: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2222 1 0000000000000000 100 0 0 10 0
`)
	got := parseProcNetTCPInodes(data)
	if got[80] != 1111 || got[8080] != 2222 {
		t.Errorf("unexpected inode map: %v", got)
	}
}

// ── ttyd filtering ────────────────────────────────────────────────────────────

// TestTtydFilterByProcess verifies that a port whose process name contains
// "ttyd" is identified correctly so the caller can exclude it.
func TestTtydFilterByProcess(t *testing.T) {
	data := []byte(`tcp LISTEN 0 128 0.0.0.0:37233 0.0.0.0:* users:(("ttyd",pid=8,fd=5))
tcp LISTEN 0 128 0.0.0.0:8080  0.0.0.0:* users:(("python3",pid=42,fd=3))
`)
	got := parseSS(data)
	if len(got) != 2 {
		t.Fatalf("expected 2 ports, got %v", got)
	}
	// Verify the ttyd entry carries the process name so ScanListeningPorts
	// can filter it (strings.Contains(process, "ttyd")).
	var ttydFound bool
	for _, p := range got {
		if p.Port == 37233 {
			ttydFound = true
			if !strings.Contains(p.Process, "ttyd") {
				t.Errorf("expected process containing 'ttyd' for port 37233, got %q", p.Process)
			}
		}
	}
	if !ttydFound {
		t.Errorf("port 37233 not found in parseSS output: %v", got)
	}
}

// ── portFromAddr ──────────────────────────────────────────────────────────────

func TestPortFromAddr(t *testing.T) {
	cases := []struct {
		addr string
		want uint16
	}{
		{"0.0.0.0:8080", 8080},
		{"127.0.0.1:443", 443},
		{"*:8080", 8080},
		{"[::1]:8080", 8080},
		{"[::]:8080", 8080},
		{"8080", 0},    // no colon
		{"invalid", 0}, // no colon
		{"", 0},
	}
	for _, c := range cases {
		got := portFromAddr(c.addr)
		if got != c.want {
			t.Errorf("portFromAddr(%q) = %d, want %d", c.addr, got, c.want)
		}
	}
}
