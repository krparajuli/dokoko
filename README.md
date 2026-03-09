# dokoko

Dokoko - so you can use vibe-code and test your application on the web - on the go.

dokoko is the docker containers based web runner for terminal and coding agents like Claude Code CLI, Gemini CLI and Codex CLI. But, it is currently extremely insecure.

*Do not expose this to the public internet. If you must use it, use it with VPN solutions or with firewall IP address whitelisting.*

## Questions
1. Is this totally secure? **No.** 
2. Are all necessary security components added? **No.** 
3. Can this be hosted on a public WAN interface or exposed to 0.0.0.0 without a firewall or the safety of NAT? **No.**
4. Can this be run with multiple user environment? **Yes, it has multi user support**
5. Is there robust separation of user activities and containers? **NO. Only use it for yourself and trusted users environment.**

## What is it good for
1. Can multiple containers and multiple CLIs be run simultaneously? **YES**
2. Can I run these CLI tools on YOLO mode? **YES, but still exercise caution.**
3. Can I cowork with other collegues on the same project? **YES**
4. Will my CLI sessions persist across multiple logins and days? **YES**

*dokoko is intended as an internal tooling layer. Run it behind a reverse proxy with authentication, on a private network,
or on localhost. The built-in auth middleware is a starting point, not a hardened production gate.*

## Full details
dokoko is a Docker management tool that ships both a terminal UI (TUI) and a web UI backed by the same server. It lets you manage containers, images, volumes, networks, and exec sessions from either interface. Under the hood, every mutating operation — create, start, stop, remove — is dispatched asynchronously through an actor/state-machine architecture. Each operation returns a ticket immediately; the caller awaits settlement, and the state machine tracks requested, active, failed, and abandoned changes independently for each subsystem. The web server also supports web-container provisioning, port-proxy registration, and environment variable management for sandboxed user sessions.

The codebase is split into `cmd/cli` for the Bubble Tea TUI, `cmd/web` for the HTTP server and React frontend, and `internal` for the Docker actor/state packages, port proxy, and web-container subsystems. The web UI is a Vite + React app with light/dark mode; the TUI renders a four-pane layout with live state summaries. Both surfaces share the same underlying manager and the same async operation semantics — a ticket is returned, waited on, and the result surfaced to the user.

Is this totally secure? No. Are all security components added? No. Can this be hosted on a public WAN interface without a firewall or the safety of NAT? No. dokoko is intended as an internal tooling layer — run it behind a reverse proxy with authentication, on a private network, or on localhost. The built-in auth middleware is a starting point, not a hardened production gate.

* Cowritten with Claude Code