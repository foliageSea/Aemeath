# Aemeath — 性能监控台与爱弥斯语音 Agent

Go 采集端（macOS / Windows）通过 Wi‑Fi WebSocket 把 CPU、内存、磁盘、网络、温度、GPU 推送到
Waveshare **ESP32‑S3‑Touch‑LCD‑4B**（480×480 ST7701 + GT911），在 LVGL 多页仪表盘上显示。

**爱弥斯语音助手**使用监控台麦克风和扬声器，由电脑离线检测唤醒词（默认“你好小爱”，支持自定义），通过 MiMo ASR → CloudWeGo Eino / OpenAI 兼容模型 → MiMo 音色克隆或 Fish 双向流式合成完成对话。也可使用全局 F8 按住说话。设备上的爱弥斯 Q 版动画随聆听、思考、播报状态切换，桌面端显示对话与工具执行过程。

→ [语音助手安装、配置与运行](docs/voice-agent.md) · [协议约定](docs/voice-contract.md) · [验证记录与限制](docs/voice-validation.md)

```
agent/      Go 1.27.3+ 采集端 + MyGO 0.8.3 桌面管理端（gopsutil + cgo IOKit / WMI + NVML）
firmware/   ESP-IDF v6.0.2 固件（LVGL 9、esp_lvgl_port、mDNS、esp_http_server WS）
docs/       语音安装配置、通信协议与验证记录
```

## 开始使用

1. 构建并烧录固件，按屏幕提示完成 Wi-Fi 配网，让设备与电脑处于同一局域网。
2. 运行 MyGO 桌面端或命令行采集端，通过 mDNS 自动发现设备，也可手动填写设备地址。
3. 使用语音助手时，更新支持音频通道的固件，在桌面“设置 → 语音”启用助手、配置 LLM 和 MiMo ASR，再选择 MiMo 或 Fish 语音合成。
4. 在“离线唤醒”安装模型并保存设置，随后测试唤醒；也可直接使用 F8 按住说话。完整步骤见[语音助手使用说明](docs/voice-agent.md)。

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

## MyGO 桌面端（macOS / Windows）

使用 [MyGO](https://mygo.egoist.dev/) 的纯 Go 原生 UI（macOS 使用 Metal 渲染）与原生托盘 API，不使用 WebView，也不依赖 Bun、Vite 或前端构建。桌面版本与命令行版本是独立入口；使用桌面版本时请先退出已有命令行 Agent，避免两个进程同时向设备推送。

- **托盘运行**：关闭窗口后继续采集与推送；菜单可打开窗口、启动/暂停推送服务、切换设备显示页、退出应用。退出才停止全部后台工作。
- **服务状态**：显示发现中、连接中、已连接、重连、暂停状态，设备地址、最近成功发送时间、连接错误，以及本机 CPU / 内存 / GPU / 网络数据。暂停服务只暂停设备推送，本地指标与额度仍会更新。
- **订阅管理**：从 Magpie 读取完整账户列表，拖动调整顺序，隐藏/恢复账户；排序和显示状态立即保存并应用，最多向设备发送前 8 个未隐藏账户。偏好按网关原始账户名称匹配，改名会被视为新账户；不会修改 Magpie 的数据。
- **连接设置**：设备地址与 Magpie 网关地址编辑后，点击“保存设置”才会保存并应用；后台快照更新不会覆盖未保存的输入。
- **快速切页**：窗口及托盘均可发送“性能 / 订阅额度”切换指令。需要更新本仓库固件；协议无回执，界面只表示已发送，屏幕手势仍可自行切页。
- **屏幕电源与亮度**：窗口标题栏、设置页与托盘菜单均可远程息屏/亮屏，设置页还可拖动调整亮度（0-100）；屏幕状态以设备为准：设备把息屏/亮度保存到 NVS（调整停止 1 秒后写入），重启后恢复，并在每次连接及每次变化后上报；软件连接时不会覆盖设备状态，设备离线或尚未上报时控件禁用。息屏会同时关闭 ST7701 显示输出（`DISPOFF`），因为板载 AP3032 的使能脚常开，仅靠背光 PWM 无法完全熄灭。
- **开机自启**：设置页启用系统登录项；登录启动时只驻留托盘。应用要求 macOS 13+，使用系统 Login Items。请先把打包的应用拖入 Applications，再启用此项。

macOS 开发与运行要求 **macOS 13+**。开发需要 Go **1.27.3+**（Go 可自动下载对应 toolchain）和 Xcode Command Line Tools。MyGO 原生 UI 使用纯 Go；macOS 硬件传感器使用 cgo，因此开发、测试和构建需保持 `CGO_ENABLED=1`。

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

### Windows 安装包（macOS / Linux 交叉编译）

需要 Go **1.27.3+**、Bash 和 **NSIS 3**（`makensis` 在 PATH 中）。macOS 安装依赖：`brew install nsis`；Debian / Ubuntu：`sudo apt-get install nsis`。无需 Windows SDK、MinGW 或 cgo。

从仓库根目录运行（脚本也支持从任意目录调用）：

```bash
./agent/scripts/build-windows.sh                  # 默认 Windows x64
./agent/scripts/build-windows.sh windows/arm64    # Windows ARM64
./agent/scripts/build-windows.sh windows/amd64 -o build/release
```

脚本生成图标，强制 `CGO_ENABLED=0`，通过当前模块锁定版本的 MyGO 构建桌面应用和 NSIS 安装程序。额外参数传给 `mygo build`，例如 `-o` 修改输出目录（相对于 `agent`）。默认产物：

- `agent/build/windows-amd64/Aemeath.exe`：桌面应用。直接运行时需保留同目录的资源文件。
- `agent/build/windows-amd64/aemeath-setup-0.1.0-amd64.exe`：可分发的安装包，版本来自 `agent/mygo.json`。
- ARM64 产物位于 `agent/build/windows-arm64/`，安装包名称中的架构为 `arm64`。

安装程序默认安装到当前用户的 `%LOCALAPPDATA%\Programs\Aemeath`，创建开始菜单入口，提供桌面快捷方式选项，并可在系统“已安装的应用”中卸载，无需管理员权限。Windows 开机自启通过桌面设置页管理；温度与 GPU 采集的依赖见上文 Windows 说明。默认安装包未进行代码签名；签名可配置 `agent/mygo.json` 的 `windows.signCommand`，参见 [MyGO 分发文档](https://mygo.egoist.dev/docs/distribution)。

### Windows 语音版

上面的默认 Windows 安装包使用 `CGO_ENABLED=0`，不提供离线唤醒能力。需要离线唤醒时，使用语音版脚本，额外准备目标架构的 MinGW-w64 / llvm-mingw C 编译器；Go、Bash 和 NSIS 要求相同：

```bash
# 从仓库根目录构建 Windows x64 语音版
CC=x86_64-w64-mingw32-gcc bash agent/scripts/build-windows-voice.sh windows/amd64
```

脚本启用 cgo 并打包 sherpa-onnx / ONNX Runtime DLL；运行时需保留配套资源。macOS 的 `build-dmg.sh` 已包含对应架构的语音原生库。唤醒模型仍需在应用内安装，具体要求与发布包校验方法见[构建与诊断](docs/voice-agent.md#8-构建与诊断)。

## 爱弥斯语音助手

电脑主动连接设备的独立 `/voice` WebSocket，采集与播放均使用 **ESP32 设备的麦克风和扬声器**；性能与额度仍通过 `/ws` 独立推送。语音配置统一在“设置 → 语音”保存。

- **离线唤醒**：默认“你好小爱”，支持 2–12 个模型支持的中文汉字；模型可在桌面端下载安装。待机音频不发送给云端 ASR，唤醒后才识别问题；对话期间再次说出已保存的唤醒词会取消当前对话并回到待命。
- **全局按住说话**：按住 **F8** 录音，松开后识别并发送给 Agent，无需先说唤醒词，也不要求离线模型就绪；需要语音助手已启用、语音通道已连接且设备麦克风未静音。切到其他应用或关闭窗口驻留托盘仍可使用，单次最长 30 秒；按下快捷键会打断当前对话或播报。F8 被占用时自动尝试 **Ctrl + Shift + F8**，实际快捷键或注册错误显示在语音助手页；部分 Mac 键盘需使用 **Fn + F8**。
- **对话与历史**：桌面端显示识别文字、回复、阶段耗时及可展开的工具执行记录；最近完整文字对话会持久化并在重启后恢复，可清空上下文。语音合成失败后可仅重试回复语音，避免重复运行工具。
- **语音合成**：MiMo 使用 `mimo-v2.5-tts-voiceclone` 分句合成；Fish 支持双向流式音频，可配置 `reference_id` 或参考 WAV 与对应转写文字。语音问题识别均使用 MiMo ASR；Fish 流式播放需要支持 `stream_playback` 的固件。
- **Agent 工具**：支持查询性能与订阅额度、切页、屏幕电源/亮度、锁定电脑、Tavily 网页搜索和当前时间。需启用“Agent 工具”并使用支持 tool calling 的模型；网页搜索另需 Tavily API Key。
- **智能家居**：可接入 Home Assistant 官方 MCP Server，通过语音调用 HA 暴露的工具。在“智能家居”配置 MCP 地址、长期访问令牌并测试连接，再保存设置；同时需要开启 Agent 工具。

首次使用建议按“参考音频播放 → 合成试听 → 唤醒或 F8 → 完整对话”的顺序检查。安装资源、API 配置、命令行参数与排障见[语音助手使用说明](docs/voice-agent.md)，硬件与云端实测范围见[验证记录](docs/voice-validation.md)。

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
切页指令是 `"type":"display"` 帧（`{"v":1,"type":"display","page":"metrics"|"quota"|"voice"}`）。
屏幕电源/亮度指令是 `"type":"screen"` 帧（`{"v":1,"type":"screen","on":true|false,"brightness":0-100}`，`on` 为 `false` 时关闭显示并熄灭背光，`brightness` 仍被记住用于下次亮屏）。
设备会回传 `{"v":1,"type":"screen_state","on":…,"brightness":…}`（连接时及每次变化后），见 `firmware/main/net/server.c` 与 `firmware/main/display.c`。
