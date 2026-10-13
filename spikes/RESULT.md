# Task 0 — 出口与协议可行性实测

探针脚本：`spikes/egress_probe.py`（纯标准库，无需 aiohttp/requests）
原始数据：`spikes/egress_result.json`、`spikes/egress_result_proxy.json`

**状态：出口部分已完成；cookie 实测已完成（§6）；协议提交侧见 §7。**

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

---

## 6. cookie 实测（`spikes/verify_cookie.py`，真账号）

探针打的就是路由器将来 `verify` 要打的那一个请求（`POST /im/chain/recent_conv`，cmd 3200）。

| 出口 | 结果 |
| --- | --- |
| **relay** | **`200` + `downlink_body.pull_recent_conv_chain_downlink_body` 存在**（1 个会话 cell）→ **cookie 有效，relay 路径可用** |
| direct | **`302` → `https://www.dola.com/chat/`**（同样的 cookie、同样的请求） |

### 6.1 直连出口不可用（重要）

同一个 cookie，经 relay 返回 200，直连被 302 弹回 `/chat/`。两者唯一差别是**来源 IP**。
上游按来源 IP 做地理/风控门禁，**直连出口拿不到数据**。

这把 `DOLA_STRICT_PROXY=1` 从"防御性设计"变成"实测必需"：一旦出口失败回落直连，
得到的是跳转页面而不是响应——而且很可能被上层误判成 cookie 失效。所以回落必须被禁止，
并把 `REDIRECT`（3xx）单独归类，别和"会话过期"混在一起。

### 6.2 会话寿命 60 天

`sid_guard` 的结构是 URL 编码的竖线分隔：`<sessionid>|<issued_epoch>|<ttl>|<GMT 到期时间>`。
实测 `ttl=5184000`（60 天），我按 `issued+ttl` 算出的到期时间与 cookie 自述的
`Sat, 12-Dec-2026 00:17:41 GMT` **完全一致**（脚本内置了这个交叉校验）。

**结论**：离线供给 cookie 的频率是 **60 天一次、每次约 2 分钟**。这是"全程路由器独立完成"
的可行性依据——运行时全自动，供给是低频人工动作。

### 6.3 探针自身的两个 bug（记录以免重犯）

第一次跑出来是 `0/1 usable`，**结论是错的**，错在探针不在 cookie：

1. **把 body 截断到 800 字符后再 `json.loads`** → 合法 JSON 必然解析失败，被判成"不是 JSON"。
   修法：完整 body 用于解析，另存一份截断副本仅用于显示。
2. **`sid_guard` 用"扫描长数字串"解算** → 被 sessionid 哈希里的 8 位数字串污染，
   算出 `ttl=23511880s / +272 天` 的错值。修法：按位置解析（`|` 分隔），并用 cookie
   自述的 GMT 日期交叉校验。

另外 `direct` 路径把裸域名喂给 `urlsplit` 导致 `hostname=None` 崩溃，也已修。

---

## 7. 协议提交侧（决策门）——**根因：`a_bogus` 签名**

### 7.1 结论先行

页面加载的 `bdms-sdk`（ByteDance 风控 SDK）会给每个 `/chat/completion` 请求**追加一个 `a_bogus` 签名参数**。
我们的客户端不带它，所以被风控拦下。

**上游仓库的协议路径从来没实现过这个签名**：`dola_client` 里只有 `_fake_a_bogus()`（返回随机串），
而 `server.py` 从不调用 `DolaClient`。也就是说 `DolaClient` 这条路径**从未可用**——不是我们移植得不对。

### 7.2 证据链

**① 浏览器发出的真实 URL**（从页面 `performance.getEntriesByType('resource')` 抓到）：

```
https://www.dola.com/chat/completion?aid=495671&...&msToken=PWf46-...&fp=verify_mv5xmc35_...
   &a_bogus=x7s5hqy7DoQnCdFt8KaI99PRDhQlNBWyXPTdWNlRHquDLXtT1wBJtGrQjPiHs8D6VRsiNHI7AnS6bxxOZWRX5ZnkFmpfSBJW6U9nVzsL%2Fqq3P0ksDrgheh8zFwBCWKTPuCp4xIf6lUiHhxZ-DqriUc57H%2Fpr4O4BFHxRksyiCV4ZgPvG3VdYSWy%3D
```

同一次加载还出现了：`bdms-sdk.920ccda932.js`、`mssdk.bytedance.com/web/common?ms_appid=495671`、
`mssdk.bytedance.com/web/r/token`、`verifycenter/1.0.0.427/index.js`（滑块验证 UI）。
shell bundle 里也写着 mssdk 的 `enablePathList` **包含 `/chat/completion`**。

**② 在真浏览器里用同样的 body/头/参数提交 → 成功**：`SSE_ACK`，`conversation_id` 拿到，
上游回执"消耗 2 個影片生成額度"。我页内 recorder 记到的 `init.headers` 只有
`{"Accept","Content-Type","agw-js-conv"}` —— 因为 recorder 包在 SDK 外层，**签名是 SDK 在更内层加的**，
所以头看起来"完全一样"。

**③ 排除法（全部实测）**：

| 假设 | 实测 | 结论 |
| --- | --- | --- |
| 请求头/body/参数不对 | 与浏览器 `SUBMIT_JS` 逐字段一致；页内 fetch 用同样的东西成功 | ❌ |
| TLS/HTTP2 指纹 | `curl_cffi` `impersonate="chrome"`（真 Chrome TLS+HTTP2）| ❌ 仍 `710022004` |
| cookie 快照过期 | 浏览器里 `msToken`/`s_v_web_id`/`csrf` 与快照**完全一致** | ❌ |
| 会话需要预热 | 浏览器刚成功生成后**立刻**用浏览器外客户端重试 | ❌ 仍 `710022004` |
| 出口 IP | 浏览器与 Python 客户端**同一台机器同一出口** | ❌ |
| 视频技能/`bot_id` 特有 | 图片生成（`ability_type=16`）报**一模一样**的错 | ❌ |
| relay 改编码/丢头 | 经 relay POST，上游看到 `Content-Length: 16` 正确保留；头逐项核对 | ❌ |

**④ 因果验证**：把抓到的带 `a_bogus` 的 URL 原样在浏览器外重放 →
错误**从 `710022004`（滑块）变成 `710022002`（服务使用量过大）**。签名被接受了
（否则会再次索要滑块），只是这条签名已过期/被重用。**门槛即签名。**

**⑤ 三条出口的失败层次**（早期实测）：

| 出口 | HTTP | 上游返回 | 卡在哪 |
| --- | --- | --- | --- |
| direct（大陆 IP） | `302` → `/chat/` | HTML 跳转 | 地理拒绝（操作者确认 dola 拒大陆 IP） |
| proxy（JP 机房 IP） | `200` | `710022004` slide / `shark_admin` | 风控（无签名） |
| relay（Vercel `sin1`） | `200` | `718013001` 预分配会话位置信息失败 | 过了风控层，卡在会话创建 |

### 7.3 对"路由器独立完成"的影响

要让路由器自己提交，就必须**自己算出每个请求的 `a_bogus`**。这要求：

- 移植 `bdms-sdk` 的签名算法（VM 混淆 JS，约 1MB，字节跳动会轮换）；
- 签名与请求参数 + 浏览器派生指纹相关；
- 这是业界已知的逆向目标（TikTok/豆包的 `a_bogus` 有公开实现），但它是**移动靶**，
  属于持续维护的逆向项目，不是"移植"。

### 7.4 错误信息的解码插曲（记录以免重犯）

第一轮把 `error_msg` 读成 `Ԥ����Ựλ����Ϣʧ��`，看起来像编码损坏。实际那 33 个字节
（`e9a284…e8b4a5`）是**合法 UTF-8**，正确解码为「预分配会话位置信息失败」——
**乱码来自 PowerShell 控制台的 GBK 码页**，不是数据。教训：怀疑编码前先看十六进制。
`spikes/decode_msg.py` 就是为此留下的工具。

---

### 7.3 方案 A 的进展与当前阻塞

**已解出并验证：`X-Bogus`（16 字符）**

页面把签名器挂成全局 `window.bdms`（两个导出：`init`、`frontierSign`）。用
`frontierSign({X-MS-STUB: <查询串>})` 当 oracle，把输出解回内部 9 字节头，逐字段钉死了算法：

```
u[0]      = 63 & (++bogusIndex)          每页递增计数器
u[1],u[2] = envcode（大端）               自动化检测标志位
u[3]      = 14                            ubcode 常量
u[4],u[5] = md5(md5(""))[14:16]           = 69,63 常量
u[6],u[7] = md5(hexDecode(stub))[14:16]   唯一随请求变化的部分
u[8]      = 随机
payload   = u + xor(u)  →  RC4(密钥=单个随机字节)  →  自定义字母表 base64
```

`M` = 纯 MD5（无盐）、`r.decode` = 十六进制解码（JS 语义：非十六进制字符当 0、奇数尾字符丢弃）
——这两个是最初判错的点，修正后 4/4 样本全部命中。

**已用 Go 纯标准库实现**（`bdms.go` + `bdms_test.go`，8 个测试，含 oracle 采集的 4 组向量）。
`envcode` 对诚实的 Go 客户端恒为 1——与干净浏览器相同，比 headless 浏览器更"干净"。

**阻塞：`a_bogus`（约 180 字符）是另一个签名，而且必需**

实测：带有效 `X-Bogus` 头、不带 `a_bogus` 提交 → **仍然 `710022004` 风控**。
`frontierSign` 不产出它；SDK 里 29KB 的大 blob 是二进制（非 WASM），生产者在懒加载代码里未定位。

**顺带发现的独立 bug**：当前 Web 客户端发 `pc_version=3.39.1` + `device_id=7686671608228726325`，
而上游 `dola_client` 发 `pc_version=3.32.62` 且**不带 `device_id`**——版本漂移。

---

## 8. 计划修正汇总

| 修正 | 依据 |
| --- | --- |
| **锁定 Route A**：不做 sidecar，运行时全链路路由器独立完成 | 操作者要求；cookie 寿命 60 天使离线供给频率可接受 |
| Task 15（sidecar 执行器）移出实施范围 | 同上（保留 `Executor` 接口，未来若要兜底仍是加一个实现） |
| Task 0 验收：`--runs 2`（受 `DAILY_LIMIT=2` 约束），不是 10 | §7 |
| **Task 6 新增 `IsDailyLimit`** | `dola_client._poll_video` 只查 `CREDIT_FAIL_PATTERN`，**不查每日上限**；协议路径会把"今天已用完"误判成超时，池就不会换号而是干等。Go 版必须补上（模式取自 `video_worker_ui.DAILY_LIMIT_PATTERN`） |
| `DOLA_STRICT_PROXY=1` 从防御性默认升级为**实测必需** | §6.1 |
| 新增 `REDIRECT` 错误分类（3xx 不等于会话过期） | §6.1 |
| `DOLA_EGRESS_FIRST_BYTE_TIMEOUT` 25s → 20s | §3.2 |
| 健康探针 httpbin → `www.dola.com/robots.txt` | §3.5 |
| `.gitignore` 补 `cookies.txt` 等凭据模式 | 原忽略清单漏了它，是真实泄漏风险 |
| **Task 0 决策门未通过：Route A 尚未证实** | §7.3：纯协议路径在唯一可用出口（relay）上无法创建会话，任何能力都一样 |
| 决策门的下一个依赖：**一份能跑通的浏览器提交的真实请求** | §7.3：请求形状/参数/头部/编码/token 都已排除，剩下的是浏览器上下文 |
