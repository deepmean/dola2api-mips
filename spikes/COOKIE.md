# 怎么给我 dola cookie（获取方法 + 格式 + 自检）

## 我需要什么

**一条登录态下的完整 `Cookie` 请求头**（不是单个字段），每个账号一行。最少必须含 `sessionid`。

| 字段 | 要求 | 用途 |
| --- | --- | --- |
| `sessionid` | **必需** | 账号登录态；上游 `dola_client.parse_cookie().is_valid` 只认它 |
| `msToken` | 强烈建议 | 作为 query 参数上送的反爬 token；缺失会抬高被风控概率 |
| `s_v_web_id` | 强烈建议 | 指纹，写进请求体 `ext.fp` |
| `sid_guard` / `passport_auth_status` | 可选但有用 | 让路由器能**提前算出会话过期时间**并在 `/health` 里告警 |

## 获取方法 A（推荐，唯一不会漏字段的方法）

`sessionid` 很可能是 `HttpOnly`，**`document.cookie` 读不到它**，所以从请求头里复制：

1. 浏览器登录 `https://www.dola.com`，随便发一条消息（让页面产生真实请求）
2. `F12` → **Network** 面板
3. 在过滤框里输入 `im/chain` 或 `completion`，点开任意一条请求
4. 右侧 **Headers** → 拉到底 **Request Headers** → 找到 `Cookie:` 这一行
5. 右键该行 → **Copy value**（或手动全选复制整行的值，不要复制 `Cookie:` 这四个字）
6. 粘到 `cookies.txt`，**一行一个账号**

## 获取方法 B（控制台，方便但要检查）

在已登录的 `www.dola.com` 页面上按 `F12` → **Console**，粘贴：

```js
copy(['sessionid','msToken','s_v_web_id','sid_guard','passport_auth_status','sid_tt']
  .map(n => { const m = document.cookie.match(new RegExp('(?:^|;\\s*)' + n + '=([^;]*)')); return m ? n + '=' + m[1] : null; })
  .filter(Boolean).join('; '))
```

它会把结果直接放进剪贴板。**但如果剪贴板里没有 `sessionid=`，说明它是 HttpOnly**——
那就必须用方法 A。不要为了凑字段自己编 `msToken`，宁可缺也不要假值。

## 文件格式

`C:\llmProj\dola-render-gateway\dola2api-mips\cookies.txt`

```
# 一行一个账号；# 开头是注释
sessionid=xxxx; msToken=yyyy; s_v_web_id=ver_zzzz; sid_guard=xxxx; passport_auth_status=xxxx
```

多账号就多行。这个文件名已在 `.gitignore` 里（我刚补上的——原来的忽略清单漏了它，
这是个真实风险，已修）。

## 先自检，再给我

不用等我，你自己就能验（纯标准库，不需要装任何依赖）：

```sh
cd C:\llmProj\dola-render-gateway\dola2api-mips
python spikes/verify_cookie.py --cookie-file cookies.txt --egress relay
```

它打的就是**路由器将来 `verify` 要打的那一个请求**（`POST /im/chain/recent_conv`，cmd 3200），
所以这里通过 = 路由器能用。输出示例：

```
[OK          ] cookie #1  sessionid=abc12345...
               HTTP 200, downlink_body present (3 recent conversation(s))
               sid_guard: issued=2026-10-01 03:20Z ttl=2592000s expires=2026-10-31 03:20Z (+18.4 days)
```

判定含义：

| verdict | 含义 | 处理 |
| --- | --- | --- |
| `OK` | 会话有效 | 可以用 |
| `EXPIRED` | 401/403，或返回 HTML 登录页 | 重新登录后按方法 A 重取 |
| `RATE_LIMITED` | 429，出口 IP 被限流 | 换出口或等一会；不是 cookie 的问题 |
| `REJECTED` | 200 但上游 `code != 0` | 把输出给我，可能需要调整请求形状 |
| `INVALID` | 缺 `sessionid` | 用方法 A 重取 |

> `sid_guard` 的过期解算是**启发式**的（这些 cookie 的格式没有公开文档）。第一次跑真实 cookie 时
> 请把输出发我，我按实际格式把解算逻辑钉死，然后写进 Go 的账号加载里。

## 关于"全程路由器独立完成"

你的要求我按 **Route A** 锁定：不做 PC sidecar，运行时全链路（提交 → 轮询 → 取回 → 账号池 →
面板 → 出口代理）都由路由器自己完成。

但有一条边界必须说清：**上游的登录是 Google OAuth**（`add_account.py`：邮箱 → 密码 → TOTP 2FA →
同意页 → 回调）。Google 对非浏览器 OAuth 有主动反自动化，这一步在路由器上做不到——不是我们没写，
是协议层面就被 Google 拦。所以：

- **cookie 供给是一次性离线步骤**（约 2 分钟），之后路由器全自动；
- 路由器会解析 `sid_guard` 算出过期时间，在 `/health` 和面板里**提前告警**，不会等到任务失败才发现；
- cookie 真过期了，就是再花 2 分钟重取一次——这个频率由 session 寿命决定（`sid_guard` 的 ttl
  实测出来才知道，常见是 30 天量级）。

如果 session 寿命短到无法接受，我们再谈第三方案（比如给登录单独做一个一次性中转），但那是另一个
决策，不塞进当前计划。

## 安全提醒

这条 cookie 等于**该账号的登录凭证**：

- 不要贴在公开场合（群、issue、pastebin）
- 我会把它放在 `cookies.txt`（已 gitignore），不会写进任何提交、日志或 `/health` 响应
- 路由器上它存在 `/opt/dola2api/accounts.json`，权限 `600`
