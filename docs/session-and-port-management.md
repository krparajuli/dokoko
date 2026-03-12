# Session & Port Management

## Container Sessions

Each authenticated user gets one Docker container. The container name is derived
deterministically from the user ID:

```
wc-<sanitized_user_id>     e.g.  wc-admin  /  wc-alice_b
```

Non-alphanumeric characters in the user ID are replaced with `_`.

### Session lifecycle

```mermaid
stateDiagram-v2
    [*] --> provisioning : Provision(userID, catalogID)
    provisioning --> ready     : container running + ttyd up
    provisioning --> error     : Docker op failed
    ready        --> terminating : Terminate(userID)
    terminating  --> stopped   : container removed
    error        --> [*]
    stopped      --> [*]
```

### Provision decision tree

```mermaid
flowchart TD
    A[Provision request] --> B{Container\nwc-userID exists?}
    B -- No --> C[Create & start container]
    B -- Yes --> D{Running &\ncorrect base-path label?}
    D -- Yes --> E[Reuse — return existing HostPort]
    D -- No  --> F[Force-remove stale container]
    F --> C
    C --> G[Bind ttyd to 127.0.0.1:random]
    G --> H[Store UserSession in memory]
    H --> I[Status → ready]
```

`HostPort` for ttyd is bound to `127.0.0.1` only — never exposed directly to the
network.  All terminal traffic flows through the dokoko web server.

### Key types

| Type | Package | Description |
|---|---|---|
| `UserSession` | `webcontainers/state` | In-memory record: userID → containerName/ID/hostPort/status |
| `SessionStore` | `webcontainers/state` | Concurrent map keyed by userID |
| `Clerk` | `webcontainers/clerk` | Public API: Provision / Terminate / GetSession |
| `Actor` | `webcontainers/actor` | Async worker queue for container ops |

---

## Port Proxying

User containers can listen on arbitrary TCP ports. dokoko proxies those ports
through a single shared nginx container (`dokoko-proxy`) so the browser only
needs one origin.

### Overall flow

```mermaid
flowchart LR
    Browser -->|"/api/webcontainers/port/{user}/{port}/**"| Go["Go web server\n(proxyUserPort)"]
    Go -->|"HTTP 127.0.0.1:{hostPort}"| Nginx["dokoko-proxy\nnginx:alpine\nports 8100-8199"]
    Nginx -->|"http://{containerName}:{containerPort}"| UC["User container\nwc-{user}"]

    subgraph "Docker bridge network  proxy_wc-{user}"
        Nginx
        UC
    end
```

### Port scan & registration

```mermaid
sequenceDiagram
    participant FE  as Browser / TerminalTab
    participant API as Go server
    participant PPA as proxyportmap actor
    participant Ops as proxyportmap ops
    participant PXA as portproxy actor
    participant NGX as dokoko-proxy (nginx)
    participant ST  as portproxy store

    FE  ->> API : POST /api/proxyportmap/scan {user_id}
    API ->> PPA : ScanAndMap(userID, containerName, containerID)
    PPA ->> Ops : ScanListeningPorts(containerName)
    Note over Ops: docker exec — reads /proc/net/tcp[6] + ss

    Ops -->> PPA : []PortInfo{port, process}
    Note over PPA: Pre-populate HostPort from<br/>portproxy store (carry forward<br/>existing mappings — no broken URLs)
    PPA ->> PPA : SetResult(rawMapped)   ← immediate, HostPort preserved

    PPA ->> PXA : RegisterContainer(containerName, ports)
    PXA ->> ST  : AllocatePort (idempotent — reuses existing)
    PXA ->> NGX : docker network create proxy_wc-{user}
    PXA ->> NGX : NetworkConnect(proxy + userContainer)
    PXA ->> NGX : exec: write proxy.conf.tmp → mv → nginx -s reload
    Note over PXA: reloadMu serialises reloads<br/>across all users

    PXA -->> PPA : Ticket settled
    PPA ->> PPA : SetResult(finalMapped)  ← with real HostPorts
    API -->> FE  : {ports:[{container_port, host_port, url, process}]}
```

### Host-port allocation

```mermaid
flowchart LR
    subgraph "portproxy store  (global)"
        direction TB
        A["wc-alice : 8000/tcp  →  8100"]
        B["wc-bob   : 3000/tcp  →  8101"]
        C["wc-alice : 5432/tcp  →  8102"]
    end

    subgraph "nginx  proxy.conf"
        direction TB
        S1["server { listen 8100;\n  proxy_pass wc-alice:8000; }"]
        S2["server { listen 8101;\n  proxy_pass wc-bob:3000; }"]
        S3["server { listen 8102;\n  proxy_pass wc-alice:5432; }"]
    end

    A --> S1
    B --> S2
    C --> S3
```

- Host ports 8100–8199 (100 total) are allocated from a global pool.
- `AllocatePort` is idempotent: same container+port always gets the same host port.
- `ReleaseMappingsFor(containerName)` frees all ports when a container is deregistered.

### nginx config generation

`portproxyconfig.Generate(allActiveMappings)` produces one `server {}` block per
active TCP mapping:

```nginx
map $http_upgrade $connection_upgrade { default upgrade; '' close; }

server {
    listen 8100;
    location / {
        proxy_pass         http://wc-alice:8000;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade    $http_upgrade;
        proxy_set_header   Connection $connection_upgrade;
        proxy_read_timeout 3600s;
    }
}
```

The config is written atomically (`proxy.conf.tmp` → `mv` → `proxy.conf`) and
reloads are serialised by `reloadMu` in the portproxy actor to prevent concurrent
writes from multiple users corrupting the file.

### URL routing

```
Browser GET /api/webcontainers/port/alice/8000/index.html
                    │
          proxyUserPort handler
                    │
          ppm.GetResult("alice")       ← look up HostPort
                    │
          ReverseProxy → http://127.0.0.1:8100
                    │
          nginx (listen 8100)          ← strips dokoko prefix
                    │
          proxy_pass → wc-alice:8000/index.html
```

### Isolation guarantees

| Layer | Mechanism |
|---|---|
| Container | Each user owns exactly one `wc-<userID>` container |
| Network | Dedicated bridge `proxy_wc-<userID>` per container; proxy is the only peer |
| Port allocation | Global store keyed by `containerName:port/proto` — no collisions |
| nginx routing | One `server {}` block per host port — no cross-user traffic |
| HTTP path | `{user_id}` in every API path; server looks up only that user's result |
| ttyd binding | `127.0.0.1` only — not reachable without going through the Go server |
