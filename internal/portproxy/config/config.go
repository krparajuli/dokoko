// Package portproxyconfig generates nginx configuration for the dokoko-proxy
// container.  It has no Docker dependencies and contains only pure string
// generation logic.
package portproxyconfig

import (
	"fmt"
	"strings"

	portproxystate "dokoko.ai/dokoko/internal/portproxy/state"
)

// mapBlock is the top-level WebSocket upgrade map block that must always
// appear in the nginx config.
const mapBlock = `map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
`

// Generate produces the complete nginx.conf content for all active mappings.
// UDP ports are skipped.  Returns a minimal valid config (map block only)
// when mappings is empty.
func Generate(mappings []*portproxystate.PortMapping) string {
	var sb strings.Builder
	sb.WriteString(mapBlock)

	for _, m := range mappings {
		if m.ContainerPort.Proto != "tcp" {
			continue
		}
		sb.WriteString(serverBlock(m))
	}

	return sb.String()
}

// serverBlock renders one nginx server{} block for a single TCP port mapping.
// HTTP only — TLS is not needed within Docker networking, and combining
// `listen N;` and `listen N ssl;` on the same port is invalid nginx config.
func serverBlock(m *portproxystate.PortMapping) string {
	return fmt.Sprintf(`
server {
    listen %d;

    location / {
        proxy_pass         http://%s:%d;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade $http_upgrade;
        proxy_set_header   Connection $connection_upgrade;
        proxy_set_header   Host $host;
        proxy_set_header   X-Real-IP $remote_addr;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
`,
		m.HostPort,
		m.ContainerName,
		m.ContainerPort.Port,
	)
}
