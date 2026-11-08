# Aemeath — PC 性能监控台

Go 采集端（macOS / Windows）通过 Wi‑Fi WebSocket 把 CPU、内存、磁盘、网络、温度、GPU 推送到
Waveshare **ESP32‑S3‑Touch‑LCD‑4B**（480×480 ST7701 + GT911），在单页 LVGL 仪表盘上显示。

```
agent/      Go 1.27.3+ 采集端 + MyGO 0.8.3 桌面管理端（gopsutil + cgo IOKit / WMI + NVML）
firmware/   ESP-IDF v6.0.2 固件（LVGL 9、esp_lvgl_port、mDNS、esp_http_server WS）
```

## 固件

```bash
. ~/.espressif/v6.0.2/esp-idf/export.sh
cd firmware
idf.py build
idf.py -p /dev/cu.usbmodem11101 flash monitor
```

首次启动（或长按 BOOT 3 秒清除配网后）进入配网模式：屏幕显示二维码，手机加入
`Aemeath-XXXX` 热点后会弹出配网页（或打开 `http://192.168.4.1`），选择 Wi‑Fi 并保存。
联网后设备广播 `aemeath.local` 与 `_aemeath._tcp`，也可在浏览器访问 `http://aemeath.local/` 重新配网。

## 采集端

```bash
cd agent
go run ./cmd/aemeath-agent            # mDNS 自动发现
go run ./cmd/aemeath-agent -addr 192.168.31.207:80
go run ./cmd/aemeath-agent -dry-run   # 只打印 JSON，不连接
```

Windows 交叉编译（无需 cgo）：

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags -H=windowsgui -o aemeath-agent.exe ./cmd/aemeath-agent
```

- **macOS**：Apple Silicon 通过 IOHID 读取 `PMU tdie*` 温度、IOAccelerator 读取 GPU 占用，无需 sudo。
  首次运行时系统会询问“本地网络”权限，需允许，否则连接会报 `no route to host`。
- **Windows**：NVIDIA GPU 直接读 NVML；CPU 温度及 AMD/Intel GPU 需要后台运行
  [LibreHardwareMonitor](https://github.com/LibreHardwareMonitor/LibreHardwareMonitor)（建议以管理员运行），
  未运行时温度字段省略，屏幕显示 `--`。
- 命令行版本开机自启：macOS 可用 `~/Library/LaunchAgents` plist；Windows 可把 exe 快捷方式放进 `shell:startup`。桌面版本通过设置页管理系统登录项。

## MyGO 桌面端（macOS）

使用 [MyGO](https://mygo.egoist.dev/) 的纯 Go 原生 UI（Metal 渲染）与原生托盘 API，不使用 WebView，也不依赖 Bun、Vite 或前端构建。桌面版本与命令行版本是独立入口；使用桌面版本时请先退出已有命令行 Agent，避免两个进程同时向设备推送。

- **托盘运行**：关闭窗口后继续采集与推送；菜单可打开窗口、启动/暂停推送服务、切换设备显示页、退出应用。退出才停止全部后台工作。
- **服务状态**：显示发现中、连接中、已连接、重连、暂停状态，设备地址、最近成功发送时间、连接错误，以及本机 CPU / 内存 / GPU / 网络数据。暂停服务只暂停设备推送，本地指标与额度仍会更新。
- **订阅管理**：从 Magpie 读取完整账户列表，上下移动调整顺序，隐藏/恢复账户；排序和显示状态立即保存并应用，最多向设备发送前 8 个未隐藏账户。偏好按网关原始账户名称匹配，改名会被视为新账户；不会修改 Magpie 的数据。
- **连接设置**：设备地址与 Magpie 网关地址编辑后，点击“保存设置”才会保存并应用；后台快照更新不会覆盖未保存的输入。
- **快速切页**：窗口及托盘均可发送“性能 / 订阅额度”切换指令。需要更新本仓库固件；协议无回执，界面只表示已发送，屏幕手势仍可自行切页。
- **开机自启**：设置页启用系统登录项；登录启动时只驻留托盘。应用要求 macOS 13+，使用系统 Login Items。请先把打包的应用拖入 Applications，再启用此项。

开发与运行要求 **macOS 13+**。开发需要 Go **1.27.3+**（Go 可自动下载对应 toolchain）和 Xcode Command Line Tools。MyGO 原生 UI 使用纯 Go；macOS 硬件传感器使用 cgo，因此开发、测试和构建需保持 `CGO_ENABLED=1`。

```bash
./agent/scripts/dev.sh
```

开发脚本可从任意目录启动；在终端输入 `r` 后按回车重新构建并重启，输入 `q` 后按回车退出，`Ctrl+C` 也可退出并清理开发进程。修改 Go 代码后自动重新构建；编译失败时保留上一版运行，修正后会再次尝试。开发运行前请退出正在推送的正式版，避免重复推送。额外参数会传给 `mygo dev`，例如 `./agent/scripts/dev.sh -lang zh-CN`。

在 `agent` 目录运行 Go 测试，覆盖原生界面逻辑与后台服务：

```bash
CGO_ENABLED=1 go test ./...
```

打包脚本会生成应用图标，并通过 MyGO 构建原生桌面应用，生成 ad-hoc 签名的 `.app`，并打包为 `.dmg` 安装镜像；全程无需 Bun 或前端资源构建：

```bash
cd agent
./scripts/build-dmg.sh                  # 默认 Universal：Apple Silicon + Intel
./scripts/build-dmg.sh darwin/arm64     # 仅 Apple Silicon
./scripts/build-dmg.sh darwin/amd64     # 仅 Intel
```

产物位于 `agent/build/darwin-universal/`（或 `darwin-arm64/`、`darwin-amd64/`）。默认 ad-hoc 签名；对外分发时可通过 `AEMEATH_SIGN_IDENTITY="Developer ID Application: …" ./scripts/build-dmg.sh` 指定签名，公证选项参见 [MyGO 分发文档](https://mygo.egoist.dev/docs/distribution)。本仓库不包含 Developer ID 或公证凭据。

配置原子写入 `~/Library/Application Support/Aemeath/settings.json`，权限 `0600`；登录项状态读取自系统。日志位于 `~/Library/Logs/Aemeath/agent.log`。设备地址留空使用 mDNS，或输入 IP/主机名（端口默认 80）；首次连接需允许本地网络权限。

## Magpie 订阅额度页

屏幕左右滑动切换到第二页「AI QUOTA」，显示本机 [Magpie](http://127.0.0.1:3425) 网关聚合的各订阅账户额度：
每个窗口一条进度条（≥75% 黄、≥90% 红）、用量 / 计数、重置倒计时，顶部是今日与 30 天花费和调用次数。
账户按 Agent 发送顺序显示；桌面端可调整顺序和隐藏账户。余额账户（如 jinpai）单独显示名称和带币种的余额（如 `$109.12`）；余额未知时显示 `--`。
余额不参与 USED / LEFT 百分比切换，也不显示进度条或重置倒计时。

- agent 每 30 秒读取 `GET /v1/magpie/quotas` 与 `/v1/magpie/usage` 并转发（不会转发账户邮箱）。
- `-magpie http://host:port` 指定网关地址，`-magpie ""` 关闭；网关不可用时只记日志，不影响性能数据。
- 超过 3 分钟未收到额度数据，页面右上角显示红色 `stale`。

## 协议

每秒一帧 JSON 文本（字段见 `agent/internal/proto/proto.go`，固件解析见 `firmware/main/metrics.c`），
缺失字段直接省略。设备用 `ts` + `tz` 同步时钟。
额度数据是另一种帧（`"type":"quota"`，见 `proto.QuotaFrame` 与 `firmware/main/quota.c`）；没有 `type` 的帧按性能数据处理。
