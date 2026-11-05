# Aemeath — PC 性能监控台

Go 采集端（macOS / Windows）通过 Wi‑Fi WebSocket 把 CPU、内存、磁盘、网络、温度、GPU 推送到
Waveshare **ESP32‑S3‑Touch‑LCD‑4B**（480×480 ST7701 + GT911），在单页 LVGL 仪表盘上显示。

```
agent/      Go 1.27 采集端（gopsutil + cgo IOKit / LibreHardwareMonitor WMI + NVML）
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
- 开机自启：macOS 用 `~/Library/LaunchAgents` plist；Windows 把 exe 快捷方式放进 `shell:startup`。

## Magpie 订阅额度页

屏幕左右滑动切换到第二页「AI QUOTA」，显示本机 [Magpie](http://127.0.0.1:3425) 网关聚合的各订阅账户额度：
每个窗口一条进度条（≥75% 黄、≥90% 红）、用量 / 计数、重置倒计时，顶部是今日与 30 天花费和调用次数。

- agent 每 30 秒读取 `GET /v1/magpie/quotas` 与 `/v1/magpie/usage` 并转发（不会转发账户邮箱）。
- `-magpie http://host:port` 指定网关地址，`-magpie ""` 关闭；网关不可用时只记日志，不影响性能数据。
- 超过 3 分钟未收到额度数据，页面右上角显示红色 `stale`。

## 协议

每秒一帧 JSON 文本（字段见 `agent/internal/proto/proto.go`，固件解析见 `firmware/main/metrics.c`），
缺失字段直接省略。设备用 `ts` + `tz` 同步时钟。
额度数据是另一种帧（`"type":"quota"`，见 `proto.QuotaFrame` 与 `firmware/main/quota.c`）；没有 `type` 的帧按性能数据处理。
