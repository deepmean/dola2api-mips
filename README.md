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

以下为**当前实现**（不是计划）。`/v1/*` 需 `DOLA_API_KEYS`，`/admin/*` 需 `DOLA_ADMIN_KEY`，
`/health` 无需鉴权（监控要用，且不含任何密钥）。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/v1/videos` | `{prompt,ratio?,model?,duration?}` → `202 {id,state,media[]}` |
| POST | `/v1/images` | 同上；`{prompt,ratio?,style?}`，产出图片而非视频 |
| POST | `/v1/chat/completions` | 文本对话（OpenAI 形状）。**不走任务队列**，自带并发与超时 |
| GET | `/v1/models` | 本服务支持的模型（`DOLA_VIDEO_MODELS` 可改） |
| GET | `/v1/videos/{id}` | 任务状态；`state ∈ pending/running/succeeded/failed` |
| GET | `/v1/videos/{id}/content` | 按 `DOLA_STORE_MODE` 交付（默认 302 到 CDN） |
| GET | `/admin` | 面板（页面壳免鉴权，密钥由浏览器本地保存） |
| GET | `/admin/status` | 出口、内存、账号（含到期倒计时）、队列深度 |
| GET | `/admin/tasks?limit=N` | 最近任务（1–500，默认 50） |
| POST | `/admin/reload` | 重新读取 `accounts.json`（等价于 SIGHUP） |
| POST | `/admin/accounts` | **先向上游验证**再保存一个 cookie |
| DELETE | `/admin/accounts/{label}` | 删除一个账号 |
| GET | `/health` | 账号池、出口状态、内存读数、热重载错误 |

**视频与图片的差别只在提示词的措辞**（视频走 `Generate video: <prompt>, <ratio>`，图片走裸提示词）与
技能类型（`DOLA_IMAGE_ABILITY`，默认 3）。同一次上游调用、同一条轮询、同一套取回。

**未匹配的路径**会返回结构化 404 并**把请求的路径写进日志**——客户端对接时的路径不匹配曾是一条
没有线索的 `404 page not found`。

鉴权**失败关闭**：未配置 key 时 `/v1/*` 返回 `503` 而不是对所有人开放；
key 用常量时间比较，错的 key 与缺失的 key 不可区分。普通 API key **不能**访问 `/admin/*`
——能重载账号的人可以把服务指向完全不同的会话。

任务列表按**单调序号**排序，不按时间戳：同一时钟刻度内创建的任务用时间戳排序不确定。

**面板（`/web`）尚未实现。** 操作面目前是 `/admin/*` 的 JSON 接口。

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
2. **产物未被 UPX 压缩**：断言二进制里没有 UPX 魔数、且 ELF 节头表完好
   （UPX 会剥掉节头表）。用内存换 flash 在这台设备上是反的——flash 还剩 44MB 不缺，
   缺的是 RAM，而 UPX 在 `exec` 时把整份镜像解压进内存。断言写在**产物**上而不是
   扫仓库里的字眼，否则连解释这条禁令的文档都会被判违规。
3. 除 `egress*.go` 外禁止出现 `http.Transport{` 与 `net.Dial(`（防止绕过出口层；
   测试文件除外，它们会连本地 httptest 监听）

## 资源预算

内存是主预算，flash 只是记录项：

| 指标 | 目标 | **真机实测** |
| --- | --- | --- |
| 稳态 RSS（空闲） | ≤ 12MB | **7.9MB**（anon 1.2MB，其余为可回收的代码页） |
| 峰值 RSS（1 个在飞任务 + 4MB 参考图） | ≤ 32MB | 提交实测 **9.6MB** |
| 二进制体积（防膨胀门禁） | ≤ 9MB | 7.88MB |
| 稳态 `MemAvailable` 下降 | ≤ 15MB | 设备 `available` 从 63.5MB → 55.5MB（含其他服务波动） |

OOM 自保：`DOLA_HEAP_SOFT_LIMIT`（默认 32MiB）在大分配前检查 `HeapAlloc`，
超限让**当前任务**以 `507` 失败，而不是让内核 OOM killer 去杀别的进程。
每个任务结束调一次 `debug.FreeOSMemory()` 把堆还给内核。

## 真机验收

在目标设备（JCG-Q20 / OpenWrt 24.10.5 / ramips-mt7621 / mipsel_24kc）上实测通过：

| 项 | 结果 |
| --- | --- |
| 二进制在设备上执行 | ✅ softfloat 构建正常启动、正常报错、正常服务 |
| procd 服务 | ✅ `enabled`，监听 `8790`，优雅停机 |
| `/health` 免鉴权 | ✅ |
| 无 key / 错误 key 访问 `/v1/*` | ✅ 401 |
| **普通 API key 访问 `/admin/*`** | ✅ **401**（设计的安全属性） |
| admin key 访问 `/admin/*` | ✅ 200 全量状态 |
| 请求校验（空 prompt） | ✅ 400 + 明确原因 |
| **真实提交全链路** | ✅ 202 受理 → 任务结算（鉴权→校验→池→**出口层 relay**→上游） |
| 账号冷却 | ✅ 提交失败后 `available:false`、`cooldown_remaining:737` |

**唯一未通的是上游签名门槛**：提交被上游以 `710022002`（服务限流）拒绝——
该错误**浏览器自己也会遇到**，详见 [`spikes/RESULT.md`](spikes/RESULT.md)。

部署与验收脚本在 [`deploy/`](deploy/)：`router-setup.sh`（就地生成密钥）、
`router-update.sh`（原子替换 + 自动回滚）、`router-footprint.sh`、`router-acceptance.sh`。

## 部署

目标布局：`/opt/dola2api/`（二进制 + `dola2api.conf` + `accounts.json`）+
`/etc/init.d/dola2api`（procd），端口 `8790`。

```sh
# 二进制从 CI 产物取
wget -O /tmp/dola2api https://github.com/deepmean/dola2api-mips/releases/download/latest/dola2api-linux-mipsle
ssh root@192.168.26.1 'mkdir -p /opt/dola2api'
scp /tmp/dola2api                root@192.168.26.1:/opt/dola2api/dola2api
scp deploy/dola2api.conf         root@192.168.26.1:/opt/dola2api/dola2api.conf
scp accounts.json                root@192.168.26.1:/opt/dola2api/accounts.json
scp deploy/dola2api.init         root@192.168.26.1:/etc/init.d/dola2api

ssh root@192.168.26.1 '
  chmod 700 /opt/dola2api/dola2api.conf        # 内含 API key
  chmod 600 /opt/dola2api/accounts.json        # 内含会话 cookie
  chmod +x /opt/dola2api/dola2api /etc/init.d/dola2api
  /etc/init.d/dola2api enable
  /etc/init.d/dola2api start
  sleep 2 && logread -e dola2api | tail -5
'
```

`accounts.json` 格式见 [`accounts.json.example`](accounts.json.example)；
`deploy/dola2api.conf` 每项都有注释说明为什么取这个值。

改完账号**不必重启**：

```sh
ssh root@192.168.26.1 '/etc/init.d/dola2api reload'      # 等价于 SIGHUP
# 或（网络侧，无需 ssh）
curl -X POST -H "Authorization: Bearer $DOLA_ADMIN_KEY" http://192.168.26.1:8790/admin/reload
```

热重载**保留 cookie 未变的账号的当日用量**——改个 label 或加一个账号，不会给已耗尽的账号发新额度。
文件写坏时**运行中的集合不变**，只记录错误（`/health` 与 `/admin/status` 里可见）。

**注意**：`.gitattributes` 强制 `deploy/` 下文件为 LF。CRLF 的 procd 脚本在设备上会以
`$'\r': not found` 失败。

## 许可

与上游一致：仅供学习与内部测试。
