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
| 固件构建 | `idf.py build` 通过，产出 [aemeath.bin](../firmware/build/aemeath.bin)，尺寸 0x57da90（5,757,584）字节，6 MiB 应用分区还剩约 8% |
| macOS 分发 | ARM64 应用与 [DMG](../agent/build/voice-fix/darwin-arm64/aemeath-0.1.0-arm64.dmg) 构建通过；sherpa/ONNX dylib 已随包分发，相对 rpath、依赖路径与 `codesign --verify --deep --strict` 校验通过 |

上述“20 轮”是 C 协议模拟，不是 20 轮真实云端+开发板对话。

## 实板烧录补充（2026-11-09）

- 目标经 esptool 确认：ESP32-S3 revision v0.2，16 MB Flash、8 MB PSRAM；USB JTAG 串口 `/dev/cu.usbmodem11101`。
- 执行 `idf.py -p /dev/cu.usbmodem11101 build flash`，仅写入 bootloader、分区表和 app，未整片擦除，也未写入 NVS。最终 app 5,355,520 字节写入并通过 esptool 哈希校验。
- 第一次实板启动发现网络发送队列约 50 KiB 被 FreeRTOS 默认队列分配到紧缺内部 RAM，触发 `ESP_ERR_NO_MEM` 和重启。已改为显式 PSRAM 分配并匹配释放；可选语音服务的注册失败不再中止监控/配网 HTTP 服务。修复版已重新烧录。随后现有电脑端自动连接，暴露出 41,728 字节 PCM 暂存队列挤占 I²S DMA 内存；已同样将非 DMA 暂存队列迁入 PSRAM，真正的 DMA 缓冲/描述符继续使用内部 RAM。代码强制要求非 IRAM-safe 的 I²S ISR（cache 不可用时屏蔽中断），不允许误启用 cache-off 中断配置。最终启动记录见 [串口日志](../firmware/build/post-flash-final.log)，ELF SHA256 前缀 `a522ce1c8`。
- 最终固件已正常启动：PSRAM 自检通过，原 Wi-Fi 与 6% 背光设置保留，获得 IP `192.168.31.207`；监控代理自动重连并恢复 3 个额度账户。HTTP 根页面返回 200。
- 已见 ES8311/ES7210、MIC1/2 和 16k 双向 I²S 成功初始化；初始化后内部 RAM 空闲 43,367 字节，PSRAM 空闲 2,030,508 字节。短时启动观察未见 panic/循环重启；这仍不是连续 20 轮或音质验收。

## 合成偶发失败与中文绘制修复（2026-11-09）

- 用户截图显示识别和 Agent 回复已完成，失败发生于读取 MiMo 响应。旧代码统一 60 秒 HTTP 总超时，并把响应中断、超时等都显示成 `could not read response`；外层 worker 115 秒还会提前截断较慢合成。截图本身不能判定那一次是超时还是上游断流。
- 现按 ASR 60 秒、Agent 90 秒、TTS 180 秒分别计时；总预算覆盖各阶段，取消仍立即传播。错误区分响应头/响应体、超时/截断/连接中断，并带 HTTP 状态、接收字节数，保留失败阶段耗时；错误及日志均不包含密钥、正文或音频。
- 已生成的回复可点“重试回复语音”单独重试 TTS，不重跑 Agent/设备工具；再次失败后文字仍保留。不会自动重发传输结果不确定的付费 POST。
- 全量 Go 测试与 vet 通过；voice/voiceapi 竞态测试覆盖响应体超时、调用方取消、截断、隐私错误、单次关闭和不重放请求。MiMo 适配器另有 9 个聚焦用例，独立执行 `go test -mod=readonly -count=5 ./internal/voiceapi`、`go test -mod=readonly -race ./internal/voiceapi` 与 vet 均通过；慢响应测试实际完成延后返回的 WAV→PCM 传输。桌面 UI 回归验证重试命令只发送现有文字；真实会话测试确认 TTS 拥有完整 180 秒预算且失败耗时保留。
- 当前 LVGL 9.6 的压缩字形 RLE 游标位于全局上下文，双绘制线程会竞争。中文改为未压缩 4bpp 位图，并启用 `CONFIG_LV_FONT_FMT_TXT_LARGE=y` 防止超过 1 MiB 后字形索引截断；保持双绘制线程与现有刷新率。字形 bitmap 增加 335,222 字节；相同字幕/状态不再重复重设标签、分配内存与布局，长字幕按原文比较以兼容省略号处理。固件重新编译和烧录通过哈希校验。
- 单绘制线程及双 pthread 绘制线程下的界面回归均通过；额外 4 线程连续 120,000 次字形解码与串行基准 hash 一致，覆盖超过 1 MiB 的高索引字符，重复 100 次相同长字幕不触发标签写入；[CTest 记录](../firmware/build-avatar-host/font-stability/Testing/Temporary/LastTest.log)可查。
- 修复版 ARM64 桌面应用已构建、验证动态库/签名并替换当前安装版本；用户设置与密钥保留，不随安装包打包。原应用备份在忽略的构建输出目录。
- 实板直接发送本地参考音频完成 3 轮，每轮 32,000 采样（100 帧、约 2 秒），均获得实际 `playing` 和完整 `played` 回执，持续时间约 2.16–2.20 秒；字幕在音频和角色动画运行时持续更新。最终三轮[输出记录](../agent/build/voice-smoke/result.log)和[设备日志](../firmware/build/voice-fix-final.log)可查，验证程序退出码为 0，之后桌面监控代理已重新连接。最终音频初始化后内部 RAM 空闲 42,439 字节、PSRAM 空闲 1,628,620 字节，观察未见 panic 或循环重启。验证后 HTTP 仍返回 200。工具最初因默认参考路径为空未执行播放，已修正；也已按 IDF 直接关闭 socket 的语义处理关闭阶段 EOF。此次没有调用付费云端 API，也没有拍摄 LCD 来判定所有“波动”现象是否消失。

## 仅唤醒词被报为空语音的修复（2026-11-09）

用户截图中的精确错误 `no speech recognized` 来自识别文字剔除“你好小爱”之后为空的分支，尚未调用 Agent 或 TTS。现将仅识别到唤醒词作为正常的继续聆听结果：保持同一轮次，重置 VAD/录音缓冲，提示“我在听，请继续说出问题”；最多重开一次录音。5 秒静音或再次只有唤醒词时正常回到待命，保留取消操作，不自动循环付费识别。

本地 WebSocket/ASR 假服务覆盖继续提问并完成播放、继续静音、重复唤醒词、取消以及初次纯静音共五种场景；验证二次录音不含旧唤醒样本、只有有效问题才调用一次 Agent/TTS，提示进入普通 Notice 而非 Error。语音与桌面 UI 竞态测试、Go 全量测试和 vet 通过。修复仅需更新桌面应用，设备已有 state.text 能显示相同中文提示；未调用真实云端验证识别准确率。

## 缩短默认克隆参考（2026-11-09）

- 本机已配置的参考路径保持不变，音频从 10.624 秒剪到 4.20 秒；选在 4.02–4.76 秒的停顿中，保留约 180ms 尾部静音。完整原版已保存在同目录 `reference-original.wav`。
- 短版 48kHz/24bit 单声道 WAV 为 604,844 字节，SHA256 `de29dbfe3950a2020aeaafb88ad0cb9e9a6c606a983ff546386cd44ced6232bb`。发送前转成 16kHz PCM16 WAV 为 134,444 字节、Base64 179,260 字节，相比旧版 453,352 字节减少约 60.5%。
- 本机三次离线实际参考处理为 37、37、38ms，旧版为 96、97、95ms。此测量只含本地解码/重采样/编码，不代表云端推理同比例加速；没有调用付费云端做时延或音色相似度对照。
- 资源安装器校验源文件后按完整 PCM 帧截取，校验短版后原子替换，保留原始文件；已缓存的旧版可无网络升级。新应用合成/参考试听入口也会对匹配旧校验值的默认样本执行本地升级，自定义音频及显式选取的完整原版保持可用。
- 已用模拟含奇数字节元数据的 WAV 验证截断位置、旧缓存迁移与幂等、完整备份保留、下载损坏拒绝、备份损坏不覆盖、应用不触碰自定义参考。资源与语音竞态测试、完整 Go 测试和 vet 通过；本机资源完整校验通过。

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

角色图片仍需 `CONFIG_LV_BIN_DECODER_RAM_LOAD=y`。中文字体现使用未压缩 4bpp 位图，避免 LVGL 9.6 的全局 RLE 解码上下文被两个绘制线程同时改写；不再依赖 `CONFIG_LV_USE_FONT_COMPRESSED=y` 来显示中文。旧的压缩开关可以保留以兼容其他字体，但不要将语音中文字体重新生成成压缩格式。仅 C 编译成功并不能证明图像、文字和并行绘制正确。

仍有上游 Kconfig bool 默认值提示和 LVGL 旧 API 弃用警告，但不会阻断当前构建。macOS 链接器会提示重复的相对 rpath 已忽略，最终校验确认没有开发机模块缓存依赖。
