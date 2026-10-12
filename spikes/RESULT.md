# Task 0 — 出口与协议可行性实测

探针脚本：`spikes/egress_probe.py`（纯标准库，无需 aiohttp/requests）
原始数据：`spikes/egress_result.json`、`spikes/egress_result_proxy.json`

**状态：出口部分已完成；协议提交部分待 cookie（见 §4）。**

---

## 1. 出口对比（本机 vantage，非路由器）

| 出口 | robots.txt | 20 次稳定性 | p50 | p95 | 上游看到的出口 IP |
| --- | --- | --- | --- | --- | --- |
| `direct` | 200 | 20/20 | 519ms | 600ms | （ipify 直连不可达） |
| `proxy` `127.0.0.1:10808` | 200 | 10/10 | 2362ms | 3249ms | `23.106.131.181`（JP Osaka） |
| `relay` `vcrelay.dmean.dpdns.org` | 200 | 20/20 | 518ms | 2522ms | `1.68.94.250`（另一次实测为 `13.212.211.250`） |

relay 在本机 vantage 上 p50 最快（570ms，第二次 10 轮），但 **p95 明显更差**（2522~3941ms）——
与边缘冷启动/缓冲一致。本机不是路由器，CN vantage 才是采用 relay 的理由，这张表只用于
确认 relay 可用性与协议契约。

## 2. relay 头契约（逐项验证，全部通过）

| 探针 | 结果 | 结论 |
| --- | --- | --- |
| 缺 `x-relay-target` | `400 {"error":"Missing x-relay-target header"}` | 与 9router 实现一致 |
| `x-relay-target: https://www.dola.com` + `x-relay-path: /robots.txt` | `200`，body 为真实 `User-agent: *\nAllow:/` | **对 dola 转发可用** |
| `Cookie` + 自定义 `User-Agent`（httpbin 回显） | 原样透传 | **cookie 透传**（协议路径依赖它） |
| `POST` JSON body | 原样到达 | 请求体透传 |
| `PUT` 方法 | 正常转发 | 方法不限（TOS PUT 可走） |
| `x-relay-path: /get?aid=495671&version_code=20800&region=JP` | query 原样到达 | **path+query 拼接正确** |
| 上游 4xx | 原状态码透传（`/robots.txt` POST → 上游 405） | 错误语义不被吞 |

## 3. 三条影响设计的硬结论

### 3.1 请求体上限：4MiB 安全，5MiB 被拒 ✅ 已验证

| body | 结果 |
| --- | --- |
| 4MiB | `405`（上游 dola 拒绝 POST 方法 → **请求确实穿透了 relay**） |
| 5MiB | `413 Request Entity Too Large` / `FUNCTION_PAYLOAD_TOO_LARGE` / `sin1::...` |

→ `DOLA_RELAY_MAX_BODY=4194304`（4MiB）得到实测支持；同时 `DOLA_REFERENCE_IMAGE_MAX_BYTES`
从上游默认 15MB 收紧到 4MiB 是必要的，否则参考图会在 relay 处 413。

### 3.2 relay 在约 25s 内没拿到上游首字节就返回 504 ⚠️

`drip` 探针（上游滴流 8s）经 relay 得到：

```
status 504, first_byte 26735.9ms, body "An error occurred with your deployment"
```

同一次探针里 `direct` 到同一上游的首字节是 42.0s（httpbin 本身过载）——也就是说上游确实
没能在 25s 内出首字节。这与 [Vercel Edge 的 25s 首字节规则](https://vercel.com/docs/functions/runtimes/edge)
一致。

**据此调整默认值**：`DOLA_EGRESS_FIRST_BYTE_TIMEOUT` 由 25s 降为 **20s**。理由：留 25s 意味着
我们几乎总是先收到 relay 的 504 错误页，拿不到"是我们超时还是 relay 超时"的可归因错误；
20s 让失败由我们自己判定，并且把 relay 504 归类为可重试的出口错误。

（本项与 [Vercel Functions Limits](https://vercel.com/docs/functions/limitations) 的 300s 流式上限、
[调用超时错误](https://vercel.com/docs/errors/edge_function_invocation_timeout) 一致。）

### 3.3 relay 出口 IP 不稳定 ⚠️

两次实测得到 `13.212.211.250`（AWS 新加坡）与 `1.68.94.250`，边缘区域固定为 `sin1`。
对做 IP 风控的上游而言，出口 IP 漂移是风险因素。**处置**：记录为 Task 19 的监控项——
若出现风控升高，优先怀疑出口 IP 漂移，其次才是 Vercel 头泄漏（§3.4）。

### 3.4 relay 会把 Vercel 指纹头转发给上游 ⚠️

上游回显中可见：`Forwarded`、`X-Forwarded-Host`、`X-Vercel-Ip-*`、`X-Vercel-Edge-Region`、
`X-Vercel-Ja4-Digest`、`X-Middleware-Subrequest`、`Logs-Url`，以及 **`X-Vercel-Oidc-Token`**
（一个含项目/账号/套餐信息的 JWT，实测载荷里有 `"plan":"hobby"`）。

上游可以据此直接判断"这是经 Vercel relay 的请求"。这是 Task 16（自建硬化 relay）的存在理由：
同头契约、剥掉这些头、加目标白名单与可选 `x-relay-key`。当前不阻塞——先按现状上线，
若风控升高再切。

### 3.5 健康探针不能用 httpbin

httpbin 在本次实测中连续 5 次 `503 Service Temporarily Unavailable`（非 Vercel 格式），
另一次 drip 首字节 42s。**健康探针改用 `https://www.dola.com/robots.txt`**（本轮 20/20 通过，
body 稳定为 `User-agent: *\nAllow:/`）。9router 原实现用 httpbin，这里刻意偏离。

---

## 4. 待办：协议提交侧（决策门本身）

`ROUTE=A/B` 的判定需要真实 cookie 驱动 `dola_client.DolaClient`，本机没有 dola 账号 cookie，
因此**尚未测**。脚本已就绪：`spikes/protocol_probe.py`

```sh
pip install aiohttp
python spikes/protocol_probe.py \
  --upstream ../dola-render-gateway \
  --cookie-file cookies.txt \
  --runs 10 --egress relay
```

判定规则（脚本内置）：成功率 ≥ 8/10 且滑块类拦截 < 2 次 → `ROUTE=A`（路由器独立成站）；
否则 `ROUTE=B`（路由器保留 API/池/存储，提交经 `Executor` 转发给局域网 PC sidecar）。

**为什么必须实测**：`server.py` 只用 `browser_pool`（浏览器路径），`dola_client.DolaClient`
在整个仓库里**没有任何调用方**；而 `video_probe.py:3-4` 的注释写明浏览器路径的存在意义是
"in-page fetch with real browser fingerprint bypasses captcha checks"。所以纯协议提交是否被
风控接受，是代码阅读无法回答的问题。

`--duration 30` 可顺带验证扩展时长在协议路径是否被接受（`_build_video_body` 的
`ability_param.duration`）。

---

## 5. 方案偏差记录

| 偏差 | 原因 |
| --- | --- |
| Go 模块位于**独立仓库** `deepmean/dola2api-mips` 的根目录，而不是上游 checkout 内的 `wrt/` | 操作者指示"在 github 建仓库，云编译"；与 `deepmean/qwen2api-mipsle` 的既有做法一致 |
| `DOLA_EGRESS_FIRST_BYTE_TIMEOUT` 默认 25s → **20s** | 见 §3.2 实测 |
| 健康探针目标 httpbin → `www.dola.com/robots.txt` | 见 §3.5 实测 |
| `spikes/` 随移植仓库一起走（而非留在上游 checkout） | 证据与产物同仓，便于复核 |
