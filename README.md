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

## 版本与自动发版

当前项目版本为 **0.0.3**。根目录 `VERSION` 是统一版本来源，Agent 安装包元数据同步到 `agent/mygo.json`，固件的 ESP-IDF `PROJECT_VER` 直接读取它。

升级版本后提交改动，再为该提交创建并发布 GitHub Release（Tag 使用 `v0.0.3` 或 `0.0.3`，与项目版本一致）：

```bash
python3 scripts/version.py 0.0.3          # 下次发版替换为新的 X.Y.Z
python3 scripts/version.py --check --tag v0.0.3
```

发布 Release 自动触发 `.github/workflows/release.yml`，并行构建 Windows x64、macOS Apple Silicon 与 ESP32-S3 固件，全部成功后上传以下附件及带版本号的 SHA-256 校验文件：

- `Aemeath-Agent-0.0.3-windows-x64-setup.exe`：NSIS 安装包，包含语音原生运行库。
- `Aemeath-Agent-0.0.3-macos-arm64.dmg`：macOS 13+ Apple Silicon 安装镜像，包含语音原生运行库。
- `Aemeath-Firmware-0.0.3-esp32s3.bin`：从地址 `0x0` 烧录的合并镜像，会覆盖镜像间隙中的 NVS / Wi-Fi 配置；保留设置时使用 ZIP 内的分区烧录命令。
- `Aemeath-Firmware-0.0.3-esp32s3.zip`：分区镜像、烧录参数与说明。

推送 `ci/release-build/**` 测试分支会执行相同的全平台构建，并将安装包、固件和校验文件保存在 Actions Artifacts，不上传到 Release。只推送 Tag 或保存 Release 草稿不会触发构建。可在 Actions 手动运行此工作流，填写已有 Release 的 Tag 重建附件；同名附件会被替换。工作流校验 Tag 与项目版本，使用仓库自带的 `GITHUB_TOKEN` 上传，无需额外发布密钥。macOS 默认 ad-hoc 签名、未公证，Windows 默认未签名；离线唤醒模型仍在应用内安装。

本地构建使用同一入口，产物统一放入根目录 `dist/`；Windows 需要 NSIS 和 x64 MinGW-w64 C/C++ 编译器，macOS 需要 Xcode Command Line Tools，固件需要先激活 ESP-IDF v6.0.2：

```bash
bash scripts/build-release.sh windows-x64
bash scripts/build-release.sh macos-arm64
. ~/.espressif/v6.0.2/esp-idf/export.sh
bash scripts/build-release.sh firmware
```

## 开始使用

1. 构建并烧录固件，按屏幕提示完成 Wi-Fi 配网，让设备与电脑处于同一局域网。
2. 运行 MyGO 桌面端或命令行采集端，通过 mDNS 自动发现设备，也可手动填写设备地址。
3. 使用语音助手时，更新支持音频通道的固件，在桌面“设置 → 助手”启用助手、配置 LLM，再在“设置 → 语音”配置 MiMo ASR 和 MiMo 或 Fish 语音合成。
4. 在“离线唤醒”安装模型并保存设置，随后测试唤醒；也可直接使用 F8 按住说话。完整步骤见[语音助手使用说明](docs/voice-agent.md)。

语音助手按 Token 预算管理上下文，默认开启“设置 → 助手 → 对话服务 → 自动读取模型上下文容量”（JSON `contextWindowMode: "auto"`；省略也为自动模式）。自动模式仅使用已启用的 **models.dev 公共目录 → 备用容量**，不向对话服务查询模型容量，不调用服务商的模型元数据接口。开启“使用 models.dev 查询上下文容量”（`modelsDevEnabled: true`，默认关闭）后，按目录服务地址和完整模型 ID 精确匹配；中转服务或模型别名可填写 `modelsDevProvider` 与 `modelsDevModel` 显式关联目录条目，不改变实际请求模型。目录下载在后台进行并本地缓存，首次无缓存也不阻塞对话；未启用、未匹配或无可用缓存时立即使用备用值，不发送密钥、对话或自定义服务地址。目录容量不是当前服务的实测上限，中转站可能限制得更小。不猜测模型名称，也不用 usage 反推容量。`contextWindow` 在自动模式下是备用值，关闭自动开关（`"manual"`）后是手动上限：0 或省略默认 **32K（32768 Tokens）**，手填允许 8192–2097152 Tokens；目录值允许 4096–2097152。修改后保存，重连生效。桌面区分“models.dev / 手动设置 / 未获取，使用备用值”，悬浮查看请求模型 ID、目录条目及独立输入上限。

估算输入占用达到预算的 **70%** 时在后台提炼摘要，达到 **85%** 时在下次请求前同步压缩；摘要后优先保留最近 **4 轮**完整问答，硬预算不足时可进一步裁剪。文字记忆在设备重连和应用重启后保留，闲置 **4 小时**过期；清空对话会删除摘要和记忆。桌面显示估算占用、输入预算、摘要状态和累计用量，累计输入/输出/总计始终标为 **Tokens（本地估算）**；启动后立即恢复上次保存的累计值，无保存用量时从 0 开始。按实际发送的请求（含历史上下文、工具定义和工具结果）及收到的输出（含可见推理和工具调用）在本地估算，覆盖工具决策、回复与摘要请求，非精确分词，不等于服务商计费用量；UI 不展示服务商上报的 usage。本地累计用量持久化保存，每次请求被接受计入输入和响应结束计入输出后即保存，无需等待一轮对话结束；重连、重启后继续累计，闲置 4 小时只让对话上下文过期。手动清空对话会清零累计用量，并删除保存的对话与用量记录。

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

### 网页刷机（无需 ESP-IDF）

打开 <https://foliageSea.github.io/Aemeath/>，用桌面版 **Chrome / Edge 89+** 通过 USB 直接刷入发布固件（基于 Web Serial 与 [esptool-js](https://github.com/espressif/esptool-js)）：

1. USB-C 连接开发板，点击“连接”，选择 ESP32-S3 串口；页面会校验芯片为 ESP32-S3、Flash 为 16MB。
2. 点击“开始刷入”。固件下载后先做 SHA-256 校验，写入后再做 MD5 回读校验，完成后自动重启。
3. 在“串口日志”查看启动输出（115200）。

默认只写入 bootloader（0x0）、分区表（0x8000）和 app（0x10000），**保留 NVS 中的 Wi‑Fi 配网与屏幕设置**；勾选“整片擦除”才会清除。“本地文件”页可刷自行构建的 `.bin`（合并镜像填 0x0）。若无法自动进入下载模式：按住 BOOT，短按 RESET，松开 BOOT 后重新连接；并关闭 `idf.py monitor` 等占用串口的程序。

本地开发与构建（源码在 `flasher/`，需要 Node 22 + pnpm，先完成 `idf.py build`）：

```bash
cd flasher
pnpm install
pnpm stage        # 把 firmware/build 产物与 manifest.json 复制到 public/firmware/
pnpm dev          # http://localhost:5173
pnpm test
pnpm build        # stage + 类型检查 + 生成 dist/
```

仅在发布 GitHub Release（`release.published`，包括预发布版本）时，[`.github/workflows/flasher-pages.yml`](.github/workflows/flasher-pages.yml) 才会构建该 Release Tag 对应的固件和网页并部署到 GitHub Pages；推送代码、推送 Tag 或保存 Release 草稿不会触发。首次使用需在仓库 **Settings → Pages** 把 Source 设为 “GitHub Actions”，并在 `github-pages` 环境中允许发布 Tag 部署。要打包发版构建的固件，可运行 `pnpm run stage -- --build-dir ../firmware/build-release`（先执行 `bash scripts/build-release.sh firmware`）。

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
- `agent/build/windows-amd64/aemeath-setup-0.0.3-amd64.exe`：可分发的安装包，版本来自 `agent/mygo.json`。
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

电脑主动连接设备的独立 `/voice` WebSocket，采集与播放均使用 **ESP32 设备的麦克风和扬声器**；性能与额度仍通过 `/ws` 独立推送。“设置 → 助手”配置启用状态、角色与对话服务；“设置 → 语音”配置识别、合成、唤醒与试听，顶部“保存设置”统一保存修改。

- **离线唤醒**：默认“你好小爱”，支持 2–12 个模型支持的中文汉字；模型可在桌面端下载安装。待机音频不发送给云端 ASR，唤醒后才识别问题；对话期间再次说出已保存的唤醒词会打断当前对话并立即倾听新问题，可连说“唤醒词 + 新问题”。新版固件持续采集，并以实际播放 PCM 为参考做本地回声消除；“取消”按钮仍停止本轮并回到待命。
- **全局按住说话**：按住 **F8** 录音，松开后识别并发送给 Agent，无需先说唤醒词，也不要求离线模型就绪；需要语音助手已启用、语音通道已连接且设备麦克风未静音。切到其他应用或关闭窗口驻留托盘仍可使用，单次最长 30 秒；按下快捷键会打断当前对话或播报。F8 被占用时自动尝试 **Ctrl + Shift + F8**，实际快捷键或注册错误显示在语音助手页；部分 Mac 键盘需使用 **Fn + F8**。
- **文本输入**：在语音助手页底部输入消息，按 **Enter** 或点击“发送”开始对话，**Shift + Enter** 换行。文字直接交给 Agent，沿用现有上下文、工具调用和设备语音回复；需启用助手并连接设备语音通道，无需唤醒词或麦克风录音。发送失败会保留草稿。
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
