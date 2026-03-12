# Authentication & Access Control

## Overview

dokoko uses cookie-based sessions backed by SQLite. Every `/api/*` request
(except login, register, and health) must carry a valid `dokoko_session` cookie.
User identity from that session determines which container a user can provision
and which ports they can access.

---

## Login & session lifecycle

```mermaid
sequenceDiagram
    participant B  as Browser
    participant MW as Auth middleware
    participant H  as Handler
    participant DB as SQLite

    B  ->> MW : POST /api/auth/login {username, password}
    MW ->> MW : bypass auth check (public route)
    MW ->> H  : forward
    H  ->> DB : SELECT user WHERE username=? AND password=?
    DB -->> H  : user row (or not found)
    alt credentials valid
        H  ->> DB : INSERT INTO sessions (token, username, role, expires_at=now+24h)
        H  -->> B  : 200 {username, role} + Set-Cookie: dokoko_session=<64-hex>, HttpOnly, SameSite=Lax"
    else invalid
        H  -->> B  : 401 "invalid username or password"
    end

    Note over B,DB: Every subsequent request automatically sends the cookie

    B  ->> MW : GET /api/auth/me
    MW ->> DB : SELECT * FROM sessions WHERE token=? AND expires_at > now()
    alt session valid
        DB -->> MW : session row {username, role}
        MW ->> H  : r.Context() ← *Session{username, role}
        H  -->> B  : 200 {username, role}
    else missing / expired
        MW -->> B  : 401 "unauthorized"
    end
```

Session tokens are 32 bytes from `crypto/rand` encoded as 64-char hex.
Sessions are stored in SQLite and expire after **24 hours**.

---

## Middleware chain

```mermaid
flowchart TD
    R[Incoming request] --> C[CORS middleware]
    C --> A{Auth middleware}

    A -- "OPTIONS\n/api/auth/login\n/api/auth/register\n/api/health\nnon-/api/ paths" --> P[Pass through — no auth required]
    A -- "all other /api/* routes" --> K{Cookie present\n& not expired?}

    K -- No  --> U[401 Unauthorized]
    K -- Yes --> I["Inject *Session into r.Context()"]
    I --> M[Route mux]

    M --> RA{"requireAdmin()\ncheck?"}
    RA -- admin route + user is not admin --> F[403 Forbidden]
    RA -- passes --> H[Handler]
```

Handlers retrieve identity with:
```go
sess, _ := sessionFromContext(r.Context())
// sess.Username, sess.Role
```

---

## Role model

| Role | Value | Capabilities |
|---|---|---|
| Admin | `"admin"` | All routes; all catalog images; manage users |
| User  | `"user"`  | Own container only; restricted image catalog; cannot manage other users |

### Admin-only routes

```
GET    /api/users
POST   /api/users
DELETE /api/users/{username}
PUT    /api/users/{username}/password
```

### Image catalog restriction

```mermaid
flowchart LR
    P[POST /api/webcontainers/provision] --> R{Role?}
    R -- admin --> ALL[All catalog images available]
    R -- user  --> W{--allowed-images\nflag set?}
    W -- empty --> ALL
    W -- set   --> F[Filter to whitelisted image IDs only]
    F --> CHK{Requested image\nin whitelist?}
    CHK -- No  --> E[403 Forbidden]
    CHK -- Yes --> OK[Provision container]
```

---

## User identity → container → port access

Every resource is scoped to a **username** (the authenticated user's username
from the session cookie). The container name is derived from it:

```
username  →  SafeContainerName()  →  "wc-" + sanitized_username
```

```mermaid
flowchart TD
    subgraph Auth layer
        Cookie["dokoko_session cookie"] --> MW2["Middleware\nextract Session{username, role}"]
    end

    subgraph Container access
        MW2 --> WP["POST /api/webcontainers/provision\nbody: {user_id}"]
        WP --> CN["Container  wc-{user_id}"]
        MW2 --> WT["GET /api/webcontainers/terminal/{user_id}/**"]
        WT --> TP["Reverse proxy → ttyd on 127.0.0.1:{hostPort}"]
    end

    subgraph Port access
        MW2 --> PP["GET /api/webcontainers/port/{user_id}/{port}/**"]
        PP --> PM["ppm.GetResult(user_id)"]
        PM --> NX["Reverse proxy → 127.0.0.1:{hostPort} (nginx)"]
        NX --> UC["wc-{user_id}:{port} via bridge network"]
    end
```

The `user_id` in all three subtree paths is the authenticated user's own
username — the frontend only ever passes `user!.username` (from the session).

---

## Database schema

```sql
CREATE TABLE users (
    username   TEXT PRIMARY KEY,
    password   TEXT NOT NULL,       -- plain text (local dev tool)
    role       TEXT NOT NULL DEFAULT 'user',
    created_at TEXT NOT NULL
);

CREATE TABLE sessions (
    token      TEXT PRIMARY KEY,    -- 64-char hex, crypto/rand
    username   TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
    role       TEXT NOT NULL,       -- snapshot at login time
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL        -- login time + 24 h
);
CREATE INDEX idx_sessions_username   ON sessions(username);
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);
```

Sessions cascade-delete when a user is deleted.
Role changes take effect at the **next login** (the session carries a snapshot).

---

## Frontend auth flow

```mermaid
flowchart TD
    Start([App mount]) --> ME["GET /api/auth/me"]
    ME --> R2{200?}
    R2 -- Yes --> SU["setUser({username, role})"]
    R2 -- No  --> SN["setUser(null) → show LoginPage"]

    SN --> LF["LoginPage\nPOST /api/auth/login"]
    LF --> L2{200?}
    L2 -- Yes --> SU
    L2 -- No  --> ER["show error message"]

    SU --> APP["TerminalTab — userID = user.username\nAll API calls send cookie automatically"]
```

The browser sends `dokoko_session` automatically on every same-origin request
(`credentials: 'include'` is set in the API client).  No token header is needed.

---

## Logout

```mermaid
sequenceDiagram
    participant B  as Browser
    participant H  as Handler
    participant DB as SQLite

    B  ->> H  : POST /api/auth/logout
    H  ->> DB : DELETE FROM sessions WHERE token=?
    H  -->> B  : 200 + Set-Cookie: dokoko_session=,MaxAge=-1
    Note over B: Cookie cleared — next /api/* returns 401
```
