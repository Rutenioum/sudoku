# Sudoku External Controller API (RESTful)

English | [中文](./api.zh.md)

Sudoku includes a RESTful API (the "external controller") with the same semantics as Clash and mihomo. Routes, JSON fields, status codes and error messages follow [MetaCubeX/mihomo Alpha](https://github.com/MetaCubeX/mihomo/tree/Alpha) ([official docs](https://wiki.metacubex.one/api/)), so panels such as [zashboard](https://github.com/Zephyruso/zashboard), [metacubexd](https://github.com/MetaCubeX/metacubexd) and [yacd](https://github.com/MetaCubeX/Yacd-meta) work with it directly. Your own scripts can use it too.

- **Client:** read nodes, groups, routing rules, live bandwidth, active connections and logs. Run latency tests, switch nodes, switch between `rule`, `global` and `direct` modes, and close connections.
- **Server:** read-only. User count, active users, each user's live up/down rate and data usage, plus bandwidth, memory and logs.

Contents:

- [1. Enabling](#1-enabling)
- [2. Connecting a panel](#2-connecting-a-panel)
- [3. Conventions](#3-conventions) (auth, CORS, errors, streams)
- [4. Route table](#4-route-table)
- [5. Common endpoints (client and server)](#5-common-endpoints-client-and-server)
- [6. Client endpoints](#6-client-endpoints)
- [7. Server endpoints](#7-server-endpoints)
- [8. mihomo endpoints not implemented](#8-mihomo-endpoints-not-implemented)
- [9. Resource cost](#9-resource-cost)

---

## 1. Enabling

Add these fields to a **client or server** JSON config:

```json
{
  "external_controller": "127.0.0.1:9090",
  "secret": "change-me"
}
```

| Field | Meaning |
|---|---|
| `external_controller` | Listen address. It must be `host:port` with a port from 1 to 65535, e.g. `127.0.0.1:9090`, `[::1]:9090`, `:9090` (every interface) or `0.0.0.0:9090`. Empty or missing turns the API off. |
| `secret` | Bearer token. Empty means no authentication. |

- On start, the log shows `[API] External controller on http://127.0.0.1:9090`.
- On a non-loopback address with an empty `secret`, sudoku logs a warning, because anyone who can reach the port can watch and steer the node. **Always** set a `secret` when the API is exposed, and prefer a firewall or SSH tunnel on top.
- A multi-node client uses `external_controller` and `secret` from its **first** config only.
- A client started from a short link with `-link` has no config file, so the API stays off. Use `-c` with a config file to get the API.

## 2. Connecting a panel

The steps below use zashboard; metacubexd and yacd work the same way.

1. Open the panel, either a hosted copy or one you serve yourself.
2. Add a backend:
   - URL: `http://127.0.0.1:9090`, i.e. the `external_controller` address. If it listens on `:9090` or `0.0.0.0:9090`, use the host's real IP.
   - Secret: the `secret` value.
3. Once connected, the panel shows:
   - Proxies: the `PROXY`, `GLOBAL` and `Balance` groups and every node, with picking and latency tests.
   - Rules: the rule providers and their rule counts, with manual updates.
   - Connections: live connections, which you can close.
   - Overview: bandwidth chart and memory.
   - Logs: live logs.
   - Mode switch in the header: rule, global or direct.

Notes:

- Any origin may call the API (CORS `*`), and Chrome's Private Network Access preflight is answered, so a hosted HTTPS panel can reach an `http://` backend on your LAN. If the browser still blocks mixed content, serve the panel yourself or open it over HTTP.
- On a server the API only offers `/traffic`, `/memory`, `/logs`, `/version` and `/users`. A panel pointed at a server shows empty pages or 404s for proxies, rules and connections; that is expected. Read user statistics from `/users` with a script (see [section 7](#7-server-endpoints)).

## 3. Conventions

### 3.1 Authentication

With a `secret` set, every request must carry:

```
Authorization: Bearer <secret>
```

Browsers cannot set headers on WebSockets, so **WebSocket requests** may pass the token as `?token=<secret>` instead. Plain HTTP requests do not accept `?token=`.

A failed check returns:

```
HTTP/1.1 401 Unauthorized
{"message":"Unauthorized"}
```

The token is compared in constant time. Without a `secret`, there is no check at all.

### 3.2 CORS

- A request with an `Origin` header gets `Access-Control-Allow-Origin: *`.
- An `OPTIONS` preflight returns `204` with these headers:
  - `Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE`
  - `Access-Control-Allow-Headers: Content-Type, Authorization`
  - `Access-Control-Max-Age: 300`
  - `Access-Control-Allow-Private-Network: true`, when the request sends `Access-Control-Request-Private-Network: true`.
- Preflights need no token.

### 3.3 Responses and errors

- Success returns JSON (`Content-Type: application/json; charset=utf-8`). Actions with no result return `204 No Content`.
- Errors are always `{"message":"..."}`:

| Status | message | When |
|---|---|---|
| 400 | `Body invalid` | Body or query cannot be parsed, or a value is invalid |
| 400 | `Must be a Selector` | Picking or unfixing on something that is not a Selector |
| 400 | `Selector update error: ...` | The member is not in the group |
| 401 | `Unauthorized` | Token wrong or missing |
| 404 | `Resource not found` | No proxy, group or rule provider by that name |
| 503 | `An error occurred in the delay test` | Latency test failed (dial failed, unexpected status, etc.) |
| 504 | `Timeout` / `get delay: all proxies timeout` | Latency test timed out |

- Unknown paths get Go's plain-text `404 page not found`, and a wrong method gets `405`.

### 3.4 Streams

`/traffic`, `/memory` and `/logs` are streams. `/connections` and `/users` can also stream. As in mihomo, a stream can be delivered in two ways:

1. **WebSocket** (what panels use): sent when the request carries `Upgrade: websocket`.
   - Each message is one JSON text frame.
   - The client never needs to send anything; pings are answered.
   ```bash
   websocat 'ws://127.0.0.1:9090/traffic?token=change-me'
   ```
2. **Chunked HTTP (NDJSON)**: a plain GET returns `200` and keeps the response open, writing one JSON object per line.
   - Use `curl -N` to turn off buffering.
   ```bash
   curl -N -H 'Authorization: Bearer change-me' http://127.0.0.1:9090/traffic
   ```

Notes:

- `/connections` and `/users` stream only over a WebSocket. A plain GET returns one snapshot.
- The first value is sent immediately, then one per interval.
- A write that stalls for more than 10 seconds ends the stream.
- All streams end when the process exits.

---

## 4. Route table

| Method | Path | Client | Server | Purpose |
|---|---|:-:|:-:|---|
| GET | `/` | ✓ | ✓ | Liveness |
| GET | `/version` | ✓ | ✓ | Version |
| GET | `/traffic` | ✓ | ✓ | Live bandwidth (stream) |
| GET | `/memory` | ✓ | ✓ | Memory (stream) |
| GET | `/logs` | ✓ | ✓ | Live logs (stream) |
| GET | `/configs` | ✓ | | Config and mode |
| PATCH | `/configs` | ✓ | | Switch mode |
| GET | `/proxies` | ✓ | | Every proxy and group |
| GET | `/proxies/{name}` | ✓ | | One proxy |
| PUT | `/proxies/{name}` | ✓ | | Change a Selector's pick |
| DELETE | `/proxies/{name}` | ✓ | | Unfix (always 400 for Selectors) |
| GET | `/proxies/{name}/delay` | ✓ | | Test one proxy |
| GET | `/group` | ✓ | | Every group |
| GET | `/group/{name}` | ✓ | | One group |
| GET | `/group/{name}/delay` | ✓ | | Test all members of a group at once |
| GET | `/providers/proxies` | ✓ | | Proxy providers (always empty) |
| GET | `/rules` | ✓ | | Routing rules |
| GET | `/providers/rules` | ✓ | | Rule providers |
| PUT | `/providers/rules/{name}` | ✓ | | Re-download a rule provider |
| GET | `/connections` | ✓ | | Active connections (streams over WebSocket) |
| DELETE | `/connections` | ✓ | | Close every connection |
| DELETE | `/connections/{id}` | ✓ | | Close one connection |
| GET | `/users` | | ✓ | User statistics (streams over WebSocket) |

The examples below assume:

```bash
API=http://127.0.0.1:9090
AUTH='Authorization: Bearer change-me'
```

---

## 5. Common endpoints (client and server)

### `GET /`

```bash
curl -H "$AUTH" $API/
```
```json
{"hello":"mihomo"}
```

The value is always `"mihomo"`, because some panels identify the backend by it.

### `GET /version`

```bash
curl -H "$AUTH" $API/version
```
```json
{"meta":true,"version":"sudoku v0.5.2"}
```

- `version` is the module version the binary was built from. A local `go build` reports `sudoku dev`.
- `meta: true` tells panels the backend is mihomo (Meta) compatible, so they enable the matching features.

### `GET /traffic` (stream, one per second)

```bash
curl -N -H "$AUTH" $API/traffic
```
```json
{"up":12345,"down":678901,"upTotal":10485760,"downTotal":524288000}
```

| Field | Meaning |
|---|---|
| `up` / `down` | Upload and download rate over the last second, in **bytes per second** |
| `upTotal` / `downTotal` | Bytes since start, closed connections included |

- **Client:** counts the **payload bytes** of every connection through the mixed proxy, proxied and direct alike. Upload is the direction toward the destination.
- **Server:** the sum of all users' wire bytes, counted the same way as `up`/`down` in `/users`.

### `GET /memory` (stream, one per second)

```bash
curl -N -H "$AUTH" $API/memory
```
```json
{"inuse":19136512,"oslimit":0}
```

- `inuse`: bytes the Go runtime currently holds from the OS, close to the process's RSS.
- `oslimit`: always `0`, meaning no limit.

### `GET /logs` (stream)

| Param | Values | Default |
|---|---|---|
| `level` | `debug` / `info` / `warning` / `error` / `silent` | `info` |
| `format` | `structured` switches to the structured shape | plain |

The stream carries only lines at `level` or above; `silent` sends nothing. An invalid `level` returns `400 Body invalid`.

```bash
curl -N -H "$AUTH" "$API/logs?level=info"
```

Plain:

```json
{"type":"info","payload":"[API] Proxy mode switched to global"}
```

`type` is `debug`, `info`, `warning` or `error`. `payload` is `[Component] message` with terminal colors removed.

Structured (`?format=structured`):

```json
{"time":"15:04:05","level":"warn","message":"[Proxy] Dial Failed: ...","fields":[]}
```

In structured lines the warning level is spelled `warn`, as in mihomo.

- You only get lines logged after you subscribe; there is no replay.
- Each subscriber has a 256-line buffer. When a reader falls further behind, lines are dropped rather than slowing the proxy.

---

## 6. Client endpoints

### 6.1 Outbounds and groups

The client maps its nodes and routing onto mihomo proxies and groups. `/proxies` lists them in this order:

| Name | type | Meaning |
|---|---|---|
| `PROXY` | `Selector` | The outbound for proxied traffic. Members are `Balance` (if present) and every node. Defaults to `Balance`, or to the only node when there is one. |
| `Balance` | `LoadBalance` | Spreads connections over all nodes, as the client does without a panel. **Exists only with two or more nodes.** |
| `GLOBAL` | `Selector` | The outbound global mode uses. Members are `PROXY` plus everything in `PROXY`. Defaults to `PROXY`. |
| each node | `Sudoku` | Named after the config's `name`, or the node ID when there is none. Duplicate names get `#2`, `#3`, … |
| `DIRECT` | `Direct` | Direct connection |
| `REJECT` | `Reject` | Reject |

- A pick applies to **new connections**; established ones are unaffected. Picks are **not persisted**: a restart restores the defaults.
- A node's `udp` / `uot` tells whether it supports UDP-over-TCP.

### 6.2 `GET /configs`

```bash
curl -H "$AUTH" $API/configs
```
```json
{
  "port": 0, "socks-port": 0, "redir-port": 0, "tproxy-port": 0,
  "mixed-port": 10233,
  "allow-lan": true, "bind-address": "*",
  "mode": "rule",
  "log-level": "info", "ipv6": true, "unified-delay": false,
  "tun": {"enable": false}
}
```

Only `mixed-port` and `mode` reflect sudoku's real state. The other fields hold mihomo's "off" values so panels render correctly.

`mode` is `rule`, `global` or `direct`, where `rule` is sudoku's PAC mode.

### 6.3 `PATCH /configs`: switch mode

```bash
curl -X PATCH -H "$AUTH" -H 'Content-Type: application/json' \
     -d '{"mode":"global"}' $API/configs
```

Returns `204` on success.

| `mode` (case-insensitive) | Effect |
|---|---|
| `rule` or `pac` | PAC routing: rule providers decide between direct, proxy and reject |
| `global` or `proxy` | Proxy everything (reject rules still apply), through the `GLOBAL` group |
| `direct` | Connect everything directly (reject rules still apply) |

- The switch applies to **new connections**; established ones keep their route.
- If the config has no `rule_urls`, switching to `rule` loads the default PAC rule sources.
- The switch is not written back to the config file, so a restart restores the configured mode.
- Other body fields (e.g. `log-level`, `allow-lan`) are ignored.
- The body limit is 64KB. An unknown mode or invalid JSON returns `400 Body invalid`.

### 6.4 `GET /proxies`

```bash
curl -H "$AUTH" $API/proxies
```

Returns `{"proxies": {name: proxy}}` with every entry from 6.1. The excerpt below is for a client with two nodes, `hk` and `jp`:

```json
{
  "proxies": {
    "PROXY": {
      "name": "PROXY", "type": "Selector", "id": "PROXY",
      "history": [], "extra": {}, "alive": true,
      "udp": true, "uot": false, "xudp": false, "tfo": false, "mptcp": false, "smux": false,
      "interface": "", "routing-mark": 0, "provider-name": "", "dialer-proxy": "",
      "now": "Balance", "all": ["Balance", "hk", "jp"],
      "testUrl": "", "hidden": false, "icon": ""
    },
    "Balance": {
      "name": "Balance", "type": "LoadBalance", "...": "...",
      "all": ["hk", "jp"], "testUrl": "", "hidden": false, "icon": ""
    },
    "hk": {
      "name": "hk", "type": "Sudoku", "id": "hk",
      "history": [{"time": "2026-10-18T12:00:00.123+08:00", "delay": 87}],
      "extra": {
        "https://www.gstatic.com/generate_204": {
          "history": [{"time": "2026-10-18T12:00:00.123+08:00", "delay": 87}],
          "alive": true
        }
      },
      "alive": true, "udp": true, "uot": true, "...": "..."
    },
    "DIRECT": {"name": "DIRECT", "type": "Direct", "...": "..."},
    "REJECT": {"name": "REJECT", "type": "Reject", "...": "..."}
  }
}
```

| Field | Meaning |
|---|---|
| `name` / `id` | Name; `id` equals `name` |
| `type` | `Selector` / `LoadBalance` / `Sudoku` / `Direct` / `Reject` |
| `history` | The last 10 test results. `delay` is in ms; `0` means the test failed |
| `extra` | History per test URL, for up to 8 URLs per proxy |
| `alive` | Whether the last test succeeded. `true` for a proxy never tested |
| `udp` / `uot` | UDP support (sudoku carries UDP over TCP) |
| `now` | A Selector's current pick (absent on `LoadBalance`) |
| `all` | Group members (groups only) |
| `testUrl` / `hidden` / `icon` | Group display fields. An empty `testUrl` makes panels use their own default |
| other fields | mihomo compatibility fields, always false, 0 or empty |

### 6.5 `GET /proxies/{name}`

```bash
curl -H "$AUTH" $API/proxies/PROXY
```

Returns one proxy object, shaped as above, or `404 Resource not found`.

URL-encode names that contain spaces, `#` or non-ASCII characters, e.g. `香港#2` becomes `%E9%A6%99%E6%B8%AF%232`:

```bash
curl -H "$AUTH" "$API/proxies/$(python3 -c 'import urllib.parse;print(urllib.parse.quote("香港#2",safe=""))')"
```

### 6.6 `PUT /proxies/{name}`: pick a node

```bash
# send proxied traffic through jp only
curl -X PUT -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"jp"}' $API/proxies/PROXY
# back to load balancing
curl -X PUT -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"Balance"}' $API/proxies/PROXY
# pin global mode to hk, regardless of PROXY
curl -X PUT -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"hk"}' $API/proxies/GLOBAL
```

| Outcome | Response |
|---|---|
| Success | `204` |
| No such proxy | `404 Resource not found` |
| Unparsable body (4KB limit) | `400 Body invalid` |
| Not a Selector (only `PROXY` and `GLOBAL` are) | `400 Must be a Selector` |
| Member not in the group | `400 Selector update error: proxy "xx" not found` |

`GLOBAL`'s members are `PROXY`, `Balance` and the nodes, not `DIRECT` or `REJECT`. To send everything direct, switch the mode to `direct`.

### 6.7 `DELETE /proxies/{name}`

In mihomo this endpoint clears a manual pin on URLTest or Fallback groups. Sudoku only has Selectors, whose pick is the whole point, so for any existing proxy it returns `400 Must be a Selector`. An unknown name returns `404`.

### 6.8 `GET /proxies/{name}/delay`: test one proxy

| Param | Required | Meaning |
|---|:-:|---|
| `timeout` | ✓ | Timeout in ms, a positive integer |
| `url` | | Test URL, http or https only. Default: `https://www.gstatic.com/generate_204` |
| `expected` | | Accepted status codes, e.g. `204`, `200-299` or `200/204/301-302`. Empty or `*` accepts any status |

```bash
curl -H "$AUTH" "$API/proxies/hk/delay?timeout=5000&url=https%3A%2F%2Fwww.gstatic.com%2Fgenerate_204&expected=204"
```
```json
{"delay":87}
```

- The test dials through the outbound exactly as traffic does.
- Timing runs from the start of the dial to the response headers of a `HEAD` request, so it includes setting up the tunnel and the TLS handshake. This matches mihomo with `unified-delay: false`.
- Redirects are not followed, and every test uses a fresh connection.
- Testing a group (`PROXY`, `GLOBAL` or `Balance`) tests its current pick.
- `DIRECT` dials directly; `REJECT` always fails.
- Each result is recorded in the proxy's `history` and `extra[url]`. A failure is recorded as `delay: 0` and sets `alive` to `false`.

| Outcome | Response |
|---|---|
| Success | `200 {"delay": ms}`, at least 1 |
| Timed out | `504 {"message":"Timeout"}` |
| Failed (dial error, unexpected status, …) | `503 {"message":"An error occurred in the delay test"}` |
| `timeout` missing or invalid, bad `expected` | `400 Body invalid` |
| No such proxy | `404 Resource not found` |

### 6.9 `GET /group`, `GET /group/{name}`

```bash
curl -H "$AUTH" $API/group          # {"proxies":[PROXY, Balance, GLOBAL]}
curl -H "$AUTH" $API/group/GLOBAL   # one group object
```

`/group` returns an **array** in the order PROXY, Balance, GLOBAL. A name that is not a group returns `404`.

### 6.10 `GET /group/{name}/delay`: test a whole group

The parameters are the same as in 6.8. Every member is tested at once, and the whole request shares one `timeout`.

```bash
curl -H "$AUTH" "$API/group/PROXY/delay?timeout=5000"
```
```json
{"Balance":92,"hk":87,"jp":143}
```

- Only members that answered appear in the result. Failures are left out, but are still recorded in each member's `history`.
- If every member fails, the response is `504 {"message":"get delay: all proxies timeout"}`.

### 6.11 `GET /providers/proxies`

```json
{"providers":{}}
```

Sudoku has no proxy providers. This endpoint always returns an empty object, so panels load normally.

### 6.12 `GET /rules`

```bash
curl -H "$AUTH" $API/rules
```
```json
{
  "rules": [
    {"index":0,"type":"RuleSet","payload":"reject","proxy":"REJECT","size":-1},
    {"index":1,"type":"RuleSet","payload":"proxy","proxy":"PROXY","size":-1},
    {"index":2,"type":"RuleSet","payload":"direct","proxy":"DIRECT","size":-1},
    {"index":3,"type":"Match","payload":"","proxy":"PROXY","size":-1}
  ]
}
```

Each rule source appears as one `RuleSet`, in match order, followed by the final `Match`:

| Provider | Source (`rule_urls`) | Target | Present |
|---|---|---|---|
| `reject` | `!https://...`, plus the default ad-block list loaded in global and direct modes | `REJECT` | when there are reject rules |
| `proxy` | `-https://...` / `_https://...` | `PROXY` in rule mode, `GLOBAL` in global mode | when there are proxy rules |
| `direct` | plain URLs | `DIRECT` | **rule mode only** |
| `Match` | — | `PROXY` in rule mode, `GLOBAL` in global mode, `DIRECT` in direct mode | always |

The list changes as soon as the mode does.

### 6.13 `GET /providers/rules`

```bash
curl -H "$AUTH" $API/providers/rules
```
```json
{
  "providers": {
    "direct": {
      "name": "direct", "type": "Rule", "vehicleType": "HTTP",
      "behavior": "Classical", "format": "TextRule",
      "ruleCount": 98765, "updatedAt": "2026-10-18T11:58:02.5+08:00"
    }
  }
}
```

`ruleCount` is the number of rules loaded, and `updatedAt` is the time of the last successful load. Until the first load finishes, they are 0 and the zero time.

### 6.14 `PUT /providers/rules/{name}`: update a rule provider

```bash
curl -X PUT -H "$AUTH" $API/providers/rules/direct
```

- Returns `204` right away and downloads in the background. Read `/providers/rules` afterwards to see the new `ruleCount` and `updatedAt`.
- A name that does not exist, or is not active in the current mode, returns `404 Resource not found`.

### 6.15 `GET /connections`

```bash
curl -H "$AUTH" $API/connections
```
```json
{
  "downloadTotal": 524288000,
  "uploadTotal": 10485760,
  "memory": 19136512,
  "connections": [
    {
      "id": "3f2b8c1e-9a4d-4e57-8b21-6c0d5e7f9a12",
      "metadata": {
        "network": "tcp", "type": "Socks5",
        "sourceIP": "127.0.0.1", "sourcePort": "53122",
        "destinationIP": "", "destinationPort": "443",
        "host": "www.example.com",
        "inboundIP": "127.0.0.1", "inboundPort": "10233",
        "inboundName": "DEFAULT-MIXED", "inboundUser": "",
        "dnsMode": "normal", "remoteDestination": "",
        "sourceGeoIP": null, "destinationGeoIP": null,
        "sourceIPASN": "", "destinationIPASN": "",
        "uid": 0, "process": "", "processPath": "",
        "specialProxy": "", "specialRules": "", "dscp": 0, "sniffHost": ""
      },
      "upload": 2048, "download": 1048576,
      "start": "2026-10-18T12:00:01.234+08:00",
      "chains": ["hk", "Balance", "PROXY"],
      "providerChains": [],
      "rule": "Match", "rulePayload": ""
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `downloadTotal` / `uploadTotal` | Same totals as `/traffic` |
| `memory` | Same as `inuse` in `/memory` |
| `id` | Connection ID (UUID v4), used by `DELETE /connections/{id}` |
| `metadata.network` | `tcp` or `udp` |
| `metadata.type` | Inbound protocol: `HTTP` (plain proxy), `HTTPS` (CONNECT), `Socks4` or `Socks5` |
| `metadata.host` / `destinationIP` | `host` for a domain destination, `destinationIP` for an IP |
| `metadata.remoteDestination` | The `IP:port` actually dialed, for direct connections |
| `upload` / `download` | Payload bytes of this connection; upload is toward the destination |
| `start` | When the connection opened |
| `chains` | From the outbound actually used up to the outermost group, e.g. `["hk","Balance","PROXY"]`, `["jp","PROXY","GLOBAL"]` or `["DIRECT"]` |
| `rule` / `rulePayload` | Kind and content of the matching rule; `Match` when the mode alone decided |

The other metadata fields have no meaning in sudoku. They are present but empty so panels can read them.

**WebSocket:** panels subscribe over a WebSocket and set the interval with `?interval=<ms>`.

- The default is 1000; anything below 100 is raised to 100.
- An `interval` that is not a positive integer returns `400 Body invalid`.

```bash
websocat 'ws://127.0.0.1:9090/connections?token=change-me&interval=2000'
```

**UDP:** each destination of a SOCKS5 UDP ASSOCIATE shows as one `network: "udp"` connection, as in mihomo.

- At most 256 destinations are shown per association; past that, the least recently active one is ended.
- An entry idle for 2 minutes is ended.
- They all share one association, so closing **any one** of them ends the whole association.

### 6.16 `DELETE /connections`, `DELETE /connections/{id}`

```bash
curl -X DELETE -H "$AUTH" $API/connections                                        # close all
curl -X DELETE -H "$AUTH" $API/connections/3f2b8c1e-9a4d-4e57-8b21-6c0d5e7f9a12   # close one
```

Both return `204`, including when the ID is unknown, e.g. because the connection already ended.

---

## 7. Server endpoints

The server API is read-only: `/`, `/version`, `/traffic`, `/memory`, `/logs` and `/users` below. The client routes do not exist on a server and return 404.

### `GET /users`

```bash
curl -H "$AUTH" $API/users
```
```json
{
  "count": 2,
  "active": 1,
  "up": 1000,
  "down": 3000,
  "users": [
    {
      "name": "alice@example.com",
      "blocked": false, "exceeded": false,
      "conns": 1,
      "up": 1000, "down": 3000,
      "upTotal": 3000, "downTotal": 10000,
      "used": 13000, "limit": 100000000000
    },
    {
      "name": "8531a778fe7c1d2e",
      "blocked": false, "exceeded": false,
      "conns": 0,
      "up": 0, "down": 0,
      "upTotal": 0, "downTotal": 0,
      "used": 0, "limit": 0
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `count` | Users in the config |
| `active` | Users with at least one open connection |
| `up` / `down` | Sum of all users' rates (bytes per second) |
| `users[].name` | The `alias`, or the 16-char user hash when unset (never the private key) |
| `users[].blocked` | `blocked` is set in the config |
| `users[].exceeded` | This month's data quota is used up |
| `users[].conns` | Open connections |
| `users[].up` / `down` | Rate over the last second (bytes per second). `up` is from the user (their upload); `down` is to the user (their download) |
| `users[].upTotal` / `downTotal` | Bytes since start |
| `users[].used` | Bytes used **this month**, counted against the quota. Kept across restarts and reset monthly |
| `users[].limit` | Monthly quota in bytes; `0` means unlimited |

- All figures count wire bytes (the obfuscated on-the-wire traffic, both directions), the same measure as the `data` quota.
- Users are listed in the order of the config's `users`.
- Without `users` configured, the response is `{"count":0,"active":0,"up":0,"down":0,"users":[]}`.
- Data is sampled once per second; a plain GET returns the latest sample.

**WebSocket** (one message per second, same shape):

```bash
websocat 'ws://SERVER:9090/users?token=change-me'
```

**Script example:** list active users with their live rate and this month's usage, using `jq`:

```bash
curl -s -H "$AUTH" $API/users | jq -r '.users[] | select(.conns > 0)
  | "\(.name)\t↑\(.up/1024|floor)KB/s\t↓\(.down/1024|floor)KB/s\tused \(.used/1e9*100|floor/100)GB"'
```

---

## 8. mihomo endpoints not implemented

Sudoku has nothing behind the following endpoints, so they return 404 or 405:

- `PUT /configs` (reload config), `POST /configs/geo`, `POST /restart`, `POST /upgrade*`, `POST /cache/*`
- `/dns/query`, `/dns/*`
- `/providers/proxies/{name}` (and its healthcheck)
- `/rules/disable`, `/debug/*`, `/storage/*`

The matching panel buttons (restart, upgrade core, flush FakeIP, …) report a failure; everything else keeps working.

## 9. Resource cost

- **Off** (the default): no listener and no goroutines. The proxy paths carry no extra work.
- **On:**
  - Each connection carries about 0.6KB of tracking state, and byte counting is one atomic add.
  - Tunnel connections keep their zero-copy and direct-write paths, with no extra buffer or copy.
  - One background goroutine samples rates once per second for every panel, so more panels cost nothing extra.
  - With 1000 connections, a `/connections` snapshot takes about 1.4ms and 650KB of short-lived memory. The WebSocket interval floor of 100ms keeps a panel from rebuilding the list in a tight loop.
