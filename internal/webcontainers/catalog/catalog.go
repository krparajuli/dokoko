// Package webcontainerscatalog defines the set of approved images users may
// provision for interactive web-terminal sessions.
//
// Each ImageDef includes an in-container startup script that installs ttyd
// and tmux if not already present, then launches ttyd on port 7681 with a
// persistent tmux session.  Users connect to that port via the browser.
package webcontainerscatalog

// ImageDef describes one approved image available for web-terminal sessions.
type ImageDef struct {
	// ID is the catalog key used in API requests (e.g. "ubuntu").
	ID string

	// Image is the Docker image reference pulled from the registry.
	Image string

	// DisplayName is shown in the UI image selector.
	DisplayName string

	// Description is a short human-readable blurb for the UI.
	Description string

	// StartScript is passed as `sh -c "<StartScript>"` when creating the
	// container.  It must install ttyd + tmux (if absent), then exec ttyd
	// on port 7681 backed by a persistent tmux session.
	StartScript string
}

// debianScript installs ttyd + tmux via apt-get, then starts ttyd.
const debianScript = `
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq tmux curl
ARCH=$(uname -m)
if [ ! -x /usr/local/bin/ttyd ]; then
  curl -fsSL -o /usr/local/bin/ttyd \
    "https://github.com/tsl0922/ttyd/releases/latest/download/ttyd.${ARCH}"
  chmod +x /usr/local/bin/ttyd
fi
exec ttyd -W -p 7681 --base-path "${TTYD_BASE_PATH:-/}" tmux new -A -s main
`

// alpineScript installs ttyd + tmux via apk, then starts ttyd.
const alpineScript = `
set -e
apk add --no-cache tmux curl
ARCH=$(uname -m)
if [ ! -x /usr/local/bin/ttyd ]; then
  curl -fsSL -o /usr/local/bin/ttyd \
    "https://github.com/tsl0922/ttyd/releases/latest/download/ttyd.${ARCH}"
  chmod +x /usr/local/bin/ttyd
fi
exec ttyd -W -p 7681 --base-path "${TTYD_BASE_PATH:-/}" tmux new -A -s main
`

// Catalog is the ordered list of images users can choose from.
var Catalog = []*ImageDef{
	{
		ID:          "ubuntu",
		Image:       "ubuntu:22.04",
		DisplayName: "Ubuntu 22.04",
		Description: "General-purpose Debian-based Linux shell with apt.",
		StartScript: debianScript,
	},
	{
		ID:          "debian",
		Image:       "debian:12-slim",
		DisplayName: "Debian 12 Slim",
		Description: "Lightweight Debian shell — smaller footprint than Ubuntu.",
		StartScript: debianScript,
	},
	{
		ID:          "alpine",
		Image:       "alpine:3.19",
		DisplayName: "Alpine 3.19",
		Description: "Minimal musl-based Linux; fast to pull, low resource use.",
		StartScript: alpineScript,
	},
}

// Find returns the ImageDef with the given catalog ID, or nil if not found.
func Find(id string) *ImageDef {
	for _, d := range Catalog {
		if d.ID == id {
			return d
		}
	}
	return nil
}
