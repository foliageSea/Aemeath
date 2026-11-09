# 语音功能验证记录

本次验证环境：macOS ARM64、Go 模块声明版本 1.27.3、ESP-IDF 6.0.2、LVGL 9.6.0~1、esp_codec_dev 1.6.2、sherpa-onnx Go/native 2.0.1。验证日期：2026-11-09。

## 已通过

| 项目 | 结果和覆盖范围 |
| --- | --- |
| Go 全量测试 | `go test -mod=readonly ./...` 通过，包括原监控/额度、桌面、语音、MiMo、Eino、唤醒资源包 |
| Go 静态检查 | `go vet -mod=readonly ./...` 通过 |
| 语音并发与取消 | `go test -race` 覆盖 desktop、voice、brain、voiceapi、wake、CLI、桌面 UI；取消旧轮次、清空历史、重连、配置重启和工具取消不误断监控连接 |
| MiMo 契约 | 本地 HTTP 假服务验证 ASR input_audio、zh、指定模型、voiceclone assistant 文本、audio.voice WAV data URL、WAV 回包、限流、错误脱敏、取消和大小边界；没有调用真实付费 API |
| Eino Agent | 本地 OpenAI 兼容假服务验证真实 Eino ReAct 工具调用、工具结果、无工具模式、10 轮历史上限、步数与工具并发上限、错误和取消 |
| 跨端协议 | Go WS 假设备验证握手、epoch/turn/seq、额度限流、实际 playing/played 回执、迟到结果与断线；额外修复初始 idle 的显式 turn=0、UTF-8 字幕和设备 4096 字节消息上限 |
| C 播放协议 | Clang ASan/UBSan 下验证无效帧、序号、轮次、sample count、32 帧流控窗口以及连续 20 个模拟轮次完整排空 |
| 资源安装 | 默认用户缓存的模型、Silero VAD、参考 WAV 全部通过固定 SHA-256 校验，安装命令与离线 `-verify` 可用 |
| 原生模型 | 真实模型运行 32 秒静音，不唤醒且正确轮换有界缓存；真实参考 WAV 被 Silero 判为语音 |
| 正负关键词 | macOS 本地 Tingting 合成“你好小爱，帮我看看电脑现在的温度。”与无关句“今天的天气很好，请帮我看看电脑温度。”；真实模型命中前者“你好小爱”（对齐 1.040–1.800 秒，含 1 秒前置静音），未命中后者。样本不是实机麦克风录音，也不代表准确率统计 |
| 无 cgo | wake/voice/brain/voiceapi 测试通过；离线唤醒明确不可用，不替换成伪唤醒或云端待机识别 |
| Windows 兼容 | amd64 与 arm64 的无 cgo 监控/CLI 交叉编译通过；Windows 原生语音安装包与硬件运行未验证 |
| 动画资源 | 9 动作、57 帧均保留，2,334,720 字节 I8 资源逐字节可重复生成；透明边缘检查通过；中文字体 7,541 字符可重复生成 |
| LVGL 真实渲染 | 主机实际 LVGL 软件渲染通过像素断言，能看到中文与人物；测挥手覆盖快到来的聆听状态、失败逐帧后停最后帧、听/说波形、静音重连重置、控件、UTF-8、3 秒恢复、手动切页优先、tick 溢出 |
| 固件构建 | `idf.py build` 通过，产出 [aemeath.bin](../firmware/build/aemeath.bin)，尺寸 0x51b800（5,355,520）字节，6 MiB 应用分区还剩约 15% |
| macOS 分发 | ARM64 应用与 [DMG](../agent/build/voice-validation/darwin-arm64/aemeath-0.1.0-arm64.dmg) 构建通过；sherpa/ONNX dylib 已随包分发，相对 rpath、依赖路径与 `codesign --verify --deep --strict` 校验通过 |

上述“20 轮”是 C 协议模拟，不是 20 轮真实云端+开发板对话。

## 实板烧录补充（2026-11-09）

- 目标经 esptool 确认：ESP32-S3 revision v0.2，16 MB Flash、8 MB PSRAM；USB JTAG 串口 `/dev/cu.usbmodem11101`。
- 执行 `idf.py -p /dev/cu.usbmodem11101 build flash`，仅写入 bootloader、分区表和 app，未整片擦除，也未写入 NVS。最终 app 5,355,520 字节写入并通过 esptool 哈希校验。
- 第一次实板启动发现网络发送队列约 50 KiB 被 FreeRTOS 默认队列分配到紧缺内部 RAM，触发 `ESP_ERR_NO_MEM` 和重启。已改为显式 PSRAM 分配并匹配释放；可选语音服务的注册失败不再中止监控/配网 HTTP 服务。修复版已重新烧录。随后现有电脑端自动连接，暴露出 41,728 字节 PCM 暂存队列挤占 I²S DMA 内存；已同样将非 DMA 暂存队列迁入 PSRAM，真正的 DMA 缓冲/描述符继续使用内部 RAM。代码强制要求非 IRAM-safe 的 I²S ISR（cache 不可用时屏蔽中断），不允许误启用 cache-off 中断配置。最终启动记录见 [串口日志](../firmware/build/post-flash-final.log)，ELF SHA256 前缀 `a522ce1c8`。
- 最终固件已正常启动：PSRAM 自检通过，原 Wi-Fi 与 6% 背光设置保留，获得 IP `192.168.31.207`；监控代理自动重连并恢复 3 个额度账户。HTTP 根页面返回 200。
- 已见 ES8311/ES7210、MIC1/2 和 16k 双向 I²S 成功初始化；初始化后内部 RAM 空闲 43,367 字节，PSRAM 空闲 2,030,508 字节。短时启动观察未见 panic/循环重启；这仍不是连续 20 轮或音质验收。

## 视觉结果

[角色状态总览](../firmware/build-avatar-host/avatar-lvgl-preview.png) 来自主机实际 LVGL 快照（每个状态原生 480×480），不是物理 LCD 拍摄。生成方法见 [主机验证说明](../firmware/tools/avatar_tests/README.md)。

## 尚需真实环境验收

用户随后授权烧录，已向实板写入并通过哈希校验；没有使用真实 MiMo/LLM Key 发起识别或声音复刻请求，因此以下项目仍没有宣称通过：

- 实板 ES7210 双麦克风信号、I²S 时钟/声道、ES8311 音量及功放噪声；
- LCD 刷新+Wi-Fi+音频同时运行时的 DMA heap、PSRAM、CPU、丢帧、欠载与连续 20 轮稳定性；
- 不同说话人、距离、背景噪声下的唤醒准确率、误唤醒率和关键词后首字完整性；
- 实际账号对 `mimo-v2.5-asr`、`mimo-v2.5-tts-voiceclone` 的权限、额度、限速、复刻相似度与端到端时延；
- 实物触摸、屏幕临时唤醒与手动操作优先级；
- macOS Intel/Universal 实际运行、Windows cgo 语音版编译/签名/安装、Windows 真机运行。

代码与构建产物已经具备这些验收的入口。使用步骤与限制见 [使用说明](voice-agent.md)。

## 复现命令

```bash
# Go（在 agent 目录）
go test -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -race ./internal/desktop ./internal/voice ./internal/brain ./internal/voiceapi ./internal/wake ./cmd/aemeath-agent ./cmd/aemeath-desktop
CGO_ENABLED=0 go test -mod=readonly ./internal/wake ./internal/voice ./internal/brain ./internal/voiceapi
go run ./cmd/aemeath-voice-assets -verify
AEMEATH_WAKE_SMOKE=1 go test ./internal/wake -run TestNativeSmoke -v

# 可选：提供自有 16k/其他支持格式 WAV，完全离线测试关键词正负样本
AEMEATH_WAKE_POSITIVE_WAV=/absolute/target.wav AEMEATH_WAKE_NEGATIVE_WAV=/absolute/unrelated.wav \
  go test ./internal/wake -run TestNativeWakePhrase -v

# C 播放协议（在 firmware 目录）
clang -Wall -Wextra -Werror -fsanitize=address,undefined -I main/voice \
  tests/voice_protocol_test.c main/voice/protocol.c -o build/voice_protocol_tests
./build/voice_protocol_tests

# IDF 固件（已加载 IDF 6.0.2）
idf.py build
```

固件需同时启用 `CONFIG_LV_USE_FONT_COMPRESSED=y` 与 `CONFIG_LV_BIN_DECODER_RAM_LOAD=y`，已加入默认配置并用于本次成功构建；从已有旧配置更新时也需要确认这两个选项，不能仅以 C 编译成功判断动画能显示。

仍有上游 Kconfig bool 默认值提示和 LVGL 旧 API 弃用警告，但不会阻断当前构建。macOS 链接器会提示重复的相对 rpath 已忽略，最终校验确认没有开发机模块缓存依赖。
