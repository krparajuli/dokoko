# Port Proxy Access Authorization

## Overview

All port proxy routes sit behind the same cookie-based `authMiddleware` that
guards every `/api/*` endpoint.  A valid `dokoko_session` cookie is required
for every request; missing or expired sessions receive **401 Unauthorized**
before any handler runs.

---

## Middleware chain for port proxy routes

```mermaid
flowchart TD
    R[Incoming request] --> C[CORS middleware]
    C --> A{authMiddleware}

    A -- "OPTIONS / /api/auth/* / /api/health / non-/api/ paths" --> P[Pass through]
    A -- "all other /api/* routes" --> K{dokoko_session\ncookie valid?}

    K -- No  --> U[401 Unauthorized]
    K -- Yes --> I["Inject *Session{username, role}\ninto r.Context()"]

    I --> M[Route mux]
    M --> PP["POST /api/proxyportmap/scan"]
    M --> GM["GET /api/proxyportmap/mappings/{user_id}"]
    M --> DM["DELETE /api/proxyportmap/mappings/{user_id}"]
    M --> PX["/api/webcontainers/port/{user_id}/{container_port}/"]
```

---

## Scan & map flow

```mermaid
sequenceDiagram
    participant B   as Browser
    participant MW  as authMiddleware
    participant H   as scanPorts handler
    participant WC  as webcontainers clerk
    participant PPA as proxyportmap actor
    participant NGX as dokoko-proxy (nginx)

    B  ->> MW  : POST /api/proxyportmap/scan {user_id} + cookie
    MW ->> MW  : validate dokoko_session → Session{username, role}
    MW ->> H   : r.Context() ← *Session

    H  ->> H   : decode body → user_id
    H  ->> WC  : GetSession(user_id)
    WC -->> H  : UserSession{containerName, containerID} (or nil → 404)

    H  ->> PPA : ScanAndMap(ctx, user_id, containerName, containerID)
    PPA ->> PPA : docker exec → scan /proc/net/tcp[6]
    PPA ->> NGX : register ports → write proxy.conf → nginx -s reload
    PPA -->> H  : Ticket settled

    H  -->> B  : 200 {user_id, ports:[{container_port, host_port, url}]}
```

> **Note**: the handler does not verify that `user_id` in the body matches the
> session username.  Any authenticated user can request a scan for another
> user's container by supplying a different `user_id`.

---

## Port proxy request flow

```mermaid
sequenceDiagram
    participant B   as Browser
    participant MW  as authMiddleware
    participant H   as proxyUserPort handler
    participant PPM as proxyportmap store
    participant NGX as dokoko-proxy (nginx)
    participant UC  as wc-{user_id} container

    B  ->> MW  : GET /api/webcontainers/port/{user_id}/{container_port}/** + cookie
    MW ->> MW  : validate dokoko_session → 401 if missing/expired
    MW ->> H   : r.Context() ← *Session (not inspected by handler)

    H  ->> PPM : GetResult(user_id)
    PPM -->> H : ScanResult{ports:[{containerPort, hostPort}]} (or nil → 404)

    H  ->> H   : find hostPort for containerPort (or → 404 "port N not mapped")
    H  ->> H   : strip /api/webcontainers/port/{user_id}/{container_port} prefix
    H  ->> NGX : reverse-proxy → http://127.0.0.1:{hostPort}{stripped_path}

    NGX ->> UC : proxy_pass → wc-{user_id}:{container_port}
    UC -->> B  : upstream response
```

---

## Authorization decision matrix

| Route | Auth required | Ownership enforced | Notes |
|---|---|---|---|
| `POST /api/proxyportmap/scan` | Yes (session cookie) | No | Any authenticated user can scan any `user_id` |
| `GET /api/proxyportmap/mappings/{user_id}` | Yes | No | Returns any user's cached mappings |
| `DELETE /api/proxyportmap/mappings/{user_id}` | Yes | No | Any authenticated user can unmap any user's ports |
| `GET /api/webcontainers/port/{user_id}/{port}/` | Yes | No | Proxies to the container named `wc-{user_id}` |

All routes require a valid session.  Ownership (session username == path
`user_id`) is **not** verified server-side — the frontend only ever passes
`user!.username` so cross-user access does not arise in normal use.  This
matches the design of the terminal proxy and webcontainer session routes.

---

## Port unavailability states

A port URL can be valid in the UI but temporarily or permanently unavailable.
There are three distinct causes:

```mermaid
flowchart TD
    B["Browser opens\n/api/webcontainers/port/{user}/{port}/"] --> H[proxyUserPort handler]

    H --> R1{Scan result\nexists for user?}
    R1 -- No  --> E1["404 — no port mapping for user\n(scan never run or result expired)"]

    R1 -- Yes --> R2{Port found\nin result?}
    R2 -- No  --> E2["404 — port N not mapped for user\n(port stopped listening between scan cycles)"]

    R2 -- Yes --> R3{HostPort\n!= 0?}
    R3 -- No  --> E3["404 — port N not mapped for user\n(registration still in flight — transient)"]

    R3 -- Yes --> FWD["Forward → nginx → container"]
    FWD --> R4{Container\nlistening?}
    R4 -- No  --> E4["502 — upstream unavailable\n(app exited after scan)"]
    R4 -- Yes --> OK["200 — upstream response"]
```

### The transient HostPort=0 window

The most common "port is not available" state occurs during the brief window
between the two store writes inside `ScanAndMap`:

1. **Step 2** — ports discovered; result written immediately with existing
   `HostPort` carried forward from the portproxy store.  For a brand-new port
   this is `HostPort=0`.
2. **Steps 3–4** — proxy container ensured; port registered with nginx; nginx
   reloaded.
3. **Step 5** — result overwritten with real `HostPort` values from the store.

If the browser opens the port URL between steps 2 and 5 (and the port has
never been registered before), `proxyUserPort` finds `HostPort=0` and returns:

```
HTTP 404  port 8000 not mapped for user
```

The next scan cycle (≤ 15 s) resolves this automatically — after step 5 the
`HostPort` is stable and subsequent requests succeed.

### Summary of error responses from proxyUserPort

| Condition | HTTP status | Body |
|---|---|---|
| No session cookie / expired | 401 | `{"error":"unauthorized"}` |
| No scan result for `user_id` | 404 | `no port mapping for user — run scan first` |
| Port not in scan result | 404 | `port N not mapped for user` |
| Port in result but `HostPort=0` (transient) | 404 | `port N not mapped for user` |
| nginx cannot reach container port | 502 | `upstream unavailable` |

---

## Path stripping before upstream

The dokoko web server strips the dokoko path prefix before forwarding, so the
upstream application sees its own URL space:

```
Browser:  GET /api/webcontainers/port/alice/8000/dashboard/index.html
                          │
               strip prefix: /api/webcontainers/port/alice/8000
                          │
nginx (127.0.0.1:8100):  GET /dashboard/index.html
                          │
              proxy_pass → wc-alice:8000/dashboard/index.html
```

WebSocket upgrades are preserved via the `Upgrade` / `Connection` headers set
in the nginx `server {}` block generated by `portproxyconfig.Generate`.

---

## Network isolation

Even though authorization is not checked per-owner, the Docker network
topology provides hard isolation at the transport layer:

```mermaid
flowchart LR
    NGX["dokoko-proxy\n(nginx)"]

    subgraph "proxy_wc-alice  (bridge)"
        NGX -->|"wc-alice:8000"| A["wc-alice container"]
    end

    subgraph "proxy_wc-bob  (bridge)"
        NGX -->|"wc-bob:3000"| B["wc-bob container"]
    end

    NGX -.->|"cannot reach"| B2["wc-alice (from bob's network)"]
```

Each user container is connected only to its own `proxy_wc-{user}` bridge
network.  nginx routes by host port (`listen 8100`, `listen 8101`, …) and
`proxy_pass` uses the container name, so alice's container is unreachable from
bob's network even if a handler bug forwarded to the wrong host port.
