# Sudoku 外部控制器 API（RESTful）

[English](./api.en.md) | 中文

Sudoku 内置一个与 Clash / mihomo 语义一致的 RESTful API（俗称 external controller）。路由、JSON 字段、状态码和错误信息都参照 [MetaCubeX/mihomo Alpha](https://github.com/MetaCubeX/mihomo/tree/Alpha)（[官方文档](https://wiki.metacubex.one/api/)）。因此 [zashboard](https://github.com/Zephyruso/zashboard)、[metacubexd](https://github.com/MetaCubeX/metacubexd)、[yacd](https://github.com/MetaCubeX/Yacd-meta) 等面板可以直接接入。自己写脚本或程序调用也可以。

- 客户端：可以查看节点、分组、分流规则、实时带宽、活跃连接和日志；可以测延迟、切换节点、切换 `rule` / `global` / `direct` 模式、关闭连接。
- 服务端：只读，可以查看用户数、活跃用户、每个用户的实时上下行速率和流量用量，另有带宽、内存和日志。

目录：

- [1. 开启](#1-开启)
- [2. 接入面板](#2-接入面板)
- [3. 通用约定](#3-通用约定)（鉴权、CORS、错误格式、流式接口）
- [4. 路由总览](#4-路由总览)
- [5. 通用接口（客户端与服务端）](#5-通用接口客户端与服务端)
- [6. 客户端接口](#6-客户端接口)
- [7. 服务端接口](#7-服务端接口)
- [8. 未实现的 mihomo 接口](#8-未实现的-mihomo-接口)
- [9. 资源占用](#9-资源占用)

---

## 1. 开启

在**客户端或服务端**的 JSON 配置里加入：

```json
{
  "external_controller": "127.0.0.1:9090",
  "secret": "change-me"
}
```

| 字段 | 说明 |
|---|---|
| `external_controller` | API 监听地址，必须是 `host:port`，端口 1–65535。示例：`127.0.0.1:9090`、`[::1]:9090`、`:9090`（所有网卡）、`0.0.0.0:9090`。不填或留空表示关闭。 |
| `secret` | 访问口令（Bearer token）。留空表示不鉴权。 |

- 启动后日志会输出 `[API] External controller on http://127.0.0.1:9090`。
- 如果监听的不是回环地址且 `secret` 为空，会打印一条警告：能连上该端口的任何人都能查看并操控这个节点。暴露到公网时**务必**设置 `secret`，最好再套一层防火墙或 SSH 隧道。
- 多节点客户端只读取**第一个**配置里的 `external_controller` 和 `secret`。
- 用 `-link` 短链直接启动的客户端没有配置文件，所以不会开启 API。需要 API 时请先用 `-c` 加载配置。

## 2. 接入面板

以 zashboard 为例，metacubexd 和 yacd 也一样：

1. 打开面板（可以用在线版，也可以自己部署）。
2. 添加后端：
   - 地址填 `http://127.0.0.1:9090`，即 `external_controller` 的地址。如果监听的是 `:9090` 或 `0.0.0.0:9090`，就填本机或服务器的实际 IP。
   - 口令填 `secret`。
3. 连接成功后能看到：
   - 代理页：`PROXY` / `GLOBAL` / `Balance` 分组和各节点，可以点选、测速。
   - 规则页：规则源（rule provider）及其条数，可以手动更新。
   - 连接页：实时连接，可以断开。
   - 概览页：带宽曲线和内存。
   - 日志页：实时日志。
   - 顶部模式切换：rule / global / direct。

说明：

- 允许任意来源（CORS `*`），并支持 Chrome 的 Private Network Access 预检，所以 HTTPS 在线面板也能访问内网的 `http://` 后端。如果浏览器仍然拦截混合内容，请自部署面板，或者用 HTTP 打开面板。
- 服务端的 API 只有 `/traffic`、`/memory`、`/logs`、`/version`、`/users`。面板接入服务端时，代理、规则、连接页会是空的或报 404，这是正常现象。服务端的用户数据请用脚本读取 `/users`（见 [第 7 节](#7-服务端接口)）。

## 3. 通用约定

### 3.1 鉴权

设置了 `secret` 时，所有请求都要带：

```
Authorization: Bearer <secret>
```

浏览器的 WebSocket 不能自定义请求头，所以 **WebSocket 请求**也可以把口令放在查询参数里：`?token=<secret>`。普通 HTTP 请求不接受 `?token=`。

鉴权失败返回：

```
HTTP/1.1 401 Unauthorized
{"message":"Unauthorized"}
```

口令比较采用常量时间。没有设置 `secret` 时不做任何鉴权。

### 3.2 CORS

- 请求带 `Origin` 时，响应会带 `Access-Control-Allow-Origin: *`。
- `OPTIONS` 预检直接返回 `204`，并带上以下头：
  - `Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE`
  - `Access-Control-Allow-Headers: Content-Type, Authorization`
  - `Access-Control-Max-Age: 300`
  - 请求带 `Access-Control-Request-Private-Network: true` 时，再加上 `Access-Control-Allow-Private-Network: true`。
- 预检不需要口令。

### 3.3 响应与错误

- 成功时返回 JSON（`Content-Type: application/json; charset=utf-8`）。没有返回内容的操作返回 `204 No Content`。
- 失败时统一返回 `{"message":"..."}`，常见的有：

| 状态码 | message | 场景 |
|---|---|---|
| 400 | `Body invalid` | 请求体或参数无法解析、取值非法 |
| 400 | `Must be a Selector` | 对非 Selector 分组做切换或取消固定 |
| 400 | `Selector update error: ...` | 要切换到的成员不在分组里 |
| 401 | `Unauthorized` | 口令错误或缺失 |
| 404 | `Resource not found` | 代理、分组或规则源不存在 |
| 503 | `An error occurred in the delay test` | 测速失败（连接失败、状态码不符等） |
| 504 | `Timeout` / `get delay: all proxies timeout` | 测速超时 |

- 不存在的路由返回 Go 标准库的 `404 page not found`（纯文本），方法不匹配返回 `405`。

### 3.4 流式接口

`/traffic`、`/memory`、`/logs` 是持续推送的流，`/connections`、`/users` 可以选择以流的方式推送。推送方式与 mihomo 相同，有两种：

1. **WebSocket**（面板使用这种）：请求带 `Upgrade: websocket` 时升级。每条消息是一个 JSON 文本帧。客户端不需要发送任何内容，服务端会自动回应 ping。
   ```bash
   websocat 'ws://127.0.0.1:9090/traffic?token=change-me'
   ```
2. **HTTP 分块（NDJSON）**：普通 GET 请求返回 `200` 后不断开，每行一个 JSON 对象。`curl` 要加 `-N` 关闭缓冲：
   ```bash
   curl -N -H 'Authorization: Bearer change-me' http://127.0.0.1:9090/traffic
   ```

说明：

- `/connections` 和 `/users` 只有通过 WebSocket 请求时才持续推送；普通 GET 只返回一次快照。
- 连接建立后会立即推送第一条数据，之后按固定间隔继续推送。
- 单条写入超过 10 秒未完成时，服务端会断开该流。
- 程序退出时所有流都会关闭。

---

## 4. 路由总览

| 方法 | 路径 | 客户端 | 服务端 | 说明 |
|---|---|:-:|:-:|---|
| GET | `/` | ✓ | ✓ | 探活 |
| GET | `/version` | ✓ | ✓ | 版本 |
| GET | `/traffic` | ✓ | ✓ | 实时带宽（流） |
| GET | `/memory` | ✓ | ✓ | 内存占用（流） |
| GET | `/logs` | ✓ | ✓ | 实时日志（流） |
| GET | `/configs` | ✓ | | 当前配置与模式 |
| PATCH | `/configs` | ✓ | | 切换模式 |
| GET | `/proxies` | ✓ | | 所有代理与分组 |
| GET | `/proxies/{name}` | ✓ | | 单个代理 |
| PUT | `/proxies/{name}` | ✓ | | 切换 Selector 选中项 |
| DELETE | `/proxies/{name}` | ✓ | | 取消固定（Selector 恒返回 400） |
| GET | `/proxies/{name}/delay` | ✓ | | 单个代理测速 |
| GET | `/group` | ✓ | | 所有分组 |
| GET | `/group/{name}` | ✓ | | 单个分组 |
| GET | `/group/{name}/delay` | ✓ | | 分组内全部成员并发测速 |
| GET | `/providers/proxies` | ✓ | | 代理集合（恒为空） |
| GET | `/rules` | ✓ | | 分流规则 |
| GET | `/providers/rules` | ✓ | | 规则源 |
| PUT | `/providers/rules/{name}` | ✓ | | 重新下载规则源 |
| GET | `/connections` | ✓ | | 活跃连接（WebSocket 时为流） |
| DELETE | `/connections` | ✓ | | 关闭全部连接 |
| DELETE | `/connections/{id}` | ✓ | | 关闭单个连接 |
| GET | `/users` | | ✓ | 用户统计（WebSocket 时为流） |

下文的示例统一假设：

```bash
API=http://127.0.0.1:9090
AUTH='Authorization: Bearer change-me'
```

---

## 5. 通用接口（客户端与服务端）

### `GET /`

```bash
curl -H "$AUTH" $API/
```
```json
{"hello":"mihomo"}
```

固定返回 `"mihomo"`，因为部分面板靠这个字段识别后端类型。

### `GET /version`

```bash
curl -H "$AUTH" $API/version
```
```json
{"meta":true,"version":"sudoku v0.5.2"}
```

- `version` 是构建时的模块版本。本地 `go build` 出来的版本显示为 `sudoku dev`。
- `meta: true` 表示后端兼容 mihomo（Meta），面板会据此开启对应功能。

### `GET /traffic`（流，每秒 1 条）

```bash
curl -N -H "$AUTH" $API/traffic
```
```json
{"up":12345,"down":678901,"upTotal":10485760,"downTotal":524288000}
```

| 字段 | 含义 |
|---|---|
| `up` / `down` | 过去 1 秒的上传 / 下载速率，单位**字节/秒** |
| `upTotal` / `downTotal` | 进程启动以来的累计字节，包含已关闭的连接 |

- 客户端：统计经过 mixed 代理的所有连接（代理和直连都算）的**负载字节**。上传指发往目标的方向。
- 服务端：等于所有用户的 wire 字节之和（与 `/users` 的 `up`/`down` 口径相同）。

### `GET /memory`（流，每秒 1 条）

```bash
curl -N -H "$AUTH" $API/memory
```
```json
{"inuse":19136512,"oslimit":0}
```

- `inuse`：Go 运行时当前向操作系统持有的内存（字节），接近进程 RSS。
- `oslimit`：恒为 `0`，表示无限制。

### `GET /logs`（流）

| 参数 | 取值 | 默认 |
|---|---|---|
| `level` | `debug` / `info` / `warning` / `error` / `silent` | `info` |
| `format` | `structured` 时使用结构化格式 | 普通格式 |

只推送不低于 `level` 的日志。`silent` 表示不推送任何日志。`level` 取值非法时返回 `400 Body invalid`。

```bash
curl -N -H "$AUTH" "$API/logs?level=info"
```

普通格式：

```json
{"type":"info","payload":"[API] Proxy mode switched to global"}
```

`type` 取 `debug` / `info` / `warning` / `error`。`payload` 是 `[组件] 消息`，已去掉终端颜色。

结构化格式（`?format=structured`）：

```json
{"time":"15:04:05","level":"warn","message":"[Proxy] Dial Failed: ...","fields":[]}
```

结构化格式下，警告级别写作 `warn`，与 mihomo 一致。

- 只能收到订阅之后产生的日志，没有历史回放。
- 每个订阅者有 256 条缓冲。消费太慢时多出的日志会被丢弃，不会拖慢代理。

---

## 6. 客户端接口

### 6.1 出站与分组模型

客户端把节点和路由映射成 mihomo 的代理与分组，`/proxies` 按以下顺序列出：

| 名称 | type | 说明 |
|---|---|---|
| `PROXY` | `Selector` | 代理流量使用的出站。成员是 `Balance`（如果有）和所有节点。默认选中 `Balance`，只有一个节点时选中该节点。 |
| `Balance` | `LoadBalance` | 在所有节点之间做负载均衡（与不接面板时的行为相同）。**只在有 2 个及以上节点时出现**。 |
| `GLOBAL` | `Selector` | global 模式使用的出站。成员是 `PROXY` 加上 `PROXY` 的所有成员，默认 `PROXY`。 |
| 各节点 | `Sudoku` | 名称取配置中的 `name`，没有则取节点 ID。重名时自动加 `#2`、`#3` 等后缀。 |
| `DIRECT` | `Direct` | 直连 |
| `REJECT` | `Reject` | 拒绝 |

- 选中项只作用于**新连接**，已建立的连接不受影响；选中项也**不会持久化**，重启后恢复默认。
- 节点的 `udp` / `uot` 表示该节点是否支持 UDP-over-TCP。

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

只有 `mixed-port` 和 `mode` 反映 sudoku 的实际状态，其余字段是 mihomo 中表示“未启用”的固定值，用于兼容面板。

`mode` 取 `rule`、`global` 或 `direct`。其中 `rule` 就是 sudoku 的 PAC 模式。

### 6.3 `PATCH /configs`：切换模式

```bash
curl -X PATCH -H "$AUTH" -H 'Content-Type: application/json' \
     -d '{"mode":"global"}' $API/configs
```

成功返回 `204`。

| `mode` 取值（不区分大小写） | 效果 |
|---|---|
| `rule` 或 `pac` | PAC 分流：按规则源决定直连、代理或拒绝 |
| `global` 或 `proxy` | 全局代理（仍执行 reject 规则），经由 `GLOBAL` 分组 |
| `direct` | 全部直连（仍执行 reject 规则） |

- 只作用于**新连接**，已建立的连接保持原来的路由。
- 如果配置里没有 `rule_urls`，切到 `rule` 时会自动加载默认的 PAC 规则源。
- 切换结果不会写回配置文件，重启后恢复为配置中的模式。
- 请求体中的其他字段（例如 `log-level`、`allow-lan`）会被忽略。
- 请求体上限 64KB。`mode` 取值非法或 JSON 无法解析时返回 `400 Body invalid`。

### 6.4 `GET /proxies`

```bash
curl -H "$AUTH" $API/proxies
```

返回 `{"proxies": {名称: 代理对象}}`，包含 6.1 节列出的全部条目。以有两个节点 `hk`、`jp` 的客户端为例（节选）：

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

| 字段 | 含义 |
|---|---|
| `name` / `id` | 名称（`id` 与 `name` 相同） |
| `type` | `Selector` / `LoadBalance` / `Sudoku` / `Direct` / `Reject` |
| `history` | 最近 10 次测速记录，`delay` 单位为毫秒，`0` 表示失败 |
| `extra` | 按测速 URL 分别记录的历史，每个代理最多记录 8 个 URL |
| `alive` | 最近一次测速是否成功。从未测过时为 `true` |
| `udp` / `uot` | 是否支持 UDP（sudoku 通过 UDP-over-TCP 实现） |
| `now` | Selector 当前选中的成员（`LoadBalance` 没有这个字段） |
| `all` | 分组成员（只有分组有这个字段） |
| `testUrl` / `hidden` / `icon` | 分组的展示字段。`testUrl` 为空表示面板使用自己的默认测速 URL |
| 其余字段 | mihomo 兼容字段，取值恒为 false / 0 / 空 |

### 6.5 `GET /proxies/{name}`

```bash
curl -H "$AUTH" $API/proxies/PROXY
```

返回单个代理对象，格式同上。不存在时返回 `404 Resource not found`。

名称含空格、`#` 或非 ASCII 字符时要先 URL 编码，例如 `香港#2` 写作 `%E9%A6%99%E6%B8%AF%232`：

```bash
curl -H "$AUTH" "$API/proxies/$(python3 -c 'import urllib.parse;print(urllib.parse.quote("香港#2",safe=""))')"
```

### 6.6 `PUT /proxies/{name}`：切换节点

```bash
# 代理流量固定走 jp
curl -X PUT -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"jp"}' $API/proxies/PROXY
# 恢复负载均衡
curl -X PUT -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"Balance"}' $API/proxies/PROXY
# global 模式固定走 hk，不随 PROXY 变化
curl -X PUT -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"hk"}' $API/proxies/GLOBAL
```

| 结果 | 响应 |
|---|---|
| 成功 | `204` |
| 代理不存在 | `404 Resource not found` |
| 请求体无法解析（上限 4KB） | `400 Body invalid` |
| 目标不是 Selector（只有 `PROXY`、`GLOBAL` 可以切换） | `400 Must be a Selector` |
| 成员不在分组里 | `400 Selector update error: proxy "xx" not found` |

`GLOBAL` 的成员是 `PROXY`、`Balance` 和各节点，不包含 `DIRECT` / `REJECT`。要整体直连，请把模式切到 `direct`。

### 6.7 `DELETE /proxies/{name}`

在 mihomo 中，这个接口用于取消 URLTest 或 Fallback 分组的手动固定。sudoku 只有 Selector，而 Selector 的选择本身就是固定的，所以这个接口对存在的代理恒返回 `400 Must be a Selector`，代理不存在时返回 `404`。

### 6.8 `GET /proxies/{name}/delay`：单个代理测速

| 参数 | 必填 | 说明 |
|---|:-:|---|
| `timeout` | ✓ | 超时时间，毫秒，正整数 |
| `url` | | 测速地址，只支持 http/https，默认 `https://www.gstatic.com/generate_204` |
| `expected` | | 期望的状态码，例如 `204`、`200-299`、`200/204/301-302`。不填或填 `*` 表示接受任意状态码 |

```bash
curl -H "$AUTH" "$API/proxies/hk/delay?timeout=5000&url=https%3A%2F%2Fwww.gstatic.com%2Fgenerate_204&expected=204"
```
```json
{"delay":87}
```

- 测速通过该出站真实拨号，路径与实际流量完全相同。
- 计时方式：从开始拨号到收到 `HEAD` 请求的响应头，包含建立隧道和 TLS 握手，等同于 mihomo 中 `unified-delay: false` 的口径。
- 不跟随重定向，每次测速都新建连接。
- 测速分组（`PROXY` / `GLOBAL` / `Balance`）时，测的是它当前选中的出站。
- 测 `DIRECT` 时直连拨号；测 `REJECT` 恒失败。
- 每次结果都会写入该代理的 `history` 和 `extra[url]`，失败记为 `delay: 0`，`alive` 变为 `false`。

| 结果 | 响应 |
|---|---|
| 成功 | `200 {"delay": 毫秒}`，最小为 1 |
| 超时 | `504 {"message":"Timeout"}` |
| 失败（拨号失败、状态码不符等） | `503 {"message":"An error occurred in the delay test"}` |
| `timeout` 缺失或非法、`expected` 格式错误 | `400 Body invalid` |
| 代理不存在 | `404 Resource not found` |

### 6.9 `GET /group`、`GET /group/{name}`

```bash
curl -H "$AUTH" $API/group          # {"proxies":[PROXY, Balance, GLOBAL]}
curl -H "$AUTH" $API/group/GLOBAL   # 单个分组对象
```

`/group` 返回的是**数组**，按 PROXY、Balance、GLOBAL 的顺序排列。名称不是分组时返回 `404`。

### 6.10 `GET /group/{name}/delay`：分组并发测速

参数与 6.8 相同。分组的所有成员会同时测速，整个请求共用一个 `timeout`。

```bash
curl -H "$AUTH" "$API/group/PROXY/delay?timeout=5000"
```
```json
{"Balance":92,"hk":87,"jp":143}
```

- 只返回测速成功的成员，失败的成员不出现在结果中，但同样会记入各自的 `history`。
- 全部失败时返回 `504 {"message":"get delay: all proxies timeout"}`。

### 6.11 `GET /providers/proxies`

```json
{"providers":{}}
```

sudoku 没有代理集合（proxy provider），恒返回空对象，只是为了让面板正常加载。

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

每个规则源按匹配顺序列为一条 `RuleSet`，最后是兜底规则 `Match`：

| 规则源 | 来源（`rule_urls`） | 去向 | 何时出现 |
|---|---|---|---|
| `reject` | `!https://...`，以及 global/direct 模式下隐式加载的默认去广告列表 | `REJECT` | 有 reject 规则时 |
| `proxy` | `-https://...` / `_https://...` | rule 模式下为 `PROXY`，global 模式下为 `GLOBAL` | 有 proxy 规则时 |
| `direct` | 普通 URL | `DIRECT` | **只在 rule 模式下** |
| `Match` | — | rule 模式：`PROXY`；global 模式：`GLOBAL`；direct 模式：`DIRECT` | 总是出现 |

切换模式后，规则列表会立即随之变化。

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

`ruleCount` 是已加载的规则条数，`updatedAt` 是最近一次加载成功的时间。尚未加载完成时，两者分别为 0 和零值时间。

### 6.14 `PUT /providers/rules/{name}`：更新规则源

```bash
curl -X PUT -H "$AUTH" $API/providers/rules/direct
```

- 立即返回 `204`，重新下载在后台进行，完成后再读 `/providers/rules` 就能看到新的 `ruleCount` 和 `updatedAt`。
- 规则源名称不存在（或当前模式下没有启用）时返回 `404 Resource not found`。

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

| 字段 | 含义 |
|---|---|
| `downloadTotal` / `uploadTotal` | 与 `/traffic` 的累计值相同 |
| `memory` | 与 `/memory` 的 `inuse` 相同 |
| `id` | 连接 ID（UUID v4），用于 `DELETE /connections/{id}` |
| `metadata.network` | `tcp` 或 `udp` |
| `metadata.type` | 入站协议：`HTTP`（明文代理）、`HTTPS`（CONNECT）、`Socks4`、`Socks5` |
| `metadata.host` / `destinationIP` | 目标为域名时填 `host`，为 IP 时填 `destinationIP` |
| `metadata.remoteDestination` | 直连时实际连接到的 `IP:端口` |
| `upload` / `download` | 该连接的负载字节，上传指发往目标的方向 |
| `start` | 建立时间 |
| `chains` | 出站链，从实际使用的出站一直到外层分组，例如 `["hk","Balance","PROXY"]`、`["jp","PROXY","GLOBAL"]`、`["DIRECT"]` |
| `rule` / `rulePayload` | 命中的规则类型和内容；仅由模式决定时为 `Match` |

其余 metadata 字段 sudoku 不涉及，保留空值只是为了兼容面板。

**WebSocket 推送**：面板通过 WebSocket 订阅连接列表，间隔由 `?interval=毫秒` 指定，默认 1000，最小 100（小于 100 时按 100 处理）。`interval` 不是正整数时返回 `400 Body invalid`。

```bash
websocat 'ws://127.0.0.1:9090/connections?token=change-me&interval=2000'
```

**UDP**：SOCKS5 UDP ASSOCIATE 中的每个目的地址显示为一条 `network: "udp"` 连接，与 mihomo 一致。

- 一个关联最多显示 256 个目的地址，超过时最久未活动的一条会被结束。
- 空闲 2 分钟的条目会被结束。
- 它们都属于同一个 UDP 关联，所以关闭其中**任意一条**都会结束整个关联。

### 6.16 `DELETE /connections`、`DELETE /connections/{id}`

```bash
curl -X DELETE -H "$AUTH" $API/connections                                        # 关闭全部
curl -X DELETE -H "$AUTH" $API/connections/3f2b8c1e-9a4d-4e57-8b21-6c0d5e7f9a12   # 关闭一条
```

两者都返回 `204`。ID 不存在（例如连接已经结束）时也返回 `204`。

---

## 7. 服务端接口

服务端只提供只读接口：`/`、`/version`、`/traffic`、`/memory`、`/logs`，以及下面的 `/users`。服务端没有客户端的那些路由，访问会得到 404。

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

| 字段 | 含义 |
|---|---|
| `count` | 配置中的用户数 |
| `active` | 当前有连接的用户数 |
| `up` / `down` | 所有用户的速率之和（字节/秒） |
| `users[].name` | `alias`，没有设置时为 16 位用户哈希（不会暴露私钥） |
| `users[].blocked` | 配置中设置了 `blocked` |
| `users[].exceeded` | 本月流量额度已用尽 |
| `users[].conns` | 当前打开的连接数 |
| `users[].up` / `down` | 过去 1 秒的速率（字节/秒）。`up` 是来自用户的方向（用户上传），`down` 是发往用户的方向（用户下载） |
| `users[].upTotal` / `downTotal` | 进程启动以来的累计字节 |
| `users[].used` | **本月**已用的字节数，按此与额度比较。重启后仍然保留，每月自动清零 |
| `users[].limit` | 每月额度（字节），`0` 表示不限 |

- 以上统计都是 wire 字节（混淆后的线上流量，双向都计入），与 `data` 额度的口径相同。
- 数组顺序与配置中 `users` 的顺序相同。
- 没有配置 `users` 时返回 `{"count":0,"active":0,"up":0,"down":0,"users":[]}`。
- 数据每秒采样一次，普通 GET 返回最近一次采样的结果。

**WebSocket 推送**（每秒 1 条，格式同上）：

```bash
websocat 'ws://SERVER:9090/users?token=change-me'
```

**脚本示例**：用 `jq` 列出活跃用户的实时速率和本月用量：

```bash
curl -s -H "$AUTH" $API/users | jq -r '.users[] | select(.conns > 0)
  | "\(.name)\t↑\(.up/1024|floor)KB/s\t↓\(.down/1024|floor)KB/s\tused \(.used/1e9*100|floor/100)GB"'
```

---

## 8. 未实现的 mihomo 接口

以下接口 sudoku 没有对应功能，所以没有实现，访问会返回 404 或 405：

- `PUT /configs`（重载配置文件）、`POST /configs/geo`、`POST /restart`、`POST /upgrade*`、`POST /cache/*`
- `/dns/query`、`/dns/*`
- `/providers/proxies/{name}`（及其 healthcheck）
- `/rules/disable`、`/debug/*`、`/storage/*`

面板中对应的按钮（重启、升级内核、清除 FakeIP 等）点击后会提示失败，不影响其他功能。

## 9. 资源占用

- **关闭时**（默认）：不监听端口，不启动任何 goroutine，代理路径上没有任何额外开销。
- **开启时**：
  - 每条连接额外占用约 0.6KB 的统计状态，字节计数只是原子加法。
  - 隧道连接仍然走原有的零拷贝或直写路径，不增加缓冲区和复制。
  - 速率由一个后台 goroutine 每秒采样一次，所有面板共享，开再多面板也只有这一份开销。
  - 1000 条连接时，一次 `/connections` 快照约 1.4ms、650KB 临时内存。WebSocket 推送间隔最小 100ms，防止面板过于频繁地刷新连接列表。
