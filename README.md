# dola2api (MIPS32 port)

Dola Render Gateway 的 MIPS32 移植版：把 `coll3879xx-cyber/dola-render-gateway`
（Python 3.11 + FastAPI + Playwright/Chromium）改写成 **Go 纯标准库** 的单文件静态二进制，
跑在 MIPS32 小端 OpenWrt 路由器上（MediaTek MT7621 / `ramips`，实测机型 JCG-Q20）。

上游用 Playwright 驱动真实浏览器做提交、滑块验证码求解与 30s 扩展拦截——这些在
250MB RAM、无 FPU 的 MT7621 上不可能运行。本项目只保留**协议路径**：

| 保留 | 依据 |
| --- | --- |
| `POST /chat/completion`（SSE 提交） | `dola_client.py:716-863` 是纯 aiohttp 实现，不含浏览器 |
| `POST /im/chain/single`（轮询） | 同上 |
| 参考图上传（`prepare_upload` → `ApplyImageUpload` AWS4 → TOS PUT） | `dola_client.py:72-106, 386-462` 纯 `hmac`/`hashlib` |
| 未水印 URL 提取（`video_model.video_list[].main_url` base64） | `video_worker.py:91-114` 只是解码 + 比码率 |

丢弃：Playwright/Chromium、`browser_pool.py` 的 profile 管理、`gap.py` 的 OpenCV 滑块求解、
`extensions/dola30` 扩展、`add_account.py` 浏览器登录（改为 cookie 注入）。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/v1/videos/generations` | `{model,prompt,size?,ratio?,duration?,reference_images?}` → `{id,status:"queued",...}` |
| GET | `/v1/videos/{id}` | `{id,status,video_url,error}`，`status ∈ queued/processing/completed/failed` |
| GET | `/v1/models` | `seedance-2.0`、`seedance-2.5` |
| GET | `/health` | 账号池、出口状态、内存读数、热重载错误 |
| GET | `/web` | 管理面板 |
| GET | `/videos/{file}` | `redirect`（默认，302 到 CDN）/ `pipe` / `mirror` |

## 出口代理层

所有上游请求经统一的 egress 层出网，失败时**默认不回落直连**（`DOLA_STRICT_PROXY=1`），
避免路由器真实 IP 泄漏给上游。支持三种路径：

- `vercel`：Vercel relay，基址重写 + `x-relay-target` / `x-relay-path` 头（与 9router 同契约）
- `http`：标准 HTTP CONNECT 代理
- `direct`：直连（仅显式配置或命中 `noProxy` 时）

## 构建

**二进制由 GitHub Actions 云编译产出，本地无需安装 Go。**

```sh
wget -O /tmp/dola2api.new \
  https://github.com/deepmean/dola2api-mips/releases/download/latest/dola2api-linux-mipsle
chmod +x /tmp/dola2api.new
```

固定构建参数：`GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0`，
`-trimpath -ldflags "-s -w"`。`GOMIPS=softfloat` 是**必须的**：MT7621 没有可用 FPU，
硬浮点二进制能启动、首次用浮点即崩。CI 在上传前用 `readelf` 断言
`ELF32` / 小端 / `Machine: MIPS` / `FP ABI: Soft float` / 无 `PT_INTERP`。

CI 还带三条门禁：

1. `go.mod` 无 `require`（保持纯标准库、可复现构建）
2. **禁止 UPX**（UPX 在 `exec` 时把整份镜像解压进内存：这台设备 flash 还剩 44MB 不缺，
   缺的是 RAM；用内存换 flash 是反的）
3. 除 `egress*.go` 外禁止出现 `http.Transport{` 与 `net.Dial(`（防止绕过出口层）

## 资源预算

内存是主预算，flash 只是记录项：

| 指标 | 目标 |
| --- | --- |
| 稳态 RSS（空闲） | ≤ 12MB |
| 峰值 RSS（1 个在飞任务 + 4MB 参考图） | ≤ 32MB |
| 二进制体积（防膨胀门禁） | ≤ 7.5MB |
| 稳态 `MemAvailable` 下降 | ≤ 15MB |

OOM 自保：`DOLA_HEAP_SOFT_LIMIT`（默认 32MiB）在大分配前检查 `HeapAlloc`，
超限让**当前任务**以 `507` 失败，而不是让内核 OOM killer 去杀别的进程。
每个任务结束调一次 `debug.FreeOSMemory()` 把堆还给内核。

## 部署

见 [`DEPLOY-OPENWRT.md`](DEPLOY-OPENWRT.md)。目标：`/opt/dola2api/` +
`/etc/init.d/dola2api`（procd），`/etc/sysupgrade.conf` 持久化，端口 `8790`。

## 许可

与上游一致：仅供学习与内部测试。
